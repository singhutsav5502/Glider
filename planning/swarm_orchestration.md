# Swarm & orchestration engine

> Honest status **2026-07-18**. Index: [README.md](./README.md).  
> Context graph: [context_management.md](./context_management.md).  
> Routing: [smart_routing_and_local_tools.md](./smart_routing_and_local_tools.md), [routing_session_policy.md](./routing_session_policy.md).  
> Canvas (visual companion): `glider_orchestration_roadmap.canvas.tsx` (may lag this file).
>
> Slate / Random Labs: https://randomlabs.ai/ · https://randomlabs.ai/blog/slate

---

## 1. How far are we? (honest %)

| Capability | % done | What exists |
|------------|--------|-------------|
| **Single-request routing** | **~90%** | Explicit, sticky (Path B), classifier + roles, Starlark, ceiling, shared pipeline |
| **Orchestration engine (1:1)** | **~75%** | Lifecycle, queue, fallback, breaker, rate/budget, aliases, VRAM |
| **Local/cloud + Path B** | **~60%** | Text fulfill + sticky/contextgraph; tools origin + would_* |
| **Local tools (Path A)** | **~90%** | Tools fields + stream tool_calls SSE; no Glider-side runners |
| **Context management** | **~55%** | `contextgraph` MVP; `contextkit` Episode stubs not wired to every fulfill |
| **Hot-swap modules** | **~65%** | Registry + fan_out Apply; router/aliases/threshold/log/GPU hot; backends/MITM restart |
| **Multi-agent / swarms** | **~40%** | `internal/swarm` FanOut+Merge+Loop+HotSwap; FanOutExecutor; no planner / default rules |
| **Loop engineering** | **~55%** | Hoops = planner/actor/critic cycles + eval score + hoop learning; Automations optional — see [loop_engineering.md](./loop_engineering.md) |
| **Slate-like thread weaving** | **~0%** | Aspirational |

**Bottom line:** Routing + Path A tools + Path B text/sticky are usable. Swarms are **foundation stubs**, not Slate parity.

---

## 2. Slate takeaways (keep short)

| Slate idea | Glider fit |
|------------|------------|
| Orchestrator + worker threads | Future planner → short `SubTask` jobs |
| Episodes | `contextkit` / MergeResults stubs |
| Role routing | Classifier `plan` / `exec` / `research` (MVP done) |
| Dumb-zone avoidance | Ceiling = safety net; classifier primary |

**Do not copy:** TypeScript DSL runtime, swarm CLI UX, hive-mind claims.

---

## 3. Gap vs target

```
Glider today:     request → route → single Execute → stream
Slate-like target: request → orchestrator → N threads → episodes → weave → stream
```

| Gap | Effort | Notes |
|-----|--------|-------|
| Wire Episode on fulfill | M | P1 |
| Fan-out default rules | M | Flag-off today |
| Planner decomposition | L | After Path A tools in user workflow |
| Path B multi-agent | XL | Blocked on tool codec |
| DSL action space | L | Product decision |

---

## 4. Hot-swap & concurrency (merged)

`internal/config/watcher.go`: `Watch` / `Swap` rebuild router, aliases, threshold, slog, GPU (+ swarm Registry Apply). **Restart** for ports, MITM, backends, cloud providers.

Rules: immutable decision snapshot at `Route()`; backend live-swap only after in-flight drain; do not hot-swap MITM listeners without drain. Fan-out: `BatchReserve` all-or-nothing; never unbounded workers.

| Loop type | Glider hook | Status |
|-----------|-------------|--------|
| Recurring Automations heartbeat | Optional `interval`/`cron` **around** hoop cycles | MVP |
| Babysit / CI wake | Wake → local fix hoop | Not built |
| Swarm wave | FanOut + Episode weave | Stub |
| **Loop Engineering cycle** | Planner→Actor→Critic + memory/router + eval | **MVP** — [loop_engineering.md](./loop_engineering.md) |

Checkpoint sketch: `{ goal, last_episode_id, eval_status, wake_reason, next_delay_s }`.

---

## 5. Swarm package API (quick)

```go
swarm.FanOut(ctx, workers, opts)
swarm.MergeResults(results)
swarm.DefaultSwarm{Opts}.Run(ctx, workers)
swarm.IntervalLoop{}
swarm.NewRegistry().BindProvider(p)
swarm.OptionsFromConfig(cfg.Orchestration)
```

---

## 6. Next steps (aligned with [README.md](./README.md))

| Pri | Work | Done when |
|-----|------|-----------|
| **P0** | Manual Cursor verify Path B text + `/cloud` sticky | Checklist green |
| **P1** | Wire Episode → Overview/history | API shows episodes after fulfill |
| **P1** | Sample FanOut rule + e2e (gateway, 2 workers) | Flag-on demo |
| **P2** | SessionState + turn budgets | Dashboard gauges |
| **P2** | Loop Engineering hoop polish (skills load, worktrees, L3 gates) | See [loop_engineering.md](./loop_engineering.md) |
| **P2** | Path B tools | Only if Path A insufficient |

---

## 7. Code anchors

| Piece | Path |
|-------|------|
| Explicit / classifier / tool_followup | `internal/router/` |
| Path A tool_calls bridge | `internal/backend/stream.go`, `internal/api/streaming.go` |
| Context graph / stubs | `internal/contextgraph`, `internal/contextkit` |
| FanOut / swarm | `internal/orchestrator/fanout.go`, `internal/swarm` |
| Path B hub | `internal/mitm/agent_fulfill_hub.go`, `intercept.go` |
| Config Watch/Swap | `internal/config/watcher.go` |
| V2 notes | `planning/implementation_plan.md` §9 |

---

## 8. Operator retest `/cloud`

1. Restart Glider with `agent_rpc_fulfill: true`.
2. Agent: `/cloud say hello` → origin prose, **not** canned/Ollama interrupt.
3. Follow-on summary / composer wrap-up → sticky cloud (`bidi_sticky_cloud` / `_summary`).
4. Next user msg without flag → re-decide.
