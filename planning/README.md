# Planning docs â€” index (source of truth)

> **As of 2026-07-24.** Code + tests are authority; these docs summarize **Done / Partial / Next**.  
> Ignore `planning/Depreceated/`. Do not treat `.cursor/plans/` as product status.

## Quick map

| Doc | Role |
|-----|------|
| **[This file](README.md)** | Index + backlog (start here) |
| [project_summary.md](project_summary.md) | Product overview, dual-mode, whatâ€™s shipped |
| [implementation_plan.md](implementation_plan.md) | HLD/LLD architecture reference (status banner at top) |
| [cursor_agent_protocol_interception.md](cursor_agent_protocol_interception.md) | Path A/B Agent status + remaining G13 work |
| [routing_session_policy.md](routing_session_policy.md) | Path B turn-family sticky + analytics definitions |
| [smart_routing_and_local_tools.md](smart_routing_and_local_tools.md) | Classifier + Path A tools + complexity score + local context |
| [context_management.md](context_management.md) | `contextgraph` hybrid MVP + Episode/export/prune + localâ†’Ollama flow |
| [loop_engineering.md](loop_engineering.md) | **Canonical** Loop Engineering (Osmani / cobusgreyling) â€” hoops, stages, eval |
| [loop_swarm_gap_plan.md](loop_swarm_gap_plan.md) | **Living** expected-features checklist + overnight P0/P1 gap work |
| [loop_swarm_gap_analysis.md](loop_swarm_gap_analysis.md) | Overnight code gap table (loop / swarm / graph UX) |
| [graph_feature_gaps.md](graph_feature_gaps.md) | **Graph gaps vs prior art** (LangGraph, Temporal, Slate, Anthropic, â€¦) |
| [enterprise_orchestrator_mvp.md](enterprise_orchestrator_mvp.md) | **Enterprise orchestrator MVP** â€” usage areas, strategy, MVP vs later |
| [orchestrator_overnight_plan.md](orchestrator_overnight_plan.md) | Overnight SM / HITL / tools / MCP checklist |
| [tools_catalog.md](tools_catalog.md) | Thin builtin/MCP index â†’ see **tools_mcp.md** |
| [tools_mcp.md](tools_mcp.md) | **Canonical** tools, ScopeRel, web search, blind vs agent loop, MCP UI |
| [graphify_context_notes.md](graphify_context_notes.md) | Graphify research vs contextgraph |
| [remaining_gaps.md](remaining_gaps.md) | **Living** leftover gaps + session status |
| [intentional_backlog.md](intentional_backlog.md) | **Deferred-by-design** deep plans (ToolCall catalog, DIP, Copilot PAT, enterprise) |
| [swarm_orchestration.md](swarm_orchestration.md) | Swarm/FanOut honesty + hot-swap (+ canvas companion) |
| [cursor_prior_art.md](cursor_prior_art.md) | **Archival** external prior art (Glider Path B text is novel) |
| [cursor_agent_rpc_debug_findings.md](cursor_agent_rpc_debug_findings.md) | **Archival** live capture / wire notes (shrunk) |
| [cursor_intercept_methodology.md](cursor_intercept_methodology.md) | **Archival** research methodology (shrunk) |
| [mock_dashboard_ui_design.md](mock_dashboard_ui_design.md) | UI mock reference (functional dashboard shipped) |
| [../STATUS.md](../STATUS.md) | Overnight build / how to run |
| [../README.md](../README.md) | User-facing setup |

**Removed (merged / obsolete):** `context_and_swarm_architecture.md`, `glider_orchestration_roadmap.md` (canvas kept), `tdd_test_plan.md` â€” use `go test ./...` + this backlog.

---

## Done (shipped)

| Area | Evidence |
|------|----------|
| Dual-mode gateway + MITM + shared `PipelineCompleter` | `cmd/glider`, `internal/api`, `internal/mitm`, `internal/orchestrator` |
| Path B text Agent: BidiAppend extract â†’ hub â†’ RunSSE fulfill | `agent_rpc_fulfill`; canned UI-proven; Ollama when backends healthy |
| `/cloud` / `/heavy` hard-force (TipTap-safe); never canned | `explicit.go`, MITM `ArmOrigin` |
| Turn-family sticky (~90s): summary/title/chrome; not conversation-wide | `OpenTurnFamily`, `IsTurnFollowOn`, `IsSystemSummaryChrome` |
| Composer summary sticky (`user_visible_high_level_summary` â†’ `bidi_sticky_cloud_summary`) | `agent_fulfill_hub.go` / tests |
| Subagent sticky under StickyCloud (`bidi_sticky_cloud_child`) | tests + metrics |
| `contextgraph` hybrid MVP (events + turn index + sticky consult) | `internal/contextgraph`; sticky uses `ResolveCloudSticky` / live open-run |
| Path A: `cus-`, tools attach, Anthropic normalize, stream `tool_calls` SSE | gateway + backends |
| Task classifier + role hints + `tool_followup` (Path A allowlist; Path B would_*) | `task_class.go`, `tool_followup.go` |
| Dashboard: Config / VRAM / Rules / Overview LOCALÂ·CLOUDÂ·CANNED % + CLASS chips | `internal/dashboard`, `internal/metrics` |
| Orchestrator 1:1 (queue, fallback, breaker, rate/budget, VRAM) | `internal/orchestrator`, `internal/vram` |
| Swarm FanOut + critique merge + sample `/swarm` rule | `internal/swarm`, `FanOutExecutor`, `fanout_dual_view.star` |
| E2E + benches; `go test ./...` green | `e2e/`, `bench/` |
| Path B common ToolCall map (opt-in `agent_rpc_tool_codec`) | `internal/cursorrpc/toolcall_map.go` + Truncated fallback |
| `CycleExecutor` body migration + `prompt.go` + `CheckGovernance` | `internal/loop/cycle_executor.go`, `prompt.go`, `governance.go` |

## Partial

> **Authoritative matrix:** [remaining_gaps.md](remaining_gaps.md) (16 areas). Do not invent enterprise features as shipped.

| Area | Reality |
|------|---------|
| Path B full ToolCall catalog / live UI verify | **DEFERRED** â€” common Read/Grep/Edit/Shell/Glob/Ls/Web map **SHIPPED** (opt-in); prefer Path A for Agent+tools demos |
| Dashboard Server DIP | **PARTIAL** â€” file split done; concrete `*Manager`/`*Runner` fields remain |
| Hosted Copilot MCP live PAT | **PARTIAL** â€” session harden shipped; production PAT verify ops-only |
| Hot-swap | Router/aliases/threshold/log/GPU hot; backends/MITM/ports need restart (**DEFERRED** live backend reload) |
| Dashboard polish | Functional, not pixel-perfect vs mock; agentlog empty-state polish minor |
| Live GPU / Ollama | Depends on local services; nvidia-smi path yes, `nvml.dll` no |

## Next (prioritized backlog)

Deep deferred plans: [intentional_backlog.md](intentional_backlog.md). Matrix: [remaining_gaps.md](remaining_gaps.md) — P0 none; P1 optional Manager SRP follow-ups; P2 DIP/globals; enterprise items **DEFERRED**.

### P0 â€” reliability / correctness

1. **Manual Cursor checklist** on a real install (Path B text + `/cloud` wrap-up + summary chrome) â€” [docs/CURSOR_CHECKLIST.md](../docs/CURSOR_CHECKLIST.md)
2. Keep Path B fail-soft + sticky regressions green (`agent_rpc_fulfill_test.go`) as Cursor builds change
3. Prefer Path A + `cus-` for Agent+tools demos (Path B common map ships opt-in; full catalog **DEFERRED**)

### P1 â€” high leverage

1. Classifier block editable in Rules UI (or documented YAML-only)
2. Harden Path B empty `conversation_checkpoint` / codec from origin RESP peeks when UI flakes
3. Optional: migrate `runCycle` call sites to `m.Exec().Complete*` directly â€” [solid_refactor.md](solid_refactor.md)

### P2 â€” later

1. Dashboard Server DIP; `contextgraph.Default()` removal; swarm governance extract — details in [intentional_backlog.md](intentional_backlog.md)
2. Hosted Copilot MCP live PAT verify (ops)
3. Backend live hot-reload without restart; optional `nvml.dll`; `go test -race` where CGO available

---

## Routing priority (both modes)

1. Explicit `/cloud` `/heavy` or `/local` `/fast` (TipTap-safe hard-force)
2. **Turn-family sticky** (Path B follow-on / summary / subagent chrome only)
3. Tool-step re-decide (`tool_followup`) â€” Path A can local; Path B metric-only
4. Task classifier (tools / must-cloud / small-local + role hints)
5. Starlark
6. Token ceiling
7. Default cloud (gateway â†’ BYOK; MITM â†’ origin)

Details: [routing_session_policy.md](routing_session_policy.md), [smart_routing_and_local_tools.md](smart_routing_and_local_tools.md).

