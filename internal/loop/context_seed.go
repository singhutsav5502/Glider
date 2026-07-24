package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/tools"
)

// stageContextBlock builds an explicit CONTEXT section so fan-out workers
// share the canonical clone path (never invent workspace-root audit-target).
func (m *Manager) stageContextBlock(st *LoopState, goal, plan string) string {
	if st == nil {
		return ""
	}
	clone := m.canonicalClonePath(st)
	toolPath := "audit-target"
	if clone != "" {
		if base := filepath.Base(filepath.FromSlash(clone)); base != "" && base != "." && base != "work" {
			toolPath = base
		}
	}
	var b strings.Builder
	b.WriteString("CONTEXT:\n")
	b.WriteString("[context_digest]\n")
	if clone != "" {
		b.WriteString("Clone verified: YES at ")
		b.WriteString(clone)
		b.WriteString("\n")
		b.WriteString("clone_path: ")
		b.WriteString(clone)
		b.WriteString("\n")
		b.WriteString("tool_path: ")
		b.WriteString(toolPath)
		b.WriteString("  (pass to fs_list/fs_read/code_grep — ScopeRel maps it under the run work dir)\n")
		b.WriteString("Do NOT re-clone. Do NOT invent absolute paths like C:\\...\\workspace\\audit-target.\n")
		b.WriteString("Prefer tool_path=")
		b.WriteString(toolPath)
		b.WriteString(" or clone_path above; context_query can recall clone_path.\n")
		b.WriteString("Ignore any prior plan/actor text claiming clone failed or git_clone was not allowed.\n")
	} else {
		b.WriteString("clone_path: (not recorded yet — verify clone before auditing)\n")
	}
	if g := strings.TrimSpace(goal); g != "" {
		b.WriteString("goal: ")
		b.WriteString(truncate(singleLine(g), 240))
		b.WriteString("\n")
	}
	// Omit contradictory clone-error plans when clone_path is already verified.
	if p := sanitizePlanText(plan); p != "" {
		if clone != "" && planContradictsClone(p) {
			// skip poisoned / contradictory plan line
		} else {
			b.WriteString("plan: ")
			b.WriteString(truncate(singleLine(p), 320))
			b.WriteString("\n")
		}
	}
	if digest := m.contextDigest(st); digest != "" {
		b.WriteString(digest)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// seedSharedContext upserts GOAL/PLAN/clone_path into contextgraph for the hoop turn.
// Used by kind: context and memory stages so later actors / fan-out share one engine.
func (m *Manager) seedSharedContext(st *LoopState, mod ModuleSpec, turnID, goal, plan, actor string) string {
	if m == nil || st == nil {
		return ""
	}
	hoopID := st.Spec.ID
	stageID := mod.ID
	if stageID == "" {
		stageID = string(mod.Kind)
	}
	// Prefer an already-recorded clone; else ScopeRel default for this hoop.
	if clone := m.canonicalClonePath(st); clone != "" {
		m.recordClonePath(st, turnID, clone, "")
	} else if m.Tools != nil && m.Tools.RunID() != "" {
		m.recordClonePath(st, turnID, m.Tools.ScopeRel("audit-target"), "audit-target")
	}

	if m.Graph != nil {
		m.Graph.RecordHoopContext(turnID, contextgraph.HoopKeyGoal, goal)
		// Prefer verify_clone / clone_path facts; skip planner narratives that only record
		// undeclared-tool rejections (e.g. git_clone not allowed in planner stage).
		// Overwrite any previously seeded poisoned plan so reseed clears CONTEXT.
		// Note: RecordHoopContext("") still Lookup's as label "plan" — always rewrite, never empty.
		cloneNow := m.canonicalClonePath(st)
		usable := sanitizePlanText(plan)
		existingPlan, hasExisting := m.Graph.LookupHoopContext(turnID, contextgraph.HoopKeyPlan)
		existingPoisoned := hasExisting && (existingPlan == "plan" || sanitizePlanText(existingPlan) == "" ||
			planLooksPoisoned(existingPlan) || (cloneNow != "" && planContradictsClone(existingPlan)))
		incomingBad := strings.TrimSpace(plan) != "" && (usable == "" || planLooksPoisoned(plan) ||
			(cloneNow != "" && planContradictsClone(usable)))
		switch {
		case usable != "" && !(cloneNow != "" && planContradictsClone(usable)):
			m.Graph.RecordHoopContext(turnID, contextgraph.HoopKeyPlan, usable)
			m.Graph.RecordEdge(turnID, "goal-follows-plan-"+hoopID,
				hoopContextEntityID(turnID, contextgraph.HoopKeyGoal),
				hoopContextEntityID(turnID, contextgraph.HoopKeyPlan),
				contextgraph.RelFollows, contextgraph.ProvenanceInferred, nil)
		case cloneNow != "" && (incomingBad || existingPoisoned):
			m.Graph.RecordHoopContext(turnID, contextgraph.HoopKeyPlan, "Clone result: OK at "+cloneNow)
		case incomingBad || existingPoisoned:
			m.Graph.RecordHoopContext(turnID, contextgraph.HoopKeyPlan, "(plan omitted — prior text was tool-rejection noise)")
		}
		if actor != "" {
			m.Graph.RecordFact(turnID, contextgraph.Fact{
				ID:         "hoop-actor-" + hoopID,
				Kind:       contextgraph.KindEpisode,
				Label:      "actor_excerpt",
				Provenance: contextgraph.ProvenanceRuntime,
				Attrs:      map[string]string{"stage": stageID, "text": truncate(actor, 2000)},
			})
			if strings.Contains(actor, "CLONE_OK") || strings.Contains(strings.ToLower(actor), "cloned to") {
				if clone := m.canonicalClonePath(st); clone != "" {
					m.Graph.RecordHoopContext(turnID, contextgraph.HoopKeyFileTree, truncate(singleLine(actor), 500))
					m.Graph.RecordFact(turnID, contextgraph.Fact{
						ID:         "hoop-tree-hint-" + hoopID,
						Kind:       contextgraph.KindNote,
						Label:      "tree_hint",
						Provenance: contextgraph.ProvenanceExtracted,
						Attrs:      map[string]string{"path": clone, "text": truncate(singleLine(actor), 500)},
					})
				}
			}
		}
		for i, art := range st.Artifacts {
			if i >= 12 {
				break
			}
			m.Graph.RecordFact(turnID, contextgraph.Fact{
				ID:         fmt.Sprintf("hoop-artifact-%s-%d", hoopID, i),
				Kind:       contextgraph.KindEntity,
				Label:      art,
				Provenance: contextgraph.ProvenanceExtracted,
				Attrs:      map[string]string{"kind": "artifact", "path": art},
			})
		}
		m.Graph.RecordFact(turnID, contextgraph.Fact{
			ID:         "hoop-context-seed-" + hoopID + "-" + stageID,
			Kind:       contextgraph.KindNote,
			Label:      "context_seed",
			Provenance: contextgraph.ProvenanceRuntime,
			Attrs: map[string]string{
				"stage": stageID, "kind": string(mod.Kind), "hoop": hoopID,
				"clone_path": m.canonicalClonePath(st),
			},
		})
	}
	return m.stageContextBlock(st, goal, plan)
}

// hoopContextEntityID mirrors contextgraph hoop-ctx ids for edges (same sanitization).
func hoopContextEntityID(turnID, key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	turn := strings.TrimSpace(turnID)
	if turn == "" {
		turn = "global"
	}
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

// recordClonePath upserts the shared workspace-relative clone root for this hoop turn.
func (m *Manager) recordClonePath(st *LoopState, turnID, rel, toolPath string) {
	if m == nil || st == nil {
		return
	}
	rel = normalizeCloneRel(rel)
	if rel == "" {
		return
	}
	if m.Tools != nil && !strings.HasPrefix(rel, "runs/") {
		rel = m.Tools.ScopeRel(rel)
	}
	if toolPath == "" {
		toolPath = filepath.Base(filepath.FromSlash(rel))
		if toolPath == "" || toolPath == "." || toolPath == "work" {
			toolPath = "audit-target"
		}
	}
	st.Artifacts = appendUniquePath(st.Artifacts, rel)
	if m.Graph == nil || turnID == "" {
		return
	}
	m.Graph.RecordHoopContext(turnID, contextgraph.HoopKeyClonePath, rel)
	m.Graph.RecordFact(turnID, contextgraph.Fact{
		ID:         "hoop-clone-" + st.Spec.ID,
		Kind:       contextgraph.KindDir,
		Label:      "clone_path",
		Provenance: contextgraph.ProvenanceExtracted,
		Attrs: map[string]string{
			"path":      rel,
			"tool_path": toolPath,
			"hoop":      st.Spec.ID,
			"key":       contextgraph.HoopKeyClonePath,
			"value":     rel,
		},
	})
}

func normalizeCloneRel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	if i := strings.Index(p, " ("); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"'`)
	return filepath.ToSlash(p)
}

// canonicalClonePath returns the best known workspace-relative clone root.
func (m *Manager) canonicalClonePath(st *LoopState) string {
	if st == nil {
		return ""
	}
	turnID := "loop:" + st.Spec.ID
	if m != nil && m.Graph != nil {
		if v, ok := m.Graph.LookupHoopContext(turnID, contextgraph.HoopKeyClonePath); ok {
			if p := normalizeCloneRel(v); p != "" {
				return p
			}
		}
		for _, e := range m.Graph.Entities(turnID, 64) {
			if e.Label == "clone_path" {
				if p := normalizeCloneRel(e.Attrs["path"]); p != "" {
					return p
				}
				if p := normalizeCloneRel(e.Label); p != "" && p != "clone_path" {
					return p
				}
			}
			if p := normalizeCloneRel(e.Attrs["path"]); strings.Contains(p, "/work/") &&
				(strings.HasSuffix(p, "audit-target") || strings.Contains(p, "audit-target")) {
				return p
			}
		}
	}
	for _, art := range st.Artifacts {
		art = normalizeCloneRel(art)
		if art == "" {
			continue
		}
		if strings.Contains(art, "audit-target") || strings.Contains(art, "/work/") {
			return art
		}
	}
	if m != nil && m.Tools != nil && m.Tools.RunID() != "" {
		return m.Tools.ScopeRel("audit-target")
	}
	return ""
}

// contextDigest returns HoopContextDigest + Query/PathSummary for prompt injection.
func (m *Manager) contextDigest(st *LoopState) string {
	if m == nil || m.Graph == nil || st == nil {
		return ""
	}
	turnID := "loop:" + st.Spec.ID
	var b strings.Builder
	clone := m.canonicalClonePath(st)
	if d := m.Graph.HoopContextDigest(turnID); d != "" {
		for _, line := range strings.Split(d, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Drop poisoned plan lines that slipped into the digest.
			if strings.HasPrefix(strings.ToLower(line), "plan:") {
				rest := strings.TrimSpace(line[len("plan:"):])
				if sanitizePlanText(rest) == "" || (clone != "" && planContradictsClone(rest)) {
					continue
				}
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	} else if clone != "" {
		b.WriteString("clone_path=")
		b.WriteString(clone)
		b.WriteString("\n")
	}
	if clone != "" && !strings.Contains(b.String(), "Clone verified:") {
		b.WriteString("Clone verified: YES at ")
		b.WriteString(clone)
		b.WriteString("\n")
	}
	q := m.Graph.Query(turnID, "clone_path OR context_seed OR plan OR goal OR tree_hint", 10)
	if strings.TrimSpace(q) != "" && !strings.Contains(strings.ToLower(q), "no hits") {
		if !planLooksPoisoned(q) {
			b.WriteString(truncate(q, 700))
			b.WriteString("\n")
		}
	}
	path := m.Graph.PathSummary(turnID, "goal", "fulfilled")
	if path == "" || strings.Contains(strings.ToLower(path), "no path") {
		path = m.Graph.PathSummary(turnID, "clone_path", "fulfilled")
	}
	if strings.TrimSpace(path) != "" && !strings.Contains(strings.ToLower(path), "no path") {
		b.WriteString(truncate(path, 300))
	}
	return strings.TrimSpace(b.String())
}

// planContradictsClone is true when plan text claims clone failure despite a recorded path.
func planContradictsClone(plan string) bool {
	lower := strings.ToLower(plan)
	return strings.Contains(lower, "not allowed") ||
		strings.Contains(lower, "clone_failed") ||
		strings.Contains(lower, "clone failed") ||
		strings.Contains(lower, "does not exist") ||
		(strings.Contains(lower, "error:") && strings.Contains(lower, "git_clone"))
}

// recordClonePathIfPresent records clone_path when the ScopeRel directory exists on disk.
func (m *Manager) recordClonePathIfPresent(st *LoopState, turnID, toolPath string) {
	if m == nil || m.Tools == nil || st == nil {
		return
	}
	if toolPath == "" {
		toolPath = "audit-target"
	}
	rel := m.Tools.ScopeRel(toolPath)
	abs := filepath.Join(m.Tools.Workspace(), filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return
	}
	// Non-empty tree preferred; empty dir still counts as present after git_clone refresh.
	m.recordClonePath(st, turnID, rel, toolPath)
	if m.Graph != nil && turnID != "" {
		m.Graph.RecordFact(turnID, contextgraph.Fact{
			ID:         "hoop-clone-verified-" + st.Spec.ID,
			Kind:       contextgraph.KindNote,
			Label:      "clone_verified",
			Provenance: contextgraph.ProvenanceExtracted,
			Attrs: map[string]string{
				"path": rel, "tool_path": toolPath, "verified": "YES",
			},
		})
	}
}

// indexToolGraphResults indexes file tree/symbols after successful git_clone / fs_list,
// and records the shared clone_path fact for later CONTEXT injection.
func (m *Manager) indexToolGraphResults(turnID string, results []tools.Result) {
	if m == nil {
		return
	}
	for _, tr := range results {
		if !tr.OK {
			continue
		}
		if tr.Name != "git_clone" && tr.Name != "fs_list" {
			continue
		}
		root := extractClonedPath(tr.Output)
		if root == "" {
			root = extractListedPath(tr.Output)
		}
		if root == "" && tr.Name == "fs_list" {
			continue
		}
		if root == "" {
			continue
		}
		root = normalizeCloneRel(root)
		abs := root
		if m.Tools != nil && !filepath.IsAbs(root) {
			abs = filepath.Join(m.Tools.Workspace(), filepath.FromSlash(root))
		}
		if m.Graph == nil {
			continue
		}
		if n, err := m.Graph.IndexFileTree(turnID, abs, 4, 200); err == nil && n > 0 {
			m.Graph.RecordHoopContext(turnID, contextgraph.HoopKeyFileTree,
				fmt.Sprintf("%s (%d nodes)", root, n))
			m.Graph.Append(contextgraph.Event{
				Kind:   contextgraph.EventKind("FileTreeIndexed"),
				TurnID: turnID,
				Actor:  "loop",
				Attrs: map[string]string{
					"root": root, "nodes": fmt.Sprintf("%d", n),
					"provenance": string(contextgraph.ProvenanceExtracted),
				},
			})
		}
		if n, err := m.Graph.IndexSymbols(turnID, abs, 100); err == nil && n > 0 {
			m.Graph.Append(contextgraph.Event{
				Kind:   contextgraph.EventKind("SymbolsIndexed"),
				TurnID: turnID,
				Actor:  "loop",
				Attrs: map[string]string{
					"root": root, "symbols": fmt.Sprintf("%d", n),
					"provenance": string(contextgraph.ProvenanceExtracted),
				},
			})
		}
	}
}

// recordClonePathsFromTools writes clone_path facts after successful git_clone / fs_list.
func (m *Manager) recordClonePathsFromTools(st *LoopState, turnID string, results []tools.Result) {
	if m == nil || st == nil {
		return
	}
	for _, tr := range results {
		if !tr.OK {
			continue
		}
		switch tr.Name {
		case "git_clone":
			if p := extractClonedPath(tr.Output); p != "" {
				m.recordClonePath(st, turnID, p, "")
			} else if p := parseArtifactPath(tr.Output); p != "" {
				m.recordClonePath(st, turnID, p, "")
			} else if m.Tools != nil {
				m.recordClonePathIfPresent(st, turnID, "audit-target")
			}
		case "fs_list":
			// fs_list success with a path= line or non-empty listing under audit-target → clone OK.
			if p := extractListedPath(tr.Output); p != "" {
				m.recordClonePath(st, turnID, p, "")
			} else if m.Tools != nil && strings.TrimSpace(tr.Output) != "" &&
				!strings.Contains(strings.ToLower(tr.Output), "does not exist") {
				m.recordClonePathIfPresent(st, turnID, "audit-target")
			}
		}
	}
}

// extractListedPath finds "path: <rel>" from fs_list output.
func extractListedPath(out string) string {
	out = strings.TrimSpace(out)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(line), "path:") {
			return normalizeCloneRel(strings.TrimSpace(line[len("path:"):]))
		}
		if strings.HasPrefix(strings.ToLower(line), "listed:") {
			return normalizeCloneRel(strings.TrimSpace(line[len("listed:"):]))
		}
	}
	return ""
}

// extractClonedPath finds "cloned to <path>" from git_clone output, or a bare path line.
func extractClonedPath(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	const mark = "cloned to "
	if i := strings.LastIndex(strings.ToLower(out), mark); i >= 0 {
		p := strings.TrimSpace(out[i+len(mark):])
		if j := strings.IndexAny(p, "\r\n"); j >= 0 {
			p = p[:j]
		}
		return strings.TrimSpace(p)
	}
	return ""
}
