# Orchestrator overnight plan (2026-07-19)

> Unattended run. Sources: [enterprise_orchestrator_mvp.md](./enterprise_orchestrator_mvp.md), [graph_feature_gaps.md](./graph_feature_gaps.md), [loop_swarm_gap_analysis.md](./loop_swarm_gap_analysis.md).  
> **Architectural core:** AI-first, relevancy-driven state machine (graphs | trees | loops | swarms).  
> No push. Checkpoint commits. Final: [remaining_gaps.md](./remaining_gaps.md).

---

## Priority order

1. State machine core (AI-first / relevancy)
2. HITL pause/resume + human_gate node
3. Live decision-route visualization
4. Conditional / policy edges + merge failure narrative
5. MCP / tools on nodes (interfaces + stubs)
6. Polish samples / docs / versioned snapshots / budget hooks

---

## Track 0 — AI-first relevancy-driven state machine (CORE)

- [ ] **SM0** Package `internal/statemachine`: `State`, `Transition`, `Guard`, `Action`, `Topology` (graph|tree|loop|swarm), `MachineDef`, `Runtime`, `DecisionRoute`
- [ ] **SM1** Guard kinds: always | score_below/above | heuristic | relevancy (AI-first) | policy | human_approved
- [ ] **SM2** `Engine.Next` — collect outgoing edges, cheap guards first, relevancy/AI scoring for ties; document decision points
- [ ] **SM3** Adapters: `FromLoopSpec` / `FromSwarmRoles` → `MachineDef`; topology detection from edges
- [ ] **SM4** Wire hoop `runCycle` hot path through SM engine (not linear-only when graph_edges present)
- [ ] **SM5** Wire swarm FanOut as TopologySwarm view (parallel states + merge)
- [ ] **SM6** Unit tests for guards, Next, adapters, path recording
- [ ] **SM7** Doc note in this plan + remaining_gaps: AI-first decision points

---

## Track 1 — HITL (human-in-the-loop)

- [ ] **H1** Status `waiting_human`; durable gate request on LoopState (comment, approve/reject)
- [ ] **H2** First-class stage kind `human_gate` (plus critic-fail auto-gate)
- [ ] **H3** Pause runner at gate; do not complete cycle until resume
- [ ] **H4** API: `POST /api/loops/{id}/approve` | `reject` | `resume` with optional comment
- [ ] **H5** Dashboard HITL panel: approve / reject / comment (ASCII-safe)
- [ ] **H6** Tests for pause + resume path

---

## Track 2 — Live decision route visualization

- [ ] **V1** Extend progress / DecisionRoute: current node, path taken, next edges, branch choices
- [ ] **V2** Emit agentlog `route_decision` + WS-friendly attrs per transition
- [ ] **V3** Cytoscape: paint path (ok), current (running), next candidates (highlight), waiting_human
- [ ] **V4** Swarm merge failure labels on graph nodes

---

## Track 3 — Conditional edges + graph P0s

- [ ] **C1** Edge kinds beyond flow/feedback: `on_fail`, `escalate`, `conditional`, `budget_exceeded`
- [ ] **C2** Normalize + YAML + dashboard edge kind select
- [ ] **C3** Version field / snapshot helper for graph audits (`graph_version` / snapshot JSON)
- [ ] **C4** Budget / rate-limit hooks on DecisionContext (consult orchestrator when present; stub OK)
- [ ] **C5** Merge failure narrative surfaced on swarm graph + CritiqueMerge already annotates

---

## Track 4 — MCP / tools on nodes

- [ ] **T1** `ToolRef` / tools field on StageSpec + Graph node data
- [ ] **T2** Package `internal/nodetools`: Plugin + MCPClient interfaces; stub backends
- [ ] **T3** Execution hook: stage can declare tools; invoke via registry before/after LLM (stub invoke logs)
- [ ] **T4** Sample hoop with tools stubs; catalog entry

---

## Track 5 — Deliverables discipline

- [ ] **D1** This plan with checkboxes (tick as shipped)
- [ ] **D2** Multiple checkpoint commits (PowerShell here-strings)
- [ ] **D3** `planning/remaining_gaps.md` honest leftover list
- [ ] **D4** Update graph_feature_gaps / gap_analysis rows where Done

---

## AI-first decision points (design)

| Point | Input | Behavior |
|-------|--------|----------|
| Outgoing edge choice | score, router signal, contextgraph relevancy, budget | Prefer edges whose Guard passes; among passers pick highest relevancy |
| Feedback vs escalate | critic SCORE, autonomy, human_gate | score_below → feedback or escalate edge if present |
| Swarm branch weight | worker role + merge critique | tree/swarm parallel; merge state aggregates |
| HITL resume | human Decision | Guard `human_approved` unlocks onward flow |
| Tool invoke | node ToolRefs | Stub MCP/plugin; log + optional no-op result into context |

Heuristics/policy are **fallback** when relevancy signal is absent (score = 0.5 default, prefer flow then feedback).

---

## Checkpoint commit cadence

1. After SM core + tests
2. After HITL API + runner
3. After live route viz + conditional edges
4. After nodetools + samples polish
5. Final: remaining_gaps + plan ticks
