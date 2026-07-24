# Glider Overnight Build Status

## Done

Implemented Phases 1–4 (core), Phase 5/6 coverage, and post-6 Path B / classifier / tools / sticky / contextgraph work. TDD; `go test ./...` green. **Planning index:** [planning/README.md](planning/README.md).

| Area | Package(s) | Status |
|------|------------|--------|
| API gateway + SSE | `internal/api` | Green — Path A **tool_calls** SSE + Anthropic normalize |
| Backends + registry | `internal/backend/...` | Green — stream tool_calls + `ToolsUnsupportedError` |
| Config hot-reload | `internal/config` | Green — `tool_followup`, `task_classifier`, `orchestration.fan_out` |
| Router + Starlark | `internal/router` | Green — classifier + role hints + tool-followup MVP |
| Tokenizer + transforms | `internal/transform` | Green |
| VRAM | `internal/vram` | Green (nvidia-smi) |
| Orchestrator | `internal/orchestrator` | Green — FanOutExecutor foundation (flag-off) |
| Context | `internal/contextgraph`, `contextkit` | Graph MVP + Episode on fulfill/loop/fan-out; prune/export APIs |
| Pure local | `configs/glider.local.yaml` | `routing.default: local`, no cloud fallback, clear Ollama errors |
| Metrics + dashboard | `internal/metrics`, `dashboard` | LOCAL/CLOUD/CANNED % + CLASS chips |
| MITM Path B | `internal/mitm` | Text fulfill + turn-family / summary / subagent sticky |
| Loop engineering | `internal/loop` | **MVP** — Loop Engineering hoops (planner/actor/critic + eval + learning); see planning/loop_engineering.md |
| E2E + benches | `e2e`, `bench` | Green |

**Benchmarks (approx):** proxy passthrough ~0.12 ms/op; rule eval ~0.38 µs/op.

## How to run

```powershell
$env:PATH = "$env:LOCALAPPDATA\go-sdk\go\bin;$env:PATH"
$env:GOROOT = "$env:LOCALAPPDATA\go-sdk\go"
cd D:\___repos\Glider
go test ./...
go build -o glider.exe ./cmd/glider
.\glider.exe --config configs/glider.yaml
# Pure local (Ollama only, no cloud keys):
.\glider.exe --config configs/glider.local.yaml
```

- Gateway: `http://localhost:8080/v1`
- Dashboard: `http://localhost:8081`
- MITM: `:8082` (when enabled)
- History: `~/.glider/history`
- Context: `~/.glider/context` + `/api/context/*`
- Cloud keys: optional unless you route to BYOK (`OPENAI_API_KEY` / `ANTHROPIC_API_KEY`)

### Pure local how-to

1. Start Ollama (`ollama serve`) and pull `codellama:7b` (or edit model names).
2. `.\glider.exe --config configs/glider.local.yaml`
3. **Gateway-only (no Cursor sub):** `mitm.enabled: false` in that profile (or edit), Override Base URL → `http://localhost:8080/v1` — `cus-` prefix optional for Agent+tools.
4. **Path B text:** keep MITM on + install CA; `agent_rpc_canned_on_error: false`; Ollama down → clear error in Cursor (not silent origin).
5. Bounded context: latest user turn + up to `transform.local_episode_count` compressed episodes.

## Dual-mode proxy (shared harness)

- **Gateway** — BYOK; preferred for **Agent + tools** (`cus-` prefix); stream `tool_calls` to Cursor.
- **MITM** — Path B text Agent (`agent_rpc_fulfill`); `/cloud` hard-force; turn-family sticky (~90s); composer `user_visible_high_level_summary` sticky; subagent sticky; child/tool RunSSE → origin (`tool_followup_would_local`).
- **Routing:** explicit → turn-family sticky → tool_followup → task_classifier → Starlark → token ceiling → default.
- **Observability:** actions include `local`, `cloud`, `origin_passthrough`, `canned`; cloud % counts `origin_passthrough`.
- Docs: [README.md](README.md), [docs/CURSOR_CHECKLIST.md](docs/CURSOR_CHECKLIST.md), [planning/README.md](planning/README.md).

## Remaining / incomplete (P0–P2)

**Authoritative:** [planning/remaining_gaps.md](planning/remaining_gaps.md) (16-area matrix). Brief:

| Pri | Item |
|-----|------|
| **P0** | None open (matrix). Manual Cursor checklist still useful for Path B text + `/cloud` wrap-up. |
| **P1** | Classifier Rules UI polish; Path B codec UI harden; optional Manager SRP call-site migration |
| **P2** | Path B full ToolCall catalog / live PAT verify (**DEFERRED**); Dashboard DIP; `contextgraph.Default()`; enterprise chargeback/SIEM (**DEFERRED**) |

Also: dashboard not pixel-perfect vs mock; `go test -race` not signed off on Windows (CGO).

## Planning docs

Start at [planning/README.md](planning/README.md). Ignore `planning/Depreceated/**`. Archival: prior_art, debug_findings, intercept methodology (shrunk).
