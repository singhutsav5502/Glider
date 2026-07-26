# Loop + Swarm implementation plan

> Overnight **2026-07-19**. Gap source: [loop_swarm_gap_analysis.md](./loop_swarm_gap_analysis.md).  
> Living scorecard: [loop_swarm_gap_plan.md](./loop_swarm_gap_plan.md). Do not push.

---

## P0 — Graph UX

- [x] **G1** Graph undo/history stack for stage + swarm (Undo/Redo + Ctrl+Z/Y)
- [x] **G2** Custom modals — `glider-prompt` / `glider-confirm`; no `window.prompt` in graph/hoops flows

---

## P0 — Loop / swarm product gaps

- [x] **L1** Honor `stop_conditions.max_latency_ms`
- [x] **L2** Mid-cycle `progress` on `LoopState`
- [x] **L3** Persist `graph_edges` on `LoopSpec`
- [x] **L4** Stage preference bias in hoop learning
- [x] **S1** Enable `orchestration.fan_out` + `swarm` in default config
- [x] **S2** Sample Starlark rules (`fanout_dual_view.star` / `fan_out_sample.star`)
- [x] **S3** `CritiqueMerge` + loop parallel actors
- [x] **S4** Parallel actor fan-out inside hoop cycle
- [x] **T1** Unit tests (agentlog, critique merge, latency, graph edges, stage prefs)
- [x] **D1** Docs: gap plan, samples, loop-engineering.html, planning README

---

## P0 — Real-time agent logs (PER INSTANCE)

- [x] **R1** Separate ring buffers keyed by hoop id / swarm run id (`internal/agentlog`)
- [x] **R2** Fresh timeline on hoop Start / swarm Run (`Reset`)
- [x] **R3** `GET /api/agent-logs?scope=&id=`; WS `type=agent_log` with scope + instance_id
- [x] **R4** UI: selecting hoop / swarm run shows that instance only
- [x] **R5** Stage start/end, route, eval, worker events, errors emitted

---

## Remaining P2

- SKILL.md load, worktrees, MCP-owned connectors
- Slate-like thread weaving / planner decomposition
- Path B multi-agent tool codec
- L3 denylist/budget gates
- Overview episode chip on dashboard

## How to try

See [loop_swarm_gap_plan.md](./loop_swarm_gap_plan.md) section 5.
