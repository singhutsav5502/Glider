package contextgraph

import (
	"fmt"
	"sort"
	"strings"
)

// Community is a connected component (or degree-cluster) over entity edges.
// MVP stand-in for Leiden — sufficient for explain/path UX without heavy deps.
type Community struct {
	ID      string   `json:"id"`
	Members []string `json:"members"`
	Size    int      `json:"size"`
	Hubs    []string `json:"hubs,omitempty"` // highest-degree members
}

// DetectCommunities finds undirected connected components over KindEdge entities.
// Returns communities sorted by size descending; hubs = top degree within each.
func (s *Store) DetectCommunities(turnID string, maxCommunities int) []Community {
	if s == nil {
		return nil
	}
	if maxCommunities <= 0 {
		maxCommunities = 20
	}
	ents := s.Entities(turnID, 2000)
	adj := map[string]map[string]bool{}
	degree := map[string]int{}
	add := func(a, b string) {
		if a == "" || b == "" || a == b {
			return
		}
		if adj[a] == nil {
			adj[a] = map[string]bool{}
		}
		if adj[b] == nil {
			adj[b] = map[string]bool{}
		}
		if !adj[a][b] {
			adj[a][b] = true
			adj[b][a] = true
			degree[a]++
			degree[b]++
		}
	}
	nodes := map[string]bool{}
	for _, e := range ents {
		if e.Kind == KindEdge && e.From != "" && e.To != "" {
			add(e.From, e.To)
			nodes[e.From] = true
			nodes[e.To] = true
		} else if e.Kind != KindEdge && e.ID != "" {
			nodes[e.ID] = true
			if adj[e.ID] == nil {
				adj[e.ID] = map[string]bool{}
			}
		}
	}
	seen := map[string]bool{}
	var comps [][]string
	for id := range nodes {
		if seen[id] {
			continue
		}
		var stack []string
		var comp []string
		stack = append(stack, id)
		seen[id] = true
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			comp = append(comp, cur)
			for nb := range adj[cur] {
				if !seen[nb] {
					seen[nb] = true
					stack = append(stack, nb)
				}
			}
		}
		if len(comp) > 0 {
			comps = append(comps, comp)
		}
	}
	sort.Slice(comps, func(i, j int) bool { return len(comps[i]) > len(comps[j]) })
	var out []Community
	for i, comp := range comps {
		if i >= maxCommunities {
			break
		}
		if len(comp) < 2 {
			continue // skip isolates for community MVP
		}
		sort.Slice(comp, func(a, b int) bool {
			if degree[comp[a]] != degree[comp[b]] {
				return degree[comp[a]] > degree[comp[b]]
			}
			return comp[a] < comp[b]
		})
		hubs := comp
		if len(hubs) > 3 {
			hubs = hubs[:3]
		}
		cid := fmt.Sprintf("community-%d", i)
		members := append([]string{}, comp...)
		sort.Strings(members)
		out = append(out, Community{
			ID:      cid,
			Members: members,
			Size:    len(members),
			Hubs:    hubs,
		})
		// Persist as note entity for Query / explain.
		s.RecordFact(turnID, Fact{
			ID:         cid,
			Kind:       KindNote,
			Label:      fmt.Sprintf("community size=%d hubs=%s", len(members), strings.Join(hubs, ",")),
			Provenance: ProvenanceInferred,
			Attrs: map[string]string{
				"size":    fmt.Sprintf("%d", len(members)),
				"hubs":    strings.Join(hubs, ","),
				"members": strings.Join(members, ","),
			},
		})
	}
	return out
}

// GodNodes returns highest-degree entity ids (Graphify-style hub report).
func (s *Store) GodNodes(turnID string, limit int) []string {
	if s == nil {
		return nil
	}
	if limit <= 0 {
		limit = 8
	}
	ents := s.Entities(turnID, 2000)
	degree := map[string]int{}
	for _, e := range ents {
		if e.Kind != KindEdge {
			continue
		}
		if e.From != "" {
			degree[e.From]++
		}
		if e.To != "" {
			degree[e.To]++
		}
	}
	type scored struct {
		id string
		d  int
	}
	var list []scored
	for id, d := range degree {
		list = append(list, scored{id, d})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].d != list[j].d {
			return list[i].d > list[j].d
		}
		return list[i].id < list[j].id
	})
	var out []string
	for i, it := range list {
		if i >= limit {
			break
		}
		out = append(out, fmt.Sprintf("%s (degree=%d)", it.id, it.d))
	}
	return out
}

// Explain returns a human-readable Graphify-style narrative for an entity id/label.
func (s *Store) Explain(turnID, entityID string) string {
	if s == nil || strings.TrimSpace(entityID) == "" {
		return "explain: empty id"
	}
	entityID = strings.TrimSpace(entityID)
	ents := s.Entities(turnID, 800)
	var target *Entity
	want := strings.ToLower(entityID)
	for i := range ents {
		e := &ents[i]
		if e.ID == entityID || strings.EqualFold(e.ID, entityID) ||
			strings.EqualFold(e.Label, entityID) || strings.Contains(strings.ToLower(e.ID), want) {
			target = e
			break
		}
	}
	var parts []string
	if target == nil {
		parts = append(parts, fmt.Sprintf("explain: no exact entity for %q; showing neighborhood", entityID))
	} else {
		prov := target.Provenance
		if prov == "" {
			prov = ProvenanceInferred
		}
		parts = append(parts, fmt.Sprintf("explain: id=%s kind=%s label=%q provenance=%s",
			target.ID, target.Kind, target.Label, prov))
		if len(target.Attrs) > 0 {
			parts = append(parts, "attrs: "+attrsBlob(target.Attrs))
		}
		entityID = target.ID
	}
	neigh := s.Neighborhood(turnID, entityID, 12)
	if len(neigh) == 0 {
		parts = append(parts, "neighbors: (none)")
	} else {
		parts = append(parts, "neighbors:")
		parts = append(parts, neigh...)
	}
	// Path hints to hubs if any.
	hubs := s.GodNodes(turnID, 3)
	if len(hubs) > 0 {
		parts = append(parts, "hubs: "+strings.Join(hubs, "; "))
		if target != nil {
			for _, h := range hubs {
				hubID := strings.Split(h, " ")[0]
				if hubID == target.ID {
					continue
				}
				ps := s.PathSummary(turnID, target.ID, hubID)
				if ps != "" && !strings.Contains(ps, "no link") && !strings.Contains(ps, "not found") {
					parts = append(parts, "path_to_hub: "+truncateExplain(ps, 200))
					break
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func truncateExplain(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
