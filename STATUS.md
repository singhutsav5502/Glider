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
| Metrics + event bus | `internal/metrics` | Green |
| Dashboard REST/WS + embedded UI | `internal/dashboard` | Green |
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
- Dashboard: `http://localhost:8081`
- Cloud keys: `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` (from config env names)

Go SDK used for this build: portable install at `%LOCALAPPDATA%\go-sdk\go` (1.24.5).

## Remaining / incomplete

- Full Phase 5 stress/`go test -race` not signed off in this run (race detector needs CGO toolchain on Windows).
- Optional Windows `nvml.dll` monitor stub not implemented (nvidia-smi path is).
- Dashboard UI is functional minimal frontend (not pixel-perfect vs mockups).
- Starlark-scriptable transforms (advanced) not separate from routing Starlark.
- Live nvidia-smi / real Ollama/vLLM integration depends on local GPU services.
- Duplicate early Phase 1 commit exists in history (`da28bc8` / `97406b8`); harmless.

## Planning docs used

- `planning/project_summary.md`
- `planning/implementation_plan.md`
- `planning/tdd_test_plan.md`
- `planning/mock_dashboard_ui_design.md`
- Ignored: `planning/Depreceated/**`
