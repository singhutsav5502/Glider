# Loop + Swarm gap plan (living)

> Overnight session **2026-07-19**. Authority: code + tests; this file tracks expected → Glider → P0/P1 work.
> Ignore `planning/Depreceated/`. Canonical product framing: [loop_engineering.md](./loop_engineering.md), [swarm_orchestration.md](./swarm_orchestration.md).

---

## 1. Expected features (production-grade harness)

### Loop Engineering (Osmani / cobusgreyling)

| # | Feature | Why it matters |
|---|---------|----------------|
| L1 | Observe → plan → act → critique → learn cycle | Recursive goal, not one-shot prompt |
| L2 | Durable state outside chat | Model forgets; disk does not |
| L3 | Maker ≠ checker (separate critic + eval score) | Stops self-grading garbage |
| L4 | Stop: max iter, score, contains, success/fail N, latency | Bounded autonomy |
| L5 | Learning bias persistence (route / stage prefs) | Self-improving harness |
| L6 | Skills (persistent intent) | Pay down intent debt |
| L7 | Automations heartbeat (optional schedule) | Closes the outer loop |
| L8 | Human gate / autonomy L1–L3 | Safe defaults |
| L9 | Sub-agent role split | Explore / implement / verify |
| L10 | Live cycle observability | Operators must read the loop |
| L11 | Worktrees / isolation | Parallel agents without collide |
| L12 | Connectors (MCP) | Loop acts in real tools |

### Swarm / multi-agent

| # | Feature | Why it matters |
|---|---------|----------------|
| S1 | Bounded fan-out / fan-in | Parallel workers without unbounded goroutines |
| S2 | Role-tagged workers (plan/exec/research) | Orchestrator–worker pattern |
| S3 | Merge / weave with failure tolerance | Partial success usable |
| S4 | Critique of merged output | Quality gate after fan-in |
| S5 | Templates for common recipes | Repeatable swarm shapes |
| S6 | Graph topology drives runtime order | Editor edges → stage pipeline |
| S7 | Loop ↔ swarm bind (parallel actors in a cycle) | Swarm inside hoop, not only API |
| S8 | Live thread / worker status | Dashboard paints ok/fail |
| S9 | Gateway StrategyFanOut + sample rule | Productized, not stub-only |
| S10 | Slate-like thread weaving | Aspirational |

---

## 2. Gap scorecard (before overnight)

| ID | Status | Path / notes | Score |
|----|--------|--------------|-------|
| L1 | **Done** | `internal/loop/cycle.go` memory→router→planner→actor→critic→learn | 85% |
| L2 | **Done** | `~/.glider/loops/` + contextgraph + episodes | 80% |
| L3 | **Done** | Critic `SCORE:` + EvalMin | 85% |
| L4 | **Partial** | Stop N / contains / max_iter / human_gate; **MaxLatencyMS unused** | 70% |
| L5 | **Partial** | LocalBias only; **no stage preference** | 55% |
| L6 | **Partial** | `skill` string field; no SKILL.md load | 30% |
| L7 | **Done** | interval/cron Automations | 75% |
| L8 | **Done** | L1 default + human_gate | 70% |
| L9 | **Partial** | Stages; no explore/implement/verify worktrees | 50% |
| L10 | **Partial** | Dashboard polls outcomes; **no mid-cycle stage progress** | 45% |
| L11 | **Missing** | Document only | 0% |
| L12 | **Partial** | Path A MCP via Cursor; hoop has no MCP runtime | 20% |
| S1 | **Done** | `internal/swarm/fanout.go` max 4 + semaphore | 80% |
| S2 | **Done** | Roles on Worker + Runner | 75% |
| S3 | **Partial** | Text concat merge; weak failure narrative | 55% |
| S4 | **Missing** | No critique-after-merge | 0% |
| S5 | **Done** | TemplateStore YAML | 70% |
| S6 | **Partial** | Cytoscape → `#hoop-stages-json`; **edges not persisted on LoopSpec** | 50% |
| S7 | **Missing** | Loop actor is single Completer call | 0% |
| S8 | **Partial** | Swarm run paints workers; hoop stages mid-run not painted | 40% |
| S9 | **Partial** | FanOutExecutor exists; **flag-off, no sample rule** | 35% |
| S10 | **Missing** | Aspirational | 0% |

**How far behind (honest):** Loop Engineering MVP is real (~60–65% of production checklist). Swarm is foundation (~40%), not Slate parity. Biggest product gaps overnight: **S7/S9 (fan-out productized + in-loop), S4 (critique merge), L10/S6 (live progress + graph persist), L4/L5 polish.**

---

## 3. Priority

### P0 (ship this session)

1. Enable `orchestration.fan_out` + `swarm` in default config; sample Starlark rule for `strategy: fan_out`.
2. Parallel actor fan-out inside hoop cycle (`StageSpec.Parallel` / roles) via `swarm.FanOut` + merge.
3. Critique-aware merge (`CritiqueMerge`) used by swarm Runner + loop parallel actors.
4. Persist `graph_edges` on `LoopSpec`; dashboard round-trip.
5. Mid-cycle `progress` on `LoopState` (current stage); dashboard paints it.
6. Honor `stop_conditions.max_latency_ms`.
7. Stage preference bias in hoop learning (which stage kinds succeed → note / route nudge).
8. Tests for fan-out-in-loop, critique merge, latency stop, progress, graph edges normalize.
9. Docs: this plan + loop-engineering.html / samples + README backlog tweak.

### P1 (same session if time; else next)

1. Seed sample swarm template under `samples/hoops/` or hoops dir.
2. Dashboard: paint running stage node on graph during poll.
3. Fail-policy continue with consecutive fail tracking under latency.
4. Document how-to-try for fan_out + parallel actor hoop.

### P2 (defer)

- SKILL.md file load, worktrees, MCP-owned connectors, Path B multi-agent, Slate thread weaving, L3 denylist/budget.

---

## 4. Implementation log

| When | Change | Commit |
|------|--------|--------|
| 2026-07-19 pre | Cytoscape graph editor, C4 docs, sample hoops | `e045de5` |
| 2026-07-19 overnight | P0/P1: parallel actors, CritiqueMerge, fan_out on, graph_edges, progress, latency stop, stage prefs, agent logs, samples | _(see git log)_ |

---

## 5. Scorecard (after P0/P1 overnight)

| Area | Before | After |
|------|--------|-------|
| Loop Engineering overall | ~60% | **~75%** |
| Swarm productization | ~40% | **~65%** |
| Graph → runtime | ~50% | **~80%** (`graph_edges` persisted + order from flow) |
| Live observability | ~45% | **~75%** (progress + per-instance agent logs) |
| L4 MaxLatencyMS | unused | **Done** |
| L5 Stage prefs | missing | **Done** (`StagePrefs`) |
| S4 Critique merge | missing | **Done** |
| S7 Loop↔swarm bind | missing | **Done** (`Parallel` on actor) |
| S9 Sample FanOut rule | missing | **Done** (`fanout_dual_view.star`) |

### Remaining P2

- SKILL.md file load, worktrees, MCP-owned connectors
- Slate-like thread weaving / planner decomposition
- Path B multi-agent tool codec
- L3 denylist/budget gates
- Graph undo stack / custom modals (UX polish)
- Overview episode chip on dashboard

### How to try

```powershell
.\glider.exe --config configs\glider.yaml
# Dashboard http://127.0.0.1:8081 → Hoops / Graph editor / Swarm

go run ./scripts/loadhoop -file samples/hoops/parallel-actor.yaml -start
# Gateway fan-out: Path A prompt containing /swarm or [fanout]
# Agent logs: GET /api/agent-logs?scope=hoop&id=<hoop-id>
```
