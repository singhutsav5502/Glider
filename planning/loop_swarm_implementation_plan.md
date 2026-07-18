# Loop + Swarm implementation plan

> Overnight **2026-07-19**. Ordered checklist. Gap source: [loop_swarm_gap_analysis.md](./loop_swarm_gap_analysis.md).  
> Living scorecard: [loop_swarm_gap_plan.md](./loop_swarm_gap_plan.md).

---

## P0 — MUST (graph UX)

- [ ] **G1** Graph undo/history stack for stage + swarm Cytoscape graphs (P2 polish)
- [ ] **G2** Custom modals — replace alert/confirm/prompt in dashboard graph/hoops flows (P2)

---

## P0 — Loop / swarm product gaps

- [x] **L1** Honor `stop_conditions.max_latency_ms`
- [x] **L2** Mid-cycle `progress` on `LoopState`
- [x] **L3** Persist `graph_edges` on `LoopSpec`
- [x] **L4** Stage preference bias in hoop learning
- [x] **S1** Enable `orchestration.fan_out` + `swarm` in default config
- [x] **S2** Sample Starlark rule `strategy: fan_out` (`fanout_dual_view.star`)
- [x] **S3** `CritiqueMerge` + loop parallel actors
- [x] **S4** Parallel actor fan-out inside hoop cycle
- [x] **T1** Unit tests for loop/swarm/agentlog pieces
- [x] **D1** Docs update (gap plan, samples, loop-engineering.html, planning README)

---

## P0 — Real-time agent logs (PER INSTANCE)

- [x] **R1** Separate log ring buffer keyed by hoop id and by swarm run id
- [x] **R2** Fresh independent timeline when a hoop starts or a swarm run begins
- [x] **R3** API `GET /api/agent-logs?scope=hoop|swarm&id=...`; WS `agent_log` events
- [x] **R4** UI: selecting a hoop/swarm shows that instance's logs
- [x] **R5** Emit stage start/end, eval, swarm worker events into the instance buffer

---

## How to try

See [loop_swarm_gap_plan.md](./loop_swarm_gap_plan.md) section 5.
