package contextgraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EntityIndex is the structural entity/edge map (Graphify-style layer).
// Not safe for concurrent use without external sync — Store holds the mutex
// and embeds EntityIndex as a facade component (SOLID SRP split from EventLog).
type EntityIndex struct {
	entities map[string]*Entity
}

func (idx *EntityIndex) ensure() {
	if idx.entities == nil {
		idx.entities = make(map[string]*Entity)
	}
}

func (idx *EntityIndex) len() int {
	if idx == nil || idx.entities == nil {
		return 0
	}
	return len(idx.entities)
}

// upsert indexes an entity (defensive Attrs copy). Caller persists to disk if needed.
func (idx *EntityIndex) upsert(e Entity) Entity {
	idx.ensure()
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
	idx.entities[e.ID] = &cp
	return cp
}

func (idx *EntityIndex) get(id string) (Entity, bool) {
	idx.ensure()
	e, ok := idx.entities[strings.TrimSpace(id)]
	if !ok || e == nil {
		return Entity{}, false
	}
	return *e, true
}

func (idx *EntityIndex) list(turnID string, limit int) []Entity {
	idx.ensure()
	if limit <= 0 {
		limit = 200
	}
	out := make([]Entity, 0, len(idx.entities))
	turnID = strings.TrimSpace(turnID)
	for _, e := range idx.entities {
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

func persistEntityJSONL(dir string, e Entity) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "entities.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(e)
}
