package contextgraph

import (
	"fmt"
	"strings"
	"time"
)

// Provenance tags an edge/fact as extracted from source vs inferred by Glider
// vs observed at runtime. Inspired by Graphify EXTRACTED / INFERRED labels;
// RUNTIME covers hoop/swarm orchestration writes (see planning/slate_weave_graphify_plan.md).
type Provenance string

const (
	ProvenanceExtracted Provenance = "EXTRACTED"
	ProvenanceInferred  Provenance = "INFERRED"
	ProvenanceRuntime   Provenance = "RUNTIME"
)

// Fact is a lightweight queryable node/edge for the structural entity layer.
// RecordFact upserts into the entity store and appends a FactRecorded event.
type Fact struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"` // entity|edge|thread|wave|episode|worker|note|relation
	Label      string            `json:"label"`
	TurnID     string            `json:"turn_id,omitempty"`
	Provenance Provenance        `json:"provenance,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	At         time.Time         `json:"at,omitempty"`
	From       string            `json:"from,omitempty"`
	To         string            `json:"to,omitempty"`
	Relation   string            `json:"relation,omitempty"`
}

// Query searches the dual-layer context: append-only events AND entity/edge store.
// Returns a human-readable summary for agent tools / relevancy hints.
// Graphify methodology: one query surface over structured graph + provenance tags.
func (s *Store) Query(turnID, q string, limit int) string {
	if s == nil {
		return ""
	}
	if limit <= 0 {
		limit = 20
	}
	q = strings.TrimSpace(strings.ToLower(q))
	var hits []string

	// Layer 1: structural entities / edges.
	ents := s.Entities(turnID, 200)
	for i := len(ents) - 1; i >= 0 && len(hits) < limit; i-- {
		e := ents[i]
		blob := strings.ToLower(e.Kind + " " + e.Label + " " + e.TurnID + " " + e.From + " " + e.To + " " + e.Relation + " " + attrsBlob(e.Attrs))
		if q != "" && !strings.Contains(blob, q) {
			continue
		}
		prov := e.Provenance
		if prov == "" {
			prov = ProvenanceInferred
		}
		hits = append(hits, fmt.Sprintf("[entity:%s] %s id=%s kind=%s turn=%s %s",
			prov, e.Label, e.ID, e.Kind, e.TurnID, attrsBlob(e.Attrs)))
	}

	// Layer 2: runtime event log.
	var events []Event
	if turnID != "" {
		if v, ok := s.Turn(turnID); ok {
			events = v.Events
		}
	} else {
		events = s.RecentEvents(200)
	}
	for i := len(events) - 1; i >= 0 && len(hits) < limit; i-- {
		ev := events[i]
		blob := strings.ToLower(string(ev.Kind) + " " + ev.TurnID + " " + ev.Actor + " " + attrsBlob(ev.Attrs))
		if q != "" && !strings.Contains(blob, q) {
			continue
		}
		prov := ProvenanceRuntime
		if ev.Attrs != nil {
			switch ev.Attrs["provenance"] {
			case string(ProvenanceInferred):
				prov = ProvenanceInferred
			case string(ProvenanceExtracted):
				prov = ProvenanceExtracted
			case string(ProvenanceRuntime):
				prov = ProvenanceRuntime
			}
		}
		hits = append(hits, fmt.Sprintf("[event:%s] %s turn=%s %s", prov, ev.Kind, ev.TurnID, attrsBlob(ev.Attrs)))
	}

	if len(hits) == 0 {
		return fmt.Sprintf("context_query: no hits for %q (turn=%s)", q, turnID)
	}
	return strings.Join(hits, "\n")
}

// RelevancyScore estimates 0..1 task relevance from recent turn activity.
// Used by AI-first state machine guards when no explicit graphHint is provided.
func (s *Store) RelevancyScore(turnID string) float64 {
	if s == nil {
		return 0.5
	}
	v, ok := s.Turn(turnID)
	if !ok || v.Stats == nil {
		evs := s.RecentEvents(32)
		if len(evs) == 0 {
			return 0.5
		}
		return clamp01(0.4 + float64(len(evs)%10)*0.05)
	}
	n := v.Stats.EventCount
	score := 0.45 + float64(n)*0.02
	if v.Stats.ByKind != nil {
		if v.Stats.ByKind[string(EventError)] > 0 {
			score -= 0.1
		}
		if v.Stats.ByKind[string(EventFulfilledLocal)] > 0 {
			score += 0.1
		}
		if v.Stats.ByKind[string(EventEpisodeMerged)] > 0 {
			score += 0.05
		}
	}
	return clamp01(score)
}

// RecordFact upserts into the entity/edge store and appends a FactRecorded event.
// Production path for hoop/swarm structural writes (not a stub index).
func (s *Store) RecordFact(turnID string, f Fact) {
	if s == nil {
		return
	}
	if f.At.IsZero() {
		f.At = time.Now().UTC()
	}
	if f.Provenance == "" {
		f.Provenance = ProvenanceInferred
	}
	if f.Kind == "" {
		f.Kind = KindEntity
	}
	if f.ID == "" {
		f.ID = fmt.Sprintf("fact-%d", f.At.UnixNano())
	}
	if turnID != "" {
		f.TurnID = turnID
	}
	kind := f.Kind
	if kind == "relation" {
		kind = KindEdge
	}
	ent := Entity{
		ID:         f.ID,
		Kind:       kind,
		Label:      f.Label,
		TurnID:     f.TurnID,
		Provenance: f.Provenance,
		From:       f.From,
		To:         f.To,
		Relation:   f.Relation,
		Attrs:      f.Attrs,
		At:         f.At,
	}
	if ent.Kind == KindEdge && ent.Relation == "" && f.Label != "" {
		ent.Relation = f.Label
	}

	s.mu.Lock()
	s.upsertEntityLocked(ent)
	s.mu.Unlock()

	attrs := map[string]string{
		"fact_id":    f.ID,
		"fact_kind":  f.Kind,
		"label":      f.Label,
		"provenance": string(f.Provenance),
		"layer":      "entity",
	}
	if f.From != "" {
		attrs["from"] = f.From
	}
	if f.To != "" {
		attrs["to"] = f.To
	}
	if f.Relation != "" {
		attrs["relation"] = f.Relation
	}
	for k, v := range f.Attrs {
		attrs[k] = v
	}
	s.Append(Event{
		Kind:   EventKind("FactRecorded"),
		TurnID: turnID,
		Actor:  "context",
		Attrs:  attrs,
		TS:     f.At,
	})
}

// PathSummary prefers entity edges (from→to), then falls back to event label scan.
func (s *Store) PathSummary(turnID, from, to string) string {
	if s == nil {
		return ""
	}
	fromL := strings.ToLower(strings.TrimSpace(from))
	toL := strings.ToLower(strings.TrimSpace(to))
	var steps []string

	ents := s.Entities(turnID, 500)
	// Direct edges whose endpoints match labels or ids.
	for _, e := range ents {
		if e.Kind != KindEdge {
			continue
		}
		blob := strings.ToLower(e.From + " " + e.To + " " + e.Relation + " " + e.Label + " " + attrsBlob(e.Attrs))
		fromHit := fromL == "" || strings.Contains(blob, fromL) || strings.EqualFold(e.From, from) || labelMatch(ents, e.From, fromL)
		toHit := toL == "" || strings.Contains(blob, toL) || strings.EqualFold(e.To, to) || labelMatch(ents, e.To, toL)
		if fromHit && toHit {
			rel := e.Relation
			if rel == "" {
				rel = e.Label
			}
			steps = append(steps, fmt.Sprintf("%s -[%s/%s]-> %s", e.From, rel, e.Provenance, e.To))
		}
	}
	if len(steps) > 0 {
		return strings.Join(steps, " | ")
	}

	v, ok := s.Turn(turnID)
	if !ok {
		if turnID == "" {
			return fmt.Sprintf("path: no link %s -> %s", from, to)
		}
		return "path: turn not found"
	}
	for _, ev := range v.Events {
		blob := strings.ToLower(attrsBlob(ev.Attrs) + " " + string(ev.Kind))
		if fromL != "" && strings.Contains(blob, fromL) {
			steps = append(steps, fmt.Sprintf("from~ %s", ev.Kind))
		}
		if toL != "" && strings.Contains(blob, toL) {
			steps = append(steps, fmt.Sprintf("to~ %s", ev.Kind))
		}
	}
	if len(steps) == 0 {
		return fmt.Sprintf("path: no link %s -> %s", from, to)
	}
	return strings.Join(steps, " | ")
}

func labelMatch(ents []Entity, id, wantLower string) bool {
	if wantLower == "" || id == "" {
		return false
	}
	for _, e := range ents {
		if e.ID == id && strings.Contains(strings.ToLower(e.Label), wantLower) {
			return true
		}
	}
	return false
}

func attrsBlob(m map[string]string) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	for k, v := range m {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte(' ')
	}
	return b.String()
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ContextQuerier adapts Store to tools.ContextStore.
type ContextQuerier struct{ Store *Store }

func (c ContextQuerier) Query(turnID, q string, limit int) string {
	if c.Store == nil {
		return ""
	}
	return c.Store.Query(turnID, q, limit)
}
