package contextgraph

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Entity kinds for the structural layer (Graphify-inspired nodes/edges).
const (
	KindEntity  = "entity"
	KindEdge    = "edge"
	KindThread  = "thread"
	KindWave    = "wave"
	KindEpisode = "episode"
	KindWorker  = "worker"
	KindNote    = "note"
)

// Relation verbs on edges.
const (
	RelProduced   = "produced"
	RelFollows    = "follows"
	RelMergedInto = "merged_into"
	RelPartOf     = "part_of"
)

// Entity is a durable node or edge in the structural context layer.
type Entity struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"` // entity|edge|thread|wave|episode|worker|note
	Label      string            `json:"label"`
	TurnID     string            `json:"turn_id,omitempty"`
	Provenance Provenance        `json:"provenance,omitempty"`
	From       string            `json:"from,omitempty"` // edge source id
	To         string            `json:"to,omitempty"`   // edge target id
	Relation   string            `json:"relation,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	At         time.Time         `json:"at,omitempty"`
}

func (s *Store) ensureEntitiesLocked() {
	if s.entities == nil {
		s.entities = make(map[string]*Entity)
	}
}

// upsertEntityLocked indexes an entity and schedules disk persist.
func (s *Store) upsertEntityLocked(e Entity) {
	s.ensureEntitiesLocked()
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if e.Provenance == "" {
		e.Provenance = ProvenanceInferred
	}
	if e.Attrs != nil {
		cp := make(map[string]string, len(e.Attrs))
		for k, v := range e.Attrs {
			cp[k] = v
		}
		e.Attrs = cp
	}
	cp := e
	s.entities[e.ID] = &cp
	s.persistEntityLocked(cp)
}

func (s *Store) persistEntityLocked(e Entity) {
	if strings.TrimSpace(s.Dir) == "" {
		return
	}
	_ = os.MkdirAll(s.Dir, 0o755)
	path := filepath.Join(s.Dir, "entities.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(e)
}

// LoadEntities replays entities.jsonl from Dir into the in-memory index.
// Later lines with the same ID win (upsert). Returns count of lines applied.
func (s *Store) LoadEntities() (int, error) {
	if s == nil || strings.TrimSpace(s.Dir) == "" {
		return 0, nil
	}
	path := filepath.Join(s.Dir, "entities.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureEntitiesLocked()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entity
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.ID == "" {
			continue
		}
		if e.Attrs != nil {
			cp := make(map[string]string, len(e.Attrs))
			for k, v := range e.Attrs {
				cp[k] = v
			}
			e.Attrs = cp
		}
		cp := e
		s.entities[e.ID] = &cp
		n++
	}
	return n, sc.Err()
}

// Entities returns a snapshot of structural nodes/edges, optionally filtered by turnID.
func (s *Store) Entities(turnID string, limit int) []Entity {
	if s == nil {
		return nil
	}
	if limit <= 0 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureEntitiesLocked()
	out := make([]Entity, 0, len(s.entities))
	turnID = strings.TrimSpace(turnID)
	for _, e := range s.entities {
		if turnID != "" && e.TurnID != turnID {
			continue
		}
		out = append(out, *e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// GetEntity looks up one entity by id.
func (s *Store) GetEntity(id string) (Entity, bool) {
	if s == nil {
		return Entity{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureEntitiesLocked()
	e, ok := s.entities[strings.TrimSpace(id)]
	if !ok || e == nil {
		return Entity{}, false
	}
	return *e, true
}

// RecordEdge upserts a directed relation between two entity ids.
func (s *Store) RecordEdge(turnID, id, from, to, relation string, prov Provenance, attrs map[string]string) {
	if s == nil || from == "" || to == "" {
		return
	}
	if id == "" {
		id = from + "->" + to + ":" + relation
	}
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["from"] = from
	attrs["to"] = to
	attrs["relation"] = relation
	s.RecordFact(turnID, Fact{
		ID:         id,
		Kind:       KindEdge,
		Label:      relation + " " + from + "→" + to,
		Provenance: prov,
		Attrs:      attrs,
		From:       from,
		To:         to,
		Relation:   relation,
	})
}

// WaveOutputs returns RUNTIME episode/worker summaries recorded for a turn
// (used to seed wave N+1 prompts from the shared graph).
func (s *Store) WaveOutputs(turnID string, waveIndex int, limit int) []string {
	if s == nil {
		return nil
	}
	if limit <= 0 {
		limit = 16
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureEntitiesLocked()
	wantWave := ""
	if waveIndex >= 0 {
		wantWave = strconv.Itoa(waveIndex)
	}
	var out []string
	for _, e := range s.entities {
		if turnID != "" && e.TurnID != turnID {
			continue
		}
		if e.Kind != KindEpisode && e.Kind != KindWorker && e.Kind != KindWave {
			continue
		}
		if wantWave != "" && e.Attrs != nil {
			if w := e.Attrs["wave"]; w != "" && w != wantWave {
				continue
			}
		}
		sum := e.Label
		if e.Attrs != nil {
			if s := e.Attrs["summary"]; s != "" {
				sum = s
			}
		}
		sum = strings.TrimSpace(sum)
		if sum == "" {
			continue
		}
		out = append(out, sum)
		if len(out) >= limit {
			break
		}
	}
	return out
}
