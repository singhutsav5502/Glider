# Glider Overnight Build Status

## Done

Implemented Phases 1–4 (core) and substantial Phase 5 coverage from `planning/`, using TDD.

| Area | Package(s) | Status |
|------|------------|--------|
| API gateway + SSE | `internal/api` | Green |
| Backends (Ollama/vLLM/OpenAI/Anthropic) + registry | `internal/backend/...` | Green |
| Config load + hot-reload | `internal/config` | Green |
| Router + Starlark | `internal/router` | Green |
| Tokenizer + opt-in transforms | `internal/transform` | Green |
| VRAM monitor/allocator | `internal/vram` | Green |
| Orchestrator (lifecycle, queue, fallback, breaker, rate/budget) | `internal/orchestrator` | Green |
| Metrics + event bus | `internal/metrics` | Green — request events include mode/action/host/path/rule/original_model; session-grouped JSONL history under `~/.glider/history` |
| Dashboard REST/WS + embedded UI | `internal/dashboard` | Green — VRAM/models discovery, GPU assignment UI, Rules editor, Config tooltips, YAML optional, session analytics; APIs: `/api/vram`, `/api/validate`, `/api/sessions`, `/api/gpu-assignments` |
| Composition root | `cmd/glider` | Builds |
| E2E passthrough/routing/fallback/concurrency/corrupt-config | `e2e` | Green |
| Benchmarks (proxy overhead, rule eval) | `bench` | Green |

**All packages:** `go test ./...` passes.

**Benchmarks (approx on this machine):**
- Proxy passthrough ~0.12 ms/op (target &lt; 5 ms)
- Rule evaluation ~0.38 µs/op (target &lt; 1 ms)

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

- **Gateway** `:8080/v1` — BYOK OpenAI Base URL; same `PipelineCompleter` as MITM
- **MITM** `:8082` — HTTPS CONNECT; `CompleteLocal` → local Ollama/vLLM or **origin passthrough** (cloud ≠ BYOK)
- **Observability** — pipeline records gateway/mitm actions (`local`, `cloud`, `origin_passthrough`, `error`); MITM also emits CONNECT `decrypt` / `blind_tunnel` and interceptor `skip` / parse `error`; slog fields include host/path/model; dashboard WS mirrors the same
- Routing: explicit overrides → Starlark → context thresholds → default (see `configs/glider.yaml`)
- Docs: [README.md](README.md), [docs/CURSOR_CHECKLIST.md](docs/CURSOR_CHECKLIST.md)
- Profiles: [configs/glider.yaml](configs/glider.yaml) (intro / MITM on), [configs/glider.cloud.yaml](configs/glider.cloud.yaml) (default cloud BYOK)
- Config UI: dashboard **Config** tab (structured form primary; section cards + tooltips; Edit YAML optional); save hot-reloads routing/aliases/threshold/**log level**; GPU assignments persist via same Swap; restart for ports/MITM/backends/providers
- VRAM & Models: `GET /api/vram` discovers Ollama tags / vLLM models + nvidia-smi gauges; GPU assignment UI → `vram.gpu_assignments`; soft catalog warnings via `/api/validate`
- Rules Engine: create/edit/enable rules in UI (priority, explicit/script/context_size/always) → config
- Overview: request log columns Mode/Action/Host·Model/Rule; browse historical sessions under `~/.glider/history` + live tail
- CA setup: Cursor-only proxy; Trusted Root install does not rewrite normal Windows internet for non-proxy clients

## Remaining / incomplete

- Full Phase 5 stress/`go test -race` not signed off in this run (race detector needs CGO toolchain on Windows).
- Optional Windows `nvml.dll` monitor stub not implemented (nvidia-smi path is).
- Dashboard UI is functional minimal frontend (not pixel-perfect vs mockups).
- Starlark-scriptable transforms (advanced) not separate from routing Starlark.
- Live nvidia-smi / real Ollama/vLLM integration depends on local GPU services.
- Manual Cursor Agent checklist still needs human verification on a real Cursor install.
- Duplicate early Phase 1 commit exists in history (`da28bc8` / `97406b8`); harmless.

## Planning docs used

- `planning/project_summary.md` (dual-mode + shared harness; explicit overrides vs script/threshold drivers)
- `planning/implementation_plan.md` (Phase 6 shared `Complete`/`CompleteLocal`; routing priority)
- `planning/tdd_test_plan.md` (Phase 6 incl. harness tests; dashboard vram/sessions APIs; ~112 total)
- `planning/mock_dashboard_ui_design.md`
- Ignored: `planning/Depreceated/**`
