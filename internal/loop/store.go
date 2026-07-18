package loop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/contextkit"
)

const (
	defaultOutcomeRing = 64
	stateFileSuffix    = ".json"
)

// DefaultDir returns ~/.glider/loops (or %USERPROFILE%\.glider\loops).
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider", "loops")
	}
	return filepath.Join(home, ".glider", "loops")
}

// Store persists LoopState JSON files under Dir.
type Store struct {
	mu  sync.Mutex
	Dir string
}

// NewStore creates a file-backed loop store. Empty dir → DefaultDir().
func NewStore(dir string) *Store {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultDir()
	}
	return &Store{Dir: dir}
}

func (s *Store) ensureDir() error {
	return os.MkdirAll(s.Dir, 0o755)
}

func (s *Store) path(id string) string {
	id = sanitizeID(id)
	return filepath.Join(s.Dir, id+stateFileSuffix)
}

func sanitizeID(id string) string {
	id = strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "loop"
	}
	return out
}

// List returns all persisted loop states (sorted by id).
func (s *Store) List() ([]LoopState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []LoopState
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), stateFileSuffix) {
			continue
		}
		st, err := s.readFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.ID < out[j].Spec.ID })
	return out, nil
}

// Get loads one loop by id.
func (s *Store) Get(id string) (*LoopState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readFile(s.path(id))
}

func (s *Store) readFile(path string) (*LoopState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st LoopState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Save writes loop state atomically.
func (s *Store) Save(st *LoopState) error {
	if st == nil {
		return fmt.Errorf("nil state")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := s.path(st.Spec.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Delete removes a loop state file.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// AppendOutcome pushes an outcome onto the ring and updates counters.
func (st *LoopState) AppendOutcome(o IterationOutcome, ring int) {
	if ring <= 0 {
		ring = defaultOutcomeRing
	}
	st.Outcomes = append(st.Outcomes, o)
	if len(st.Outcomes) > ring {
		st.Outcomes = append([]IterationOutcome(nil), st.Outcomes[len(st.Outcomes)-ring:]...)
	}
	if o.Success {
		st.ConsecutiveOK++
		st.ConsecutiveFail = 0
	} else {
		st.ConsecutiveFail++
		st.ConsecutiveOK = 0
	}
}

// CreateState builds a fresh idle state from a normalized spec.
func CreateState(spec LoopSpec) *LoopState {
	now := time.Now().UTC()
	return &LoopState{
		Spec:   spec,
		Status: StatusIdle,
		Checkpoint: contextkit.LoopCheckpoint{
			Goal:       spec.Prompt,
			EvalStatus: "pending",
		},
		UpdatedAt: now,
	}
}
