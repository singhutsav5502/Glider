package tools

import (
	"context"
	"encoding/json"
	"strings"
)

type contextQuery struct{ store ContextStore }

func (t *contextQuery) Name() string { return "context_query" }
func (t *contextQuery) Description() string {
	return "Query dual-layer contextgraph (hoop facts + file-tree). Input: turn_id [key=clone_path|goal|plan|file-tree] [kind=note|file|dir] [prov=RUNTIME|EXTRACTED|INFERRED] [path=from->to] [neigh=id] keyword (OR-separated ok)"
}
func (t *contextQuery) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"turn_id":{"type":"string"},"query":{"type":"string"},"key":{"type":"string","description":"clone_path|goal|plan|file-tree"},"kind":{"type":"string"},"prov":{"type":"string"},"path":{"type":"string"},"neigh":{"type":"string"}}}`)
}
func (t *contextQuery) Call(_ context.Context, input string, args json.RawMessage) (Result, error) {
	if t.store == nil {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: true, Stubbed: true, Output: "no context store"}, nil
	}
	input = strings.TrimSpace(input)
	// Prefer structured args when present.
	if len(args) > 0 && string(args) != "null" {
		var m map[string]any
		if json.Unmarshal(args, &m) == nil {
			var parts []string
			if v, ok := m["turn_id"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, strings.TrimSpace(v))
			}
			if v, ok := m["key"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, "key="+strings.TrimSpace(v))
			}
			if v, ok := m["kind"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, "kind="+strings.TrimSpace(v))
			}
			if v, ok := m["prov"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, "prov="+strings.TrimSpace(v))
			}
			if v, ok := m["path"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, "path="+strings.TrimSpace(v))
			}
			if v, ok := m["neigh"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, "neigh="+strings.TrimSpace(v))
			}
			if v, ok := m["query"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, strings.TrimSpace(v))
			}
			if len(parts) > 0 {
				input = strings.Join(parts, " ")
			}
		}
	}
	if raw, okStore := t.store.(interface{ QueryRaw(string) string }); okStore {
		return ok(t.Name(), raw.QueryRaw(input)), nil
	}
	// Fallback: simple turn_id + keyword.
	turnID, q, _ := strings.Cut(input, " ")
	turnID = strings.TrimSpace(turnID)
	q = strings.TrimSpace(q)
	if q == "" {
		q = turnID
		turnID = ""
	}
	return ok(t.Name(), t.store.Query(turnID, q, 20)), nil
}
