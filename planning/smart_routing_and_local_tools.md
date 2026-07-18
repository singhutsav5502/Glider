# Smart routing & local tooling

> Status: **done for Path A + classifier** (2026-07-18) — M0–M3 complete; Path B child tools still origin (M4).
>
> Related: [swarm_orchestration.md](./swarm_orchestration.md), [cursor_agent_protocol_interception.md](./cursor_agent_protocol_interception.md),
> [cursor_prior_art.md](./cursor_prior_art.md), [project_summary.md](./project_summary.md),
> [routing_session_policy.md](./routing_session_policy.md) (turn-family + tool-followup).

---

## Problem

Default routing was dominated by **context token thresholds**. That saves money when the prompt is short, but:

1. **Short ≠ simple** — a 2k-token “redesign this package” prompt should stay cloud/origin.
2. **Long ≠ always cloud** — a 12k-token paste with “rename `foo` → `bar` in this file” is often local-safe.
3. **Tools** — Path A keeps `tools` on `CompletionRequest`, passes them to Ollama/vLLM/OpenAI, and **re-emits stream `tool_calls`** on gateway SSE. Path B child/tool-loop RunSSE always origin.

Goal: route by **task shape** (and tool need) to offload cheap work locally, and give local models a **large tooling surface** where the protocol allows it (Path A first).

---

## Current state (code truth)

| Layer | What exists | Gap | ~Done |
|-------|-------------|-----|-------|
| Explicit overrides | `/local` `/cloud` `/fast` `/heavy` — **hard-force** | Cursor mention chips without slash text | **95%** |
| Rules | Explicit → **task_classifier** (role hints) → Starlark → ceiling | Classifier is regex MVP (no ML) | **100%** (MVP) |
| Config | `routing.task_classifier` + `tool_followup` | Dashboard Rules UI does not edit classifier block | **90%** |
| Path A tools | `Tools` / `ToolChoice`; message `tool_calls` / `tool_call_id`; Anthropic normalize; stream SSE | Optional Glider-side runners / MCP | **95%** |
| Path B | Text-only root RunSSE; tool_followup would_* | Child RunSSE / tool frames | **40%** |
| Swarms / multi-agent | `FanOutExecutor` + `contextkit` Episode/SessionState | No planner, no default fan_out rules | **15%** |
| Metrics | distribution + class_rates/role_rates + Overview chips + contextgraph `RouteDecided` | — | **95%** |

---

## A. Task-based routing

### Priority order (implemented + enforced)

1. **Explicit hard-force** `/cloud` `/heavy` → cloud/origin
2. **Explicit hard-force** `/local` `/fast` → local
3. **Turn-family sticky** (reply-summary / title only) — see [routing_session_policy.md](./routing_session_policy.md)
4. **Tool-step re-decide** (`routing.tool_followup`) — Path B metric-only until codec; Path A allowlist bypass
5. **Task classifier tools** (~85) → cloud when `tools[]` + `tools_force_cloud` (skipped when allowlisted)
6. **Task classifier must-cloud** (~80) → cloud (`role: plan`)
7. **Task classifier small-local** (~70) → local (`role: exec|research`)
8. **Starlark**
9. **Token ceiling** `context_size` (~10)
10. **Default** always → cloud when unsure

### `/cloud` leak (fixed 2026-07-18)

TipTap mid-text `/cloud` + priority inversion fixed. StickyCloud refuses child/subagent local (`bidi_sticky_cloud_child`). `LatestUserTurnText` strips history noise.

### Path A vs Path B

| Decision | Path A (gateway + `cus-`) | Path B (MITM Agent RPC) |
|----------|---------------------------|-------------------------|
| Small text Q&A / rename | Local Ollama/vLLM | Local root RunSSE text fulfill |
| Needs tools (`tools_force_cloud`) | BYOK cloud (until allowlist bypass) | **Origin** (child RunSSE) |
| Allowlisted tools only | Local + tool_calls SSE | Origin + `tool_followup_would_local` |
| `/cloud` | Cloud backend | Hard-force → `ArmOrigin` |

Classifier decisions emit `contextgraph.EventRouteDecided` (attrs: `reason`, `role`, `rule`) via `PipelineCompleter` — Append-only hook; does not own graph internals.

---

## B. Local tooling (Path A first)

### Shipped (M2)

- `CompletionRequest.Tools` / `ToolChoice`
- `Message.ToolCalls` / `ToolCallID` / `Name` for client tool-loop rounds
- Gateway Anthropic normalize: `tool_use` → `assistant.tool_calls`; `tool_result` → `role=tool`
- Ollama / vLLM / OpenAI attach tools when present
- **`ParseOpenAIStreamPayload`** parses stream `tool_calls`
- **`WriteChatSSE` / `WriteChatJSON`** re-emit tool_calls to Cursor

### Ollama / local tools limitation (documented fallback)

Many Ollama models **do not support tool-calling**. When the local backend returns a tools-unsupported error:

1. Backend wraps as `backend.ToolsUnsupportedError`
2. `FallbackChain` continues to the next step (BYOK cloud when configured)
3. Default config keeps `tools_force_cloud: true` so tool-heavy Agent turns skip local unless every tool is on `tool_followup.local_tool_allowlist`

Client still owns tool **execution**; Glider only preserves definitions + call/result shapes.

### Still TODO (out of Path A scope)

- Optional Glider-side runners / MCP catalog
- Path B child RunSSE tool frames (M4)

---

## C. Milestones

| Milestone | Status | Acceptance |
|-----------|--------|------------|
| **M0** Plan + banner | ✅ | Path B banner on dashboard |
| **M1** Task classifiers | ✅ | Rename local; architecture cloud; `/cloud` wins |
| **M1b** `/cloud` hard-force | ✅ | TipTap + priority inversion; never canned |
| **M2** Path A tools | ✅ | tool_calls SSE + message round-trip + Anthropic bridge |
| **M3** Metrics | ✅ | local%/cloud%/canned% + class_rates chips + RouteDecided |
| **M4** Path B tools | pending | Feature-flagged child RunSSE |
| **S1–S3** Swarms | 🟡 stubs | FanOut + contextkit |

---

## Quick try (after restart)

```text
/cloud rename foo to bar   → origin / Explicit Cloud
rename foo to bar          → Task Classifier Small-Local
/local explain this        → Explicit Local
```

Dashboard CLASS chips show `small_offload` / `must_cloud` rates after traffic.
