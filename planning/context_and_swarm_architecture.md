# Context management & swarm architecture

> Honest status **2026-07-18**. Canvas companion (open beside chat):
> `C:\Users\Utsav\.cursor\projects\d-repos-Glider\canvases\glider-orchestration-roadmap.canvas.tsx`
> Also: [swarm_orchestration.md](./swarm_orchestration.md),
> [context_management.md](./context_management.md),
> [smart_routing_and_local_tools.md](./smart_routing_and_local_tools.md).
>
> Slate / Random Labs: https://randomlabs.ai/ · https://docs.randomlabs.ai/

---

## 1. Capability maturity (code truth)

| Area | % | What exists | Gap |
|------|---|-------------|-----|
| Routing | **80%** | Explicit hard-force, task_classifier MVP, Starlark, token ceiling | ML classifier; dashboard classifier editor |
| Path A gateway | **85%** | `cus-` + Anthropic normalize + shared pipeline | Stream `tool_calls` → Cursor SSE |
| Path B MITM | **35%** | BidiAppend extract → RunSSE text fulfill (experimental) | Child/tool RunSSE still origin |
| Local tools | **40%** | `Tools`/`ToolChoice` on request → Ollama/vLLM/OpenAI | Bridge + Path B tools |
| Orchestration 1:1 | **75%** | Queue, fallback, breaker, rate/budget | — |
| Swarms | **5%** | `Strategy` / `SubTask` / Executor interface stubs | No `SwarmExecutor`, episodes, fan-out |
| Context management | **55%** | `contextgraph` event log + turn index + sticky graph correlation; `contextkit` Episode stubs | Blackboard / sqlite cold store; swarm episode merge |
| Hot-swap modules | **55%** | `Provider.Watch`/`Swap` for router/aliases/threshold/log/GPU | Backends/MITM/ports need restart |
| Concurrency (swarm-ready) | **45%** | Cancel + queue solid; RunSSE hub races managed | Fan-out backpressure unbuilt |
| Loop engineering | **0%** | Impl plan §9.2 notes only | Eval loops, babysit wakes |

**Shipped and not oversold:** `/cloud` P0 TipTap hard-force is done. Path B text fulfill is experimental. Swarms are stubs.

---

## 2. Context management design

Glider is a **proxy/harness**, not a coding agent product. Context work is about keeping routing + local execution out of the “dumb zone” (Slate’s term for context-bloated degradation) and supporting recurring loops.

### 2.1 Layers

| Layer | Purpose | Today | Target |
|-------|---------|-------|--------|
| Turn budget | Cap tokens/cost per Cursor turn | Orchestrator rate/budget; `max_local_context_tokens` | Soft+hard per-session gauges on Overview |
| Session memory | Carry decisions across turns | `~/.glider/history` JSONL | `SessionState`: overrides, last decision, episode ring, spend |
| Episode (swarm) | Compressed worker return | — | `Episode{Summary, Artifacts, Tokens, Model}` |
| Shared swarm state | Avoid peer message-pass | — | Hub scratchpad keyed by `corr_id` / `swarm_id` |
| Loop checkpoint | Resume after `/loop` or CI wake | — | `{ goal, last_episode_id, eval_status, wake_reason, next_delay_s }` |
| Dumb-zone guard | Small working memory | Ceiling as safety net | Episode-first; compaction last resort |

### 2.2 Session memory sketch

```text
SessionState
  session_id
  active_overrides          // last /local|/cloud|/fast|/heavy
  last_decision             // RoutingDecision snapshot
  episodes[]                // ring buffer, N≈32
  spend_tokens / spend_cost
  loop_checkpoint?          // optional pointer for recurring work
```

Key by Cursor request UUID / composer id when extractable; else Glider history session id.

### 2.3 Swarm shared state

- Hub owns scratchpad; workers never peer-message (Slate thread-weaving adapted to Go fan-in).
- Workers return **Episode only**, not full transcripts.
- Parent `ctx` cancel → cancel all workers.
- Partial failure → degrade to single-model or `ErrOriginPassthrough`.

---

## 3. Hot-swap modular architecture

### 3.1 What Swap already does

`internal/config/watcher.go`:

- `Watch(cb)` — subscribers on successful file reload
- `Swap(cfg)` — atomic `value.Store` + notify (same path as dashboard `PUT /api/config`)

Hot today: router rules, aliases, context threshold, slog level, GPU assignments.

Restart required: listen ports, MITM enable/port/CA/hosts, backend URLs, cloud provider registration.

### 3.2 Module boundaries (target)

```text
ConfigProvider  →  RouterEngine  →  Executor  →  Backend
     │                  │              │
   Watch/Swap      snapshot at      single |
                   Route() start    FanOut | SwarmExecutor (V2)
                                   Pipeline|
```

Rules:

1. **Immutable decision snapshot** at `Route()` — mid-flight Swap must not mutate an in-flight request.
2. **Providers / routers / agents as swappable modules** behind interfaces (already true for Executor + Backend).
3. **Backend live swap** only when in-flight `Complete` drains on the old client (generation counter).
4. **Do not** hot-swap MITM listeners without a drain protocol.

### 3.3 Concurrency contract for Swap

| Event | Required behavior |
|-------|-------------------|
| File reload / dashboard save | `Swap` → rebuild Engine; in-flight keep old snapshot |
| Fan-out spawn | `BatchReserve` all-or-nothing before workers |
| `/cloud` vs local | Hard-force + Path B `ArmOrigin` before any local fulfill |
| RunSSE hub | UUID key; 800ms wait; 30s pending TTL; metric on miss/expire |

---

## 4. Concurrency model (detail)

| Concern | Current | Target |
|---------|---------|--------|
| Queue + cancel | Priority queue; ctx cancel tested | Propagate into fan-out children |
| Fallback / breaker | `FallbackChain` + live reprobe | Shared per-backend breaker for swarm workers |
| `/cloud` race | TipTap mid-text match + `ArmOrigin` | Keep regression tests forever |
| RunSSE hub | `pending`/`waiting` maps | Multi-offer only after Path A tools proven |
| Fan-out backpressure | Unbuilt | Queue slots or swarm semaphore — never unbounded |
| Hot-swap mid-flight | Subscriber rebuild | Decision snapshot immutability |

Code: `internal/mitm/agent_fulfill_hub.go`, `internal/orchestrator/`, `internal/vram/manager.go` (`BatchReserve`).

---

## 5. Loop engineering

Map Cursor product patterns onto the harness (Glider does not own the IDE loop UI):

| Loop type | Cursor analogue | Glider hook | Status |
|-----------|-----------------|-------------|--------|
| Recurring eval | `/loop` interval | Checkpoint + re-`Complete` with last episode | 0% |
| Babysit CI | babysit / PR checks | Wake on CI fail → local fix loop | 0% |
| Lint/test reflect | impl plan §9.2 | Background runner; Cursor sees final SSE only | Design |
| Swarm wave | Slate parallel workers | FanOut + Episode weave | Stub |

Proposed checkpoint:

```json
{
  "goal": "...",
  "last_episode_id": "...",
  "eval_status": "fail|pass|unknown",
  "wake_reason": "interval|ci|file|manual",
  "next_delay_s": 300,
  "spend_tokens": 0
}
```

On wake: load checkpoint → route → execute → write episode → update checkpoint. Prefer episode summary over full transcript replay.

---

## 6. Slate-inspired patterns → Glider

| Slate | Meaning | Glider fit | Status |
|-------|---------|------------|--------|
| Orchestrator thread | Programs in action space | Local planner or Starlark+LLM → `SubTasks` | 0% |
| Worker threads | One bounded action | Short `Complete` jobs on Ollama pool | Stub |
| Episodes | Compressed returns | New type → history/metrics | Design |
| Thread weaving | Parallel + shared context | FanOut + hub scratchpad + merge SSE | 0% |
| Role routing | Plan/research/exec models | Aliases + classifier roles | Partial |
| Implicit planning | No rigid plan mode | Adaptive decompose | Design |
| Dumb-zone avoidance | Small memory via episodes | Ceiling = safety net only | Ceiling only |

**Do not copy:** TypeScript DSL runtime, swarm CLI UX, hive-mind claims before Path A tools are solid.

---

## 7. Backlog (P0–P3)

| Pri | Item | Status |
|-----|------|--------|
| P0 | `/cloud` hard-force | **Done** |
| P0 | Path A `tool_calls` stream bridge | Open |
| P1 | Role-aware classifier + dashboard chips | Open |
| P1 | Episode record on local fulfill | Open |
| P1 | Feature-flagged `FanOutExecutor` (gateway, 2 workers) | Stub |
| P2 | Session memory + turn budgets | Design |
| P2 | Path B child RunSSE tools | Blocked (prefer Path A) |
| P2 | Recurring eval loops | Design |
| P3 | Thread weaving + planner | Aspirational |
| P3 | Provider hot-swap without restart | Open |

---

## 8. Milestones

### ASAP — 48h

1. ✅ `/cloud` verified on TipTap Agent turns
2. Path A tool_calls stream bridge (M2 remainder)
3. Role tags on classifier + reason chips
4. Episode stub → history API
5. Flag `FanOutExecutor` gateway-only e2e

### 2 weeks

1. SessionState + turn budgets on Overview
2. Eval loop MVP (lint/test reflect)
3. Babysit-style CI wake adapter
4. Provider registry hot-reload (not ports/MITM)
5. Path B tools only if Path A bridge proven
6. `go test -race` where CGO available

---

## 9. Related code

| Piece | Path |
|-------|------|
| Explicit hard-force | `internal/router/explicit.go`, `router.go` |
| Task classifier | `internal/router/task_class.go` |
| Pipeline | `internal/orchestrator/pipeline.go` |
| Config Watch/Swap | `internal/config/watcher.go` |
| Path B hub | `internal/mitm/agent_fulfill_hub.go` |
| Strategy / SubTask | `internal/backend/interfaces.go` |
| VRAM BatchReserve | `internal/vram/manager.go` |
| V2 notes | `planning/implementation_plan.md` §9 |
