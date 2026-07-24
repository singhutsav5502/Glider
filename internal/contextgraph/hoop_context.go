package contextgraph

import (
	"fmt"
	"strings"
)

// Well-known hoop context keys (labels + attrs["key"]) for context_query / LookupHoopContext.
const (
	HoopKeyClonePath = "clone_path"
	HoopKeyGoal      = "goal"
	HoopKeyPlan      = "plan"
	HoopKeyFileTree  = "file-tree"
)

// RecordHoopContext upserts a RUNTIME note keyed for hoop turns.
// Label and attrs["key"] match so context_query "clone_path" / key=goal retrieves it.
// Stable id: hoop-ctx-<sanitizedTurn>-<key>. Safe for concurrent stages (upsert).
func (s *Store) RecordHoopContext(turnID, key, value string) {
	if s == nil {
		return
	}
	key = normalizeHoopKey(key)
	if key == "" {
		return
	}
	value = strings.TrimSpace(value)
	turnID = strings.TrimSpace(turnID)
	id := hoopContextID(turnID, key)
	s.RecordFact(turnID, Fact{
		ID:         id,
		Kind:       KindNote,
		Label:      key,
		Provenance: ProvenanceRuntime,
		Attrs: map[string]string{
			"key":   key,
			"value": truncateLabel(value, 2000),
			"text":  truncateLabel(value, 2000),
		},
	})
}

// LookupHoopContext returns the value for a hoop context key (exact label / attrs.key).
func (s *Store) LookupHoopContext(turnID, key string) (string, bool) {
	if s == nil {
		return "", false
	}
	key = normalizeHoopKey(key)
	if key == "" {
		return "", false
	}
	turnID = strings.TrimSpace(turnID)
	if id := hoopContextID(turnID, key); id != "" {
		if e, ok := s.GetEntity(id); ok {
			return hoopValue(e), true
		}
	}
	// Fallback: scan by label/key (covers legacy hoop-goal-* facts from seedSharedContext).
	for _, e := range s.Entities(turnID, 400) {
		if e.Kind == KindEdge {
			continue
		}
		if strings.EqualFold(e.Label, key) {
			return hoopValue(e), true
		}
		if e.Attrs != nil && strings.EqualFold(e.Attrs["key"], key) {
			return hoopValue(e), true
		}
	}
	return "", false
}

// HoopContextDigest returns a short multi-line summary of well-known keys for prompt injection.
func (s *Store) HoopContextDigest(turnID string, keys ...string) string {
	if s == nil {
		return ""
	}
	if len(keys) == 0 {
		keys = []string{HoopKeyClonePath, HoopKeyGoal, HoopKeyPlan, HoopKeyFileTree}
	}
	var b strings.Builder
	for _, k := range keys {
		if v, ok := s.LookupHoopContext(turnID, k); ok && strings.TrimSpace(v) != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "%s: %s", normalizeHoopKey(k), truncateLabel(v, 400))
		}
	}
	return b.String()
}

func hoopContextID(turnID, key string) string {
	key = normalizeHoopKey(key)
	if key == "" {
		return ""
	}
	turn := strings.TrimSpace(turnID)
	if turn == "" {
		turn = "global"
	}
	// Keep id filesystem / jsonl friendly.
	turn = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		case r == ':' || r == '/':
			return '-'
		default:
			return '_'
		}
	}, turn)
	return "hoop-ctx-" + turn + "-" + key
}

func normalizeHoopKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	key = strings.ReplaceAll(key, " ", "_")
	switch key {
	case "clone_path", "clone-path":
		return HoopKeyClonePath
	case "file-tree", "file_tree", "filetree":
		return HoopKeyFileTree
	case "goal", "plan":
		return key
	default:
		return key
	}
}

func hoopValue(e Entity) string {
	if e.Attrs != nil {
		if v := strings.TrimSpace(e.Attrs["value"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(e.Attrs["text"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(e.Attrs["path"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(e.Attrs["summary"]); v != "" {
			return v
		}
		// Keyed hoop-context notes use Label as the key name (goal/plan/…);
		// empty value must not fall back to the label.
		if strings.TrimSpace(e.Attrs["key"]) != "" {
			return ""
		}
	}
	return strings.TrimSpace(e.Label)
}
