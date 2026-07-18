# Swarm & orchestration engine

> Honest status **2026-07-18**. Full architecture (context, hot-swap, concurrency, loops):
> [context_and_swarm_architecture.md](./context_and_swarm_architecture.md),
> [context_management.md](./context_management.md).
> Routing/tools: [smart_routing_and_local_tools.md](./smart_routing_and_local_tools.md).
> Turn-family + tool-followup: [routing_session_policy.md](./routing_session_policy.md).
> Interactive canvas (open beside chat):
> `C:\Users\Utsav\.cursor\projects\d-repos-Glider\canvases\glider-orchestration-roadmap.canvas.tsx`
>
> Slate / Random Labs: https://randomlabs.ai/ Â· https://randomlabs.ai/blog/slate

---

## 1. How far are we? (honest %)

| Capability | % done | What exists in Glider today |
|------------|--------|------------------------------|
| **Single-request routing** | **~85%** | Explicit overrides, task_classifier + roles, Starlark, token ceiling, shared `PipelineCompleter` |
| **Orchestration engine (1:1 execute)** | **~75%** | Lifecycle, priority queue, fallback, breaker, rate/budget, aliases, VRAM hybrid |
| **Local/cloud split + Path B** | **~55%** overall; Path B text **~40%** | Text fulfill; tools origin + `tool_followup_would_local`; `/cloud` hard-force **done** |
| **Local tools (Path A)** | **~75%** | `Tools`/`ToolChoice` + stream tool_calls SSE bridge |
| **Context management** | **~55%** | `contextgraph` hybrid MVP (JSONL + turn/request/session index); sticky/summary consults graph; dashboard `/api/context/*` | Blackboard; episode merge on FanOut |
| **Hot-swap modules** | **~65%** | `internal/swarm` Registry + fan_out Apply; router/aliases/threshold/log/GPU hot; backends/MITM restart |
| **Multi-agent / swarms** | **~40%** | `internal/swarm` FanOut+Merge+Loop+HotSwap; FanOutExecutor cancel-aware; no planner / default fan_out rules |
| **Loop engineering (lint/test reflect)** | **~0%** | Documented only in implementation_plan Â§9.2 |
| **Slate-like thread weaving** | **~0%** | Not started â€” patterns below are aspirational |

**Bottom line:** Routing + Path A tool_calls bridge are production-usable. **Swarms have FanOut + context stubs** â€” not Slate parity.

---

## 2. Slate (Random Labs) â€” takeaways for Glider

Source: [randomlabs.ai](https://randomlabs.ai/), [docs](https://docs.randomlabs.ai/en/getting-started/introduction), [blog/slate](https://randomlabs.ai/blog/slate), VentureBeat / product writeups (2026).

### Architecture patterns

| Slate idea | Meaning | Glider fit |
|------------|---------|------------|
| **Orchestrator thread** | Central agent â€œprograms in action spaceâ€ (DSL), does not do all tactics itself | Future: local planner model or Starlark+LLM that emits bounded work units |
| **Worker threads** | One **action** then pause; not long-lived role subagents | Maps to short-lived `SubTask` jobs on local Ollama pool |
| **Episodes** | Compressed step history returned to orchestrator (not full transcript / brittle message-pass) | New type: `Episode{Summary, Artifacts, Tokens}` fed into next route decision |
| **Thread weaving** | Parallel workers; episodes compose; shared context by design | Needs VRAM `BatchReserve` + aggregator SSE (already sketched in impl plan Â§9.1) |
| **Model routing by role** | Plan â†’ Claude-class; research â†’ retrieval-strong; exec â†’ Codex-class | Glider already has multi-backend + aliases â€” extend classifier with `role: plan\|research\|exec` |
| **Implicit planning** | No separate â€œplan modeâ€; research then present plan in conversation | Prefer adaptive decomposition over rigid plannerâ†’coderâ†’reviewer pipelines (Slate warns those feel slow) |
| **Dumb-zone avoidance** | Keep working memory small via episodes, not lossy whole-context compaction | Aligns with Glider token ceiling as safety net, not primary signal |
| **Worktrees / sessions** | Parallel git contexts with separate chat state | Out of Glider scope (Cursor owns workspace); optional later for multi-root |

### What *not* to copy blindly

- Full TypeScript DSL orchestration runtime â€” Glider stays Go; start with JSON/Starlark work graphs.
- Swarm-native UX CLI â€” Glider is a **proxy/harness**, not a coding agent product.
- Claiming â€œhive mindâ€ before Path A tool loops and reliable `/cloud` (now fixed) are solid.

### Quick wins inspired by Slate (safe, high leverage)

1. **Role-tagged routing** â€” extend task_classifier with `plan` / `exec` / `research` hints â†’ different local models (already in registry).
2. **Episode stub** â€” after local fulfill, store 1-line summary + rule/reason in metrics/history (no multi-agent yet).
3. **Bounded fan-out prototype** â€” `StrategyFanOut` only for gateway Mode A, 2 workers, text-merge; feature-flagged.
4. **Keep explicit overrides absolute** â€” Slate-style expressivity still needs a human hard-force (`/cloud`).

---

## 3. Gap vs Slate-inspired target

```
Glider today:     request â†’ route â†’ single Execute â†’ stream
Slate-like target: request â†’ orchestrator â†’ N threads â†’ episodes â†’ weave â†’ stream
```

| Gap | Effort | Blocked by |
|-----|--------|------------|
| Fan-out executor | M | VRAM batch + merge SSE |
| Planner decomposition | L | Stable Path A tools (M2) preferred |
| Episode memory store | M | Schema + dashboard |
| Session / loop checkpoints | M | Context architecture doc Â§2 |
| Parallel Agent Path B | XL | Cursor tool-loop protocol |
| DSL action space | L | Product decision (Starlark vs JSON) |

---

## 4. ASAP milestone order (next 48h)

Ordered for **user-visible reliability first**, then swarm foundation:

| # | Window | Work | Done when |
|---|--------|------|-----------|
| **P0** | 0â€“2h | âœ… `/cloud` hard-force + rebuild | TipTap `/cloud` never local/canned |
| **P1** | 2â€“8h | âœ… Path A `tool_calls` stream bridge (M2) | Agent+tools via gateway SSE |
| **P2** | 8â€“16h | âœ… Role-aware classifier + dashboard class chips | Metrics show class/role rates |
| **P3** | 16â€“32h | ðŸŸ¡ `Episode` stubs (`contextkit`) â€” not wired to fulfill | Wire â†’ Overview episodes |
| **P4** | 32â€“48h | ðŸŸ¡ Feature-flagged `FanOutExecutor` (default off) | Enable + e2e with StrategyFanOut rule |

**Do not** start Path B multi-agent or full thread-weaving until Path A tools proven (now green).

### 2-week horizon (see context doc)

SessionState + turn budgets â†’ eval/reflect loop MVP â†’ CI babysit wake â†’ cautious provider hot-reload â†’ Path B tools only if Path A proven â†’ race detector sign-off.

---

## 5. Code anchors

| Piece | Path |
|-------|------|
| Hard-force explicit | `internal/router/explicit.go`, `router.go` |
| Task classifier | `internal/router/task_class.go` |
| Tool followup | `internal/router/tool_followup.go` |
| Path A tool_calls bridge | `internal/backend/stream.go`, `internal/api/streaming.go` |
| Context stubs | `internal/contextkit` |
| FanOut executor | `internal/orchestrator/fanout.go` + `internal/swarm` |
| Swarm package | `internal/swarm` |
| Pipeline / CompleteLocal | `internal/orchestrator/pipeline.go` |
| Config Watch/Swap | `internal/config/watcher.go` |
| Path B hub | `internal/mitm/agent_fulfill_hub.go`, `intercept.go` |
| V2 swarm notes | `planning/implementation_plan.md` Â§9 |
| Strategy enum | `internal/backend/interfaces.go` (`RoutingDecision.Strategy`) |
| VRAM BatchReserve | `internal/vram/manager.go` |

---

## 6. Retest `/cloud` (operator)

1. Stop any running Glider.
2. Start `D:\___repos\Glider\glider.exe` (or `glider.new.exe` if `.exe` was locked).
3. Agent chat: `/cloud say hello` (and a TipTap-only `/cloud` turn).
4. Expect: Cursor origin model prose â€” **not** Ollama â€” **not** `pong from glider (canned Path B)`.
5. Logs/metrics: `bidi_cloud_override` or `bidi_decide_passthrough`; **zero** `runsse_local` / `runsse_canned` for that corr_id.
