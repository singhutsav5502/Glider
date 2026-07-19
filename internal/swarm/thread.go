package swarm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/contextkit"
)

// ThreadState is durable swarm run state (Slate-inspired thread, not full weave).
// Persisted under ~/.glider/swarm/threads/<id>.json so multi-wave runs survive restarts.
type ThreadState struct {
	ID            string             `json:"id"`
	TurnID        string             `json:"turn_id"`
	Goal          string             `json:"goal,omitempty"`
	Waves         []WaveRecord       `json:"waves,omitempty"`
	Merged        contextkit.Episode `json:"merged,omitempty"`
	MergedSummary string             `json:"merged_summary,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

// WaveRecord is one FanOut wave within a durable thread.
type WaveRecord struct {
	Index   int                `json:"index"`
	Prompt  string             `json:"prompt,omitempty"`
	Results []ResultView       `json:"results,omitempty"`
	Merged  contextkit.Episode `json:"merged,omitempty"`
	At      time.Time          `json:"at"`
}

// ThreadStore persists ThreadState JSON files.
type ThreadStore struct {
	mu  sync.Mutex
	Dir string
}

// DefaultThreadDir returns ~/.glider/swarm/threads.
func DefaultThreadDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider", "swarm", "threads")
	}
	return filepath.Join(home, ".glider", "swarm", "threads")
}

// NewThreadStore constructs a store; empty dir → DefaultThreadDir.
func NewThreadStore(dir string) *ThreadStore {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultThreadDir()
	}
	return &ThreadStore{Dir: dir}
}

func (ts *ThreadStore) path(id string) string {
	id = strings.TrimSpace(id)
	id = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, id)
	if id == "" {
		id = "thread"
	}
	return filepath.Join(ts.Dir, id+".json")
}

// Save writes thread state atomically (temp + rename).
func (ts *ThreadStore) Save(st *ThreadState) error {
	if ts == nil || st == nil {
		return fmt.Errorf("thread: nil store or state")
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if err := os.MkdirAll(ts.Dir, 0o755); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC()
	if st.CreatedAt.IsZero() {
		st.CreatedAt = st.UpdatedAt
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := ts.path(st.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a thread by id.
func (ts *ThreadStore) Load(id string) (*ThreadState, error) {
	if ts == nil {
		return nil, fmt.Errorf("thread: nil store")
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	data, err := os.ReadFile(ts.path(id))
	if err != nil {
		return nil, err
	}
	var st ThreadState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// AppendWave loads-or-creates the thread, appends a wave, saves.
func (ts *ThreadStore) AppendWave(threadID, turnID, goal string, wave WaveRecord) (*ThreadState, error) {
	if ts == nil {
		return nil, fmt.Errorf("thread: nil store")
	}
	st, err := ts.Load(threadID)
	if err != nil {
		st = &ThreadState{
			ID:        threadID,
			TurnID:    turnID,
			Goal:      goal,
			CreatedAt: time.Now().UTC(),
		}
	}
	if st.TurnID == "" {
		st.TurnID = turnID
	}
	if st.Goal == "" {
		st.Goal = goal
	}
	if wave.At.IsZero() {
		wave.At = time.Now().UTC()
	}
	st.Waves = append(st.Waves, wave)
	if err := ts.Save(st); err != nil {
		return st, err
	}
	return st, nil
}

// SetMerged updates the thread's woven summary.
func (ts *ThreadStore) SetMerged(threadID string, ep contextkit.Episode, summary string) error {
	st, err := ts.Load(threadID)
	if err != nil {
		return err
	}
	st.Merged = ep
	st.MergedSummary = summary
	return ts.Save(st)
}
