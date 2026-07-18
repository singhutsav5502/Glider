# Planning docs — index (source of truth)

> **As of 2026-07-18 (late).** Code + tests are authority; these docs summarize **Done / Partial / Next**.  
> Ignore `planning/Depreceated/`. Do not treat `.cursor/plans/` as product status.

## Quick map

| Doc | Role |
|-----|------|
| **[This file](README.md)** | Index + backlog (start here) |
| [project_summary.md](project_summary.md) | Product overview, dual-mode, what’s shipped |
| [implementation_plan.md](implementation_plan.md) | HLD/LLD architecture reference (status banner at top) |
| [cursor_agent_protocol_interception.md](cursor_agent_protocol_interception.md) | Path A/B Agent status + remaining G13 work |
| [routing_session_policy.md](routing_session_policy.md) | Path B turn-family sticky + analytics definitions |
| [smart_routing_and_local_tools.md](smart_routing_and_local_tools.md) | Classifier + Path A tools + complexity score + local context |
| [context_management.md](context_management.md) | `contextgraph` hybrid MVP + Episode/export/prune + local→Ollama flow |
| [loop_engineering.md](loop_engineering.md) | **Canonical** Loop Engineering (Osmani / cobusgreyling) — hoops, stages, eval |
| [loop_swarm_gap_plan.md](loop_swarm_gap_plan.md) | **Living** expected-features checklist + overnight P0/P1 gap work |
| [swarm_orchestration.md](swarm_orchestration.md) | Swarm/FanOut honesty + hot-swap (+ canvas companion) |
| [cursor_prior_art.md](cursor_prior_art.md) | **Archival** external prior art (Glider Path B text is novel) |
| [cursor_agent_rpc_debug_findings.md](cursor_agent_rpc_debug_findings.md) | **Archival** live capture / wire notes (shrunk) |
| [cursor_intercept_methodology.md](cursor_intercept_methodology.md) | **Archival** research methodology (shrunk) |
| [mock_dashboard_ui_design.md](mock_dashboard_ui_design.md) | UI mock reference (functional dashboard shipped) |
| [../STATUS.md](../STATUS.md) | Overnight build / how to run |
| [../README.md](../README.md) | User-facing setup |

**Removed (merged / obsolete):** `context_and_swarm_architecture.md`, `glider_orchestration_roadmap.md` (canvas kept), `tdd_test_plan.md` — use `go test ./...` + this backlog.

---

## Done (shipped)

| Area | Evidence |
|------|----------|
| Dual-mode gateway + MITM + shared `PipelineCompleter` | `cmd/glider`, `internal/api`, `internal/mitm`, `internal/orchestrator` |
| Path B text Agent: BidiAppend extract → hub → RunSSE fulfill | `agent_rpc_fulfill`; canned UI-proven; Ollama when backends healthy |
| `/cloud` / `/heavy` hard-force (TipTap-safe); never canned | `explicit.go`, MITM `ArmOrigin` |
| Turn-family sticky (~90s): summary/title/chrome; not conversation-wide | `OpenTurnFamily`, `IsTurnFollowOn`, `IsSystemSummaryChrome` |
| Composer summary sticky (`user_visible_high_level_summary` → `bidi_sticky_cloud_summary`) | `agent_fulfill_hub.go` / tests |
| Subagent sticky under StickyCloud (`bidi_sticky_cloud_child`) | tests + metrics |
| `contextgraph` hybrid MVP (events + turn index + sticky consult) | `internal/contextgraph`; sticky uses `ResolveCloudSticky` / live open-run |
| Path A: `cus-`, tools attach, Anthropic normalize, stream `tool_calls` SSE | gateway + backends |
| Task classifier + role hints + `tool_followup` (Path A allowlist; Path B would_*) | `task_class.go`, `tool_followup.go` |
| Dashboard: Config / VRAM / Rules / Overview LOCAL·CLOUD·CANNED % + CLASS chips | `internal/dashboard`, `internal/metrics` |
| Orchestrator 1:1 (queue, fallback, breaker, rate/budget, VRAM) | `internal/orchestrator`, `internal/vram` |
| Swarm FanOut + critique merge + sample `/swarm` rule | `internal/swarm`, `FanOutExecutor`, `fanout_dual_view.star` |
| E2E + benches; `go test ./...` green | `e2e/`, `bench/` |

## Partial

| Area | Reality |
|------|---------|
| Path B tool loops / child RunSSE | Origin only; `tool_followup_would_local` logged — **no codec** |
| Episode / SessionState (`contextkit`) | Wired on fulfill / fan-out / loop; export + prune APIs |
| FanOut productization | Enabled in default config + sample Starlark `/swarm` rule; critique merge |
| Hot-swap | Router/aliases/threshold/log/GPU hot; backends/MITM/ports need restart |
| Loop engineering | Hoop cycles + parallel actors + live progress + graph_edges — [loop_engineering.md](loop_engineering.md), [loop_swarm_gap_plan.md](loop_swarm_gap_plan.md) |
| Dashboard polish | Functional, not pixel-perfect vs mock |
| Live GPU / Ollama | Depends on local services; nvidia-smi path yes, `nvml.dll` no |

## Next (prioritized backlog)

### P0 — reliability / correctness

1. **Manual Cursor checklist** on a real install (Path B text + `/cloud` wrap-up + summary chrome) — [docs/CURSOR_CHECKLIST.md](../docs/CURSOR_CHECKLIST.md)
2. Keep Path B fail-soft + sticky regressions green (`agent_rpc_fulfill_test.go`) as Cursor builds change
3. Prefer Path A + `cus-` for any Agent+tools demos until Path B tool codec exists

### P1 — high leverage product

1. **Overview episode chip** — API ready (`/api/context/episodes`); wire dashboard UI
2. ~~Default or sample FanOut rule~~ — **done** (`fanout_dual_view.star` + `orchestration.fan_out.enabled`)
3. Classifier block editable in Rules UI (or documented YAML-only)
4. Harden Path B empty `conversation_checkpoint` / codec from origin RESP peeks when UI flakes
5. Loop/swarm polish remaining — see [loop_swarm_gap_plan.md](loop_swarm_gap_plan.md) P2

### P2 — later

1. Path B child RunSSE / tool-frame fulfill (feature-flagged) — only after Path A tools proven in user’s workflow
2. SessionState + turn budgets on dashboard
3. SKILL.md load, worktrees, L3 denylist/budget — [loop_engineering.md](loop_engineering.md)
4. Backend live hot-reload without restart; optional `nvml.dll`; `go test -race` where CGO available
5. Slate-like planner / thread-weaving (aspirational)

---

## Routing priority (both modes)

1. Explicit `/cloud` `/heavy` or `/local` `/fast` (TipTap-safe hard-force)
2. **Turn-family sticky** (Path B follow-on / summary / subagent chrome only)
3. Tool-step re-decide (`tool_followup`) — Path A can local; Path B metric-only
4. Task classifier (tools / must-cloud / small-local + role hints)
5. Starlark
6. Token ceiling
7. Default cloud (gateway → BYOK; MITM → origin)

Details: [routing_session_policy.md](routing_session_policy.md), [smart_routing_and_local_tools.md](smart_routing_and_local_tools.md).
