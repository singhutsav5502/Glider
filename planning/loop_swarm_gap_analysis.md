# Loop + Swarm gap analysis

> Overnight session **2026-07-19**. Authority: code under `internal/loop`, `internal/swarm`, dashboard graph.  
> Product framing: [loop_engineering.md](./loop_engineering.md), [swarm_orchestration.md](./swarm_orchestration.md).  
> Living combined notes (may lag): [loop_swarm_gap_plan.md](./loop_swarm_gap_plan.md).  
> Implementation checklist: [loop_swarm_implementation_plan.md](./loop_swarm_implementation_plan.md).

---

## Expected | Current | Priority | Notes

### Loop Engineering

| Expected | Current | Priority | Notes |
|----------|---------|----------|-------|
| Observe → plan → act → critique → learn cycle | **Done** — `runCycle` memory→router→planner→actor→critic→`ApplyHoopLearning` | P2 polish | Real MVP; learn is bias-only |
| Durable state outside chat | **Done** — `~/.glider/loops/`, contextgraph events, episode store | P2 | Checkpoint fields thin |
| Maker ≠ checker + eval score | **Done** — Critic `SCORE:` + EvalMin | P1 | Parse is regex-tolerant |
| Stop: max iter, score, contains, success/fail N, latency | **Partial** — max/contains/N work; **MaxLatencyMS unused** in `evaluateStop` | **P0** | Wire latency into fail / OnFailN |
| Learning bias (route + stage prefs) | **Partial** — `LocalBias` only; note mentions stage pref but no map | **P0** | Add `StageBias` / preferred kinds |
| Skills (SKILL.md / persistent intent) | **Partial** — `skill` string on spec/stage | P2 | File load deferred |
| Automations heartbeat | **Done** — interval/cron around cycles | P2 | Optional by design |
| Human gate / autonomy L1–L3 | **Done** — L1 + human_gate on critic fail | P1 | L3 denylist/budget deferred |
| Sub-agent role split | **Partial** — stage kinds; no worktrees | P2 | Worktrees out of scope overnight |
| Live cycle observability | **Partial** — outcome cards; **no mid-cycle Progress** on `LoopState` | **P0** | Paint running stage on graph |
| Worktrees / isolation | **Missing** | P2 | Document only |
| Connectors (MCP) for hoop | **Partial** — Path A via Cursor only | P2 | No hoop-owned MCP runtime |

### Swarm / multi-agent

| Expected | Current | Priority | Notes |
|----------|---------|----------|-------|
| Bounded fan-out / fan-in | **Done** — FanOut max 4 + semaphore | P1 | Solid foundation |
| Role-tagged workers | **Done** — plan/exec/research on Worker + Runner | P1 | Dashboard roles input |
| Merge / weave with failure narrative | **Partial** — concat summaries; skips errors quietly | **P0** | Stronger merge + fail list |
| Critique of merged output | **Missing** | **P0** | `CritiqueMerge` helper + optional critic pass |
| Templates | **Done** — TemplateStore YAML | P1 | Seed sample templates |
| Graph topology → runtime order | **Partial** — Cytoscape stages → JSON; **edges not on LoopSpec** | **P0** | Persist `graph_edges` |
| Loop ↔ swarm bind (parallel actors) | **Missing** — actor is single Completer | **P0** | `StageSpec.Parallel` + FanOut |
| Live thread status on graph | **Partial** — swarm run paints; hoop mid-stage not | **P0** | Progress + poll paint |
| Gateway StrategyFanOut + sample rule | **Partial** — FanOutExecutor exists; **flag-off, no sample** | **P0** | Enable config + Starlark sample |
| Slate-like thread weaving | **Missing** | P2 | Aspirational; do not fake |

### Graph UX (dashboard)

| Expected | Current | Priority | Notes |
|----------|---------|----------|-------|
| Undo / redo history stack | **Missing** | **P0 MUST** | Stage + swarm graphs; Ctrl+Z/Y |
| Custom modals (no browser prompt/alert) | **Partial** — stage edit dialog exists; swarm + fallback still use `window.prompt` | **P0 MUST** | Generic confirm/prompt dialogs |
| Keep Cytoscape + edgehandles | **Done** | — | Enhance, do not rip out |
| ASCII-safe UI strings | **Mostly** | P1 | Avoid mojibake |

---

## Honest scores (pre-overnight)

| Area | Score | Verdict |
|------|-------|---------|
| Loop Engineering overall | ~60–65% | Usable MVP; latency/progress/learning gaps |
| Swarm productization | ~40% | Foundation stubs; not in-loop or flag-on |
| Graph → runtime | ~50% | Editor strong; edges/progress weak |
| Graph UX (undo/modals) | ~35% | Edit dialog only; no history |

---

## Overnight target (after P0/P1)

| Area | After |
|------|-------|
| Loop Engineering | ~75% |
| Swarm productization | ~65% |
| Graph → runtime | ~80% |
| Graph UX | ~90% (undo + modals) |
