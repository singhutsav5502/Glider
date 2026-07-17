# Glider

Local AI harness that sits above Cursor: an OpenAI-compatible proxy that routes requests to Ollama/vLLM or cloud APIs, manages VRAM, and exposes a live dashboard.

## Quick start

```bash
# Requires Go 1.22+
go test ./...
go build -o glider.exe ./cmd/glider
./glider.exe --config configs/glider.yaml
```

Point Cursor at `http://localhost:8080/v1`. Open the dashboard at `http://localhost:8081`.

Set cloud keys via environment variables referenced in `configs/glider.yaml` (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`).

## Architecture

| Package | Role |
|---------|------|
| `internal/api` | OpenAI-compatible gateway + SSE |
| `internal/backend` | Ollama, vLLM, OpenAI, Anthropic |
| `internal/config` | `glider.yaml` load + hot-reload |
| `internal/router` | Explicit / regex / context / Starlark rules |
| `internal/transform` | Tokenizer + opt-in trim/augment |
| `internal/orchestrator` | Lifecycle, queue, fallback, circuit breaker |
| `internal/vram` | nvidia-smi monitor + allocation |
| `internal/metrics` | Route/token/cost/latency + event bus |
| `internal/dashboard` | Embedded Web UI + REST + WebSocket |

## Tests

Development follows the TDD plan in `planning/tdd_test_plan.md`.

```bash
go test ./...
go test -race ./...
```

## Config

See `configs/glider.yaml` for the full schema (thresholds, models, routing rules, backends, cloud budget).
