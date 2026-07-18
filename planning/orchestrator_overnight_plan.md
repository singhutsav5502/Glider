# Orchestrator overnight plan (2026-07-19)

> Unattended run. Sources: enterprise MVP, graph gaps, loop/swarm analysis, [graphify_context_notes.md](./graphify_context_notes.md), [tools_catalog.md](./tools_catalog.md).  
> **Core:** AI-first relevancy SM + unified tools + shared contextgraph + MCP/plugin interfaces.  
> Final: [remaining_gaps.md](./remaining_gaps.md). No push.

---

## Priority order

1. Tool registry + standard AI tools — **done**
2. Shared context for hoop/swarm — **done (MVP)**
3. MCP plumbing + GitHub MCP interfaces — **done (stub transport)**
4. Graphify-informed context — **done (notes + Query/provenance)**
5. Clone + audit sample — **done**
6. HITL / live route / SM — **done (MVP)**
7. remaining_gaps.md — **done**

---

## Track checkboxes

### State machine
- [x] SM0–SM7 internal/statemachine + hoop wire + tests

### HITL
- [x] H1–H5 waiting_human, API, dashboard
- [x] H6 tests

### Live route viz
- [x] V1–V3 DecisionRoute + Cytoscape paint
- [ ] V4 Swarm merge failure labels (partial — see remaining_gaps)

### Conditional edges
- [x] C1–C4 kinds, version, budget hook
- [ ] C5 swarm graph failure labels

### Tools / MCP / plugins
- [x] T3 standard builtins in `internal/tools`
- [x] T4 MCP + plugin interface packages
- [x] T5 GitHub MCP config + stub catalog
- [x] T6 tools_catalog.md

### Shared context
- [x] CX1–CX4 Query, RelevancyScore, swarm shared snippet, provenance

### Graphify
- [x] G1 notes
- [x] G2 Query/Path/Fact provenance

### Samples
- [x] A1 clone-repo-security-audit + repo-audit-swarm
- [x] A2 README + samples.html

### UX / docs design
- [x] HITL + log focus visibility CSS
- [x] docs/site token polish
- [ ] Full tools panel in stage editor (gap)

### Deliverables
- [x] Plan checkboxes
- [x] Checkpoint commits
- [x] remaining_gaps.md
