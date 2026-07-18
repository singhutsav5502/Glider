# Glider Overnight Build Status

## Done

Implemented Phases 1Ã¢â‚¬â€œ4 (core) and substantial Phase 5 coverage from `planning/`, using TDD.

| Area | Package(s) | Status |
|------|------------|--------|
| API gateway + SSE | `internal/api` | Green Ã¢â‚¬â€ Path A **tool_calls** SSE + Anthropic tool_use/result normalize |
| Backends (Ollama/vLLM/OpenAI/Anthropic) + registry | `internal/backend/...` | Green Ã¢â‚¬â€ `ParseOpenAIStreamPayload` + `AttachTools` + `ToolsUnsupportedError` |
| Config load + hot-reload | `internal/config` | Green Ã¢â‚¬â€ `routing.tool_followup`, `task_classifier`, `orchestration.fan_out` |
| Router + Starlark | `internal/router` | Green Ã¢â‚¬â€ task classifier + role hints + tool-followup (MVP complete) |
| Tokenizer + opt-in transforms | `internal/transform` | Green |
| VRAM monitor/allocator | `internal/vram` | Green |
| Orchestrator (lifecycle, queue, fallback, breaker, rate/budget) | `internal/orchestrator` | Green Ã¢â‚¬â€ `FanOutExecutor` foundation (flag-off) |
| Context stubs (Episode / SessionState / TurnBudget) | `internal/contextkit` | Green Ã¢â‚¬â€ in-memory store; not yet wired to fulfill path |
| Metrics + event bus | `internal/metrics` | Green Ã¢â‚¬â€ distribution LOCAL/CLOUD/CANNED % + `class_rates` / `role_rates` |
| Dashboard REST/WS + embedded UI | `internal/dashboard` | Green Ã¢â‚¬â€ Overview LOCAL/CLOUD % + CLASS chips |
| Composition root | `cmd/glider` | Builds |
| E2E passthrough/routing/fallback/concurrency/corrupt-config | `e2e` | Green |
| Benchmarks (proxy overhead, rule eval) | `bench` | Green |

**All packages:** `go test ./...` passes.

**Benchmarks (approx on this machine):**
- Proxy passthrough ~0.12 ms/op (target &lt; 5 ms)
- Rule evaluation ~0.38 Ã‚Âµs/op (target &lt; 1 ms)

## How to run

```powershell
$env:PATH = "$env:LOCALAPPDATA\go-sdk\go\bin;$env:PATH"
$env:GOROOT = "$env:LOCALAPPDATA\go-sdk\go"
cd D:\___repos\Glider
go test ./...
go test ./bench -bench=. -benchtime=1s
go build -o glider.exe ./cmd/glider
.\glider.exe --config configs/glider.yaml
```

- Proxy: `http://localhost:8080/v1`
- Dashboard: `http://localhost:8081` (Overview sessions, VRAM & Models, Rules Engine, Config form; YAML optional)
- Session history: `~/.glider/history` (per process-run session; live WS still works)
- Cloud keys: `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` (from config env names)

Go SDK used for this build: portable install at `%LOCALAPPDATA%\go-sdk\go` (1.24.5).

## Dual-mode proxy (shared harness)

- **Gateway** `:8080/v1` Ã¢â‚¬â€ BYOK OpenAI Base URL; same `PipelineCompleter` as MITM; preferred path for **Agent + tools** (`cus-` model prefix). Tools attach + **stream `tool_calls` re-emitted** to Cursor SSE.
- **MITM** `:8082` Ã¢â‚¬â€ HTTPS CONNECT decrypt on allowlisted hosts; OpenAI/Responses JSON + **Path B text-only Agent** (`agent_rpc_fulfill`: BidiAppend extract Ã¢â€ â€™ RunSSE fulfill). Tool loops / child RunSSE still origin (**`tool_followup_would_local`** logged when allowlisted). See [planning/cursor_agent_protocol_interception.md](planning/cursor_agent_protocol_interception.md)
- **Observability** Ã¢â‚¬â€ pipeline records gateway/mitm actions (`local`, `cloud`, `origin_passthrough`, `canned`, `error`); Path B **turn-family** sticky (~90s); dashboard LOCAL/CLOUD/CANNED % (**cloud % counts `origin_passthrough`**) + CLASS reason/role chips; `GET /api/metrics` Ã¢â€ â€™ `distribution` + `class_rates` + `role_rates` + `tokens_saved_est`
- Routing: explicit Ã¢â€ â€™ **turn-family sticky (Path B follow-ons only)** Ã¢â€ â€™ **task_classifier** (role hints) Ã¢â€ â€™ Starlark Ã¢â€ â€™ token ceiling Ã¢â€ â€™ default
- Path A: `CompletionRequest` carries `tools` / `tool_choice` + message `tool_calls`/`tool_call_id`; Ollama/vLLM/OpenAI attach + stream tool_calls bridge; tools-unsupported Ã¢â€ â€™ cloud fallback; `tools_force_cloud: true` by default (allowlisted tools can skip via `tool_followup`); classifier emits `contextgraph.EventRouteDecided`
- Docs: [README.md](README.md), [docs/CURSOR_CHECKLIST.md](docs/CURSOR_CHECKLIST.md)
- Profiles: [configs/glider.yaml](configs/glider.yaml) (intro / MITM on), [configs/glider.cloud.yaml](configs/glider.cloud.yaml) (default cloud BYOK)
- Config UI: dashboard **Config** tab; GPU assignments; Rules Engine; Overview request log
- CA setup: Cursor-only proxy; Trusted Root install does not rewrite normal Windows internet for non-proxy clients

## Remaining / incomplete

- **Cursor Agent tool-loop fulfill on MITM** (G13 remainder) Ã¢â‚¬â€ text-only Path B works; child RunSSE / tools still origin; **would_local** metrics only until Path B tool codec. Tracked in `planning/cursor_agent_protocol_interception.md`.
- **Episode store not wired** into pipeline fulfill (stubs in `contextkit` + FanOut only).
- **FanOut** via `internal/swarm` (flag-off); no default rules emit `StrategyFanOut` yet. **Loop/HotSwap** skeletons only — not Cursor `/loop` or backend live reload.
- Full Phase 5 stress/`go test -race` not signed off (race detector needs CGO on Windows).
- Optional Windows `nvml.dll` monitor stub not implemented (nvidia-smi path is).
- Dashboard UI is functional minimal frontend (not pixel-perfect vs mockups).
- Starlark-scriptable transforms (advanced) not separate from routing Starlark.
- Live nvidia-smi / real Ollama/vLLM integration depends on local GPU services.
- Manual Cursor Agent checklist still needs human verification on a real Cursor install.
- Duplicate early Phase 1 commit exists in history (`da28bc8` / `97406b8`); harmless.

## Planning docs used

- `planning/routing_session_policy.md` (Path B **turn-family** sticky + analytics definitions)
- `planning/smart_routing_and_local_tools.md` (M1 task classifier + M2 Path A tools)
- `planning/swarm_orchestration.md` / `planning/glider_orchestration_roadmap.md`
- `planning/context_and_swarm_architecture.md` (Episode / SessionState design)
- `planning/project_summary.md`
- `planning/implementation_plan.md`
- `planning/cursor_agent_protocol_interception.md`
- `planning/tdd_test_plan.md`
- `planning/mock_dashboard_ui_design.md`
- Ignored: `planning/Depreceated/**`
