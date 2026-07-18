package contextgraph

import (
	"fmt"
	"strings"
	"time"
)

// Provenance tags an edge/fact as extracted from source vs inferred by Glider.
// Inspired by Graphify's EXTRACTED / INFERRED edge labels (see planning/graphify_context_notes.md).
type Provenance string

const (
	ProvenanceExtracted Provenance = "EXTRACTED"
	ProvenanceInferred  Provenance = "INFERRED"
)

// Fact is a lightweight queryable node/edge annotation on the event log.
type Fact struct {
	ID         string            `json:"id"`
	Kind       string            `json:"kind"` // entity|relation|note
	Label      string            `json:"label"`
	TurnID     string            `json:"turn_id,omitempty"`
	Provenance Provenance        `json:"provenance,omitempty"`
	Attrs      map[string]string `json:"attrs,omitempty"`
	At         time.Time         `json:"at,omitempty"`
}

// Query searches recent events (and optional turn) for a substring match.
// Returns a human-readable summary for agent tools / relevancy hints.
// Graphify inspiration: prefer structured lookup over re-reading entire logs.
func (s *Store) Query(turnID, q string, limit int) string {
	if s == nil {
		return ""
	}
	if limit <= 0 {
		limit = 20
	}
	q = strings.TrimSpace(strings.ToLower(q))
	var events []Event
	if turnID != "" {
		if v, ok := s.Turn(turnID); ok {
			events = v.Events
		}
	} else {
		events = s.RecentEvents(200)
	}
	var hits []string
	for i := len(events) - 1; i >= 0 && len(hits) < limit; i-- {
		ev := events[i]
		blob := strings.ToLower(string(ev.Kind) + " " + ev.TurnID + " " + ev.Actor + " " + attrsBlob(ev.Attrs))
		if q != "" && !strings.Contains(blob, q) {
			continue
		}
		prov := ProvenanceExtracted
		if ev.Attrs != nil && ev.Attrs["provenance"] == string(ProvenanceInferred) {
			prov = ProvenanceInferred
		}
		hits = append(hits, fmt.Sprintf("[%s] %s turn=%s %s", prov, ev.Kind, ev.TurnID, attrsBlob(ev.Attrs)))
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

// RecordFact appends a Fact as an event with provenance attrs (Graphify-style).
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
	attrs := map[string]string{
		"fact_id":    f.ID,
		"fact_kind":  f.Kind,
		"label":      f.Label,
		"provenance": string(f.Provenance),
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

// PathSummary lists events linking from→to labels within a turn (lightweight path).
func (s *Store) PathSummary(turnID, from, to string) string {
	if s == nil {
		return ""
	}
	v, ok := s.Turn(turnID)
	if !ok {
		return "path: turn not found"
	}
	from = strings.ToLower(from)
	to = strings.ToLower(to)
	var steps []string
	for _, ev := range v.Events {
		blob := strings.ToLower(attrsBlob(ev.Attrs) + " " + string(ev.Kind))
		if from != "" && strings.Contains(blob, from) {
			steps = append(steps, fmt.Sprintf("from~ %s", ev.Kind))
		}
		if to != "" && strings.Contains(blob, to) {
			steps = append(steps, fmt.Sprintf("to~ %s", ev.Kind))
		}
	}
	if len(steps) == 0 {
		return fmt.Sprintf("path: no link %s -> %s", from, to)
	}
	return strings.Join(steps, " | ")
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
