package contextgraph

import (
	"fmt"
	"strings"
)

// QueryOpts filters dual-layer search (Graphify methodology: query / path / neighborhood / explain).
type QueryOpts struct {
	TurnID       string
	Keyword      string
	From         string     // path endpoint (label or id substring)
	To           string     // path endpoint
	Neighborhood string     // entity id — include 1-hop neighbors
	Explain      string     // entity id — Graphify-style explain narrative
	Communities  bool       // include community detection summary
	Provenance   Provenance // empty = any
	Limit        int
}

// QueryWith applies keyword + path + neighborhood + provenance filters.
func (s *Store) QueryWith(opts QueryOpts) string {
	if s == nil {
		return ""
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	q := strings.TrimSpace(strings.ToLower(opts.Keyword))
	turnID := strings.TrimSpace(opts.TurnID)
	wantProv := opts.Provenance
	var hits []string

	if eid := strings.TrimSpace(opts.Explain); eid != "" {
		hits = append(hits, s.Explain(turnID, eid))
		if len(hits) >= limit {
			return strings.Join(hits, "\n")
		}
	}

	if opts.Communities {
		coms := s.DetectCommunities(turnID, 8)
		if len(coms) == 0 {
			hits = append(hits, "[communities] none (need ≥2 linked entities)")
		} else {
			for _, c := range coms {
				hits = append(hits, fmt.Sprintf("[community] %s size=%d hubs=%s", c.ID, c.Size, strings.Join(c.Hubs, ",")))
				if len(hits) >= limit {
					break
				}
			}
			for _, g := range s.GodNodes(turnID, 5) {
				hits = append(hits, "[god_node] "+g)
				if len(hits) >= limit {
					break
				}
			}
		}
	}

	if opts.From != "" || opts.To != "" {
		path := s.PathSummary(turnID, opts.From, opts.To)
		if path != "" {
			hits = append(hits, "[path] "+path)
		}
	}

	if nid := strings.TrimSpace(opts.Neighborhood); nid != "" {
		for _, line := range s.Neighborhood(turnID, nid, limit) {
			hits = append(hits, line)
			if len(hits) >= limit {
				break
			}
		}
	}

	ents := s.Entities(turnID, 400)
	for i := len(ents) - 1; i >= 0 && len(hits) < limit; i-- {
		e := ents[i]
		prov := e.Provenance
		if prov == "" {
			prov = ProvenanceInferred
		}
		if wantProv != "" && prov != wantProv {
			continue
		}
		blob := strings.ToLower(e.Kind + " " + e.Label + " " + e.TurnID + " " + e.From + " " + e.To + " " + e.Relation + " " + attrsBlob(e.Attrs))
		if q != "" && !strings.Contains(blob, q) {
			continue
		}
		if opts.From != "" || opts.To != "" {
			if e.Kind != KindEdge {
				continue
			}
			fromL := strings.ToLower(opts.From)
			toL := strings.ToLower(opts.To)
			fromHit := fromL == "" || strings.Contains(blob, fromL) || strings.EqualFold(e.From, opts.From)
			toHit := toL == "" || strings.Contains(blob, toL) || strings.EqualFold(e.To, opts.To)
			if !fromHit || !toHit {
				continue
			}
		}
		hits = append(hits, fmt.Sprintf("[entity:%s] %s id=%s kind=%s turn=%s %s",
			prov, e.Label, e.ID, e.Kind, e.TurnID, attrsBlob(e.Attrs)))
	}

	if wantProv == "" || wantProv == ProvenanceRuntime || wantProv == ProvenanceInferred {
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
			if wantProv != "" && prov != wantProv {
				continue
			}
			hits = append(hits, fmt.Sprintf("[event:%s] %s turn=%s %s", prov, ev.Kind, ev.TurnID, attrsBlob(ev.Attrs)))
		}
	}

	if len(hits) == 0 {
		return fmt.Sprintf("context_query: no hits for %q (turn=%s prov=%s)", q, turnID, wantProv)
	}
	return strings.Join(hits, "\n")
}

// Neighborhood returns 1-hop edge descriptions around entityID.
func (s *Store) Neighborhood(turnID, entityID string, limit int) []string {
	if s == nil || entityID == "" {
		return nil
	}
	if limit <= 0 {
		limit = 16
	}
	ents := s.Entities(turnID, 500)
	want := strings.ToLower(entityID)
	var out []string
	for _, e := range ents {
		if e.Kind != KindEdge {
			continue
		}
		fromL := strings.ToLower(e.From)
		toL := strings.ToLower(e.To)
		if e.From != entityID && e.To != entityID &&
			!strings.EqualFold(e.From, entityID) && !strings.EqualFold(e.To, entityID) &&
			!strings.Contains(fromL, want) && !strings.Contains(toL, want) {
			continue
		}
		rel := e.Relation
		if rel == "" {
			rel = e.Label
		}
		out = append(out, fmt.Sprintf("[neigh:%s] %s -[%s]-> %s", e.Provenance, e.From, rel, e.To))
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ParseQueryInput parses context_query tool input:
//
//	turn_id [prov=RUNTIME] [path=from->to] [neigh=id] keyword...
func ParseQueryInput(input string) QueryOpts {
	input = strings.TrimSpace(input)
	opts := QueryOpts{Limit: 20}
	if input == "" {
		return opts
	}
	parts := strings.Fields(input)
	i := 0
	if len(parts) > 0 && !strings.Contains(parts[0], "=") {
		opts.TurnID = parts[0]
		i = 1
	}
	var kw []string
	for ; i < len(parts); i++ {
		p := parts[i]
		eq := strings.IndexByte(p, '=')
		if eq <= 0 {
			kw = append(kw, p)
			continue
		}
		key := strings.ToLower(p[:eq])
		val := p[eq+1:]
		switch key {
		case "prov", "provenance":
			opts.Provenance = Provenance(strings.ToUpper(val))
		case "path":
			from, to, ok := strings.Cut(val, "->")
			if ok {
				opts.From = strings.TrimSpace(from)
				opts.To = strings.TrimSpace(to)
			} else {
				opts.From = strings.TrimSpace(val)
			}
		case "neigh", "neighborhood":
			opts.Neighborhood = val
		case "explain":
			opts.Explain = val
		case "communities", "community":
			opts.Communities = val == "1" || strings.EqualFold(val, "true") || val == ""
		case "limit":
			var n int
			fmt.Sscanf(val, "%d", &n)
			if n > 0 {
				opts.Limit = n
			}
		default:
			kw = append(kw, p)
		}
	}
	opts.Keyword = strings.Join(kw, " ")
	if opts.Keyword == "" && opts.TurnID != "" && opts.From == "" && opts.Neighborhood == "" && opts.Provenance == "" && opts.Explain == "" && !opts.Communities {
		opts.Keyword = opts.TurnID
		opts.TurnID = ""
	}
	return opts
}

// QueryRaw parses filter syntax from a single input string.
func (c ContextQuerier) QueryRaw(input string) string {
	if c.Store == nil {
		return ""
	}
	return c.Store.QueryWith(ParseQueryInput(input))
}
