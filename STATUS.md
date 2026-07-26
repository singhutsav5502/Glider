# Status

| Area | Package(s) | State |
|---|---|---|
| Gateway + MITM proxy | `internal/api`, `internal/mitm` | Working — dual-mode, CONNECT + TLS forge, transparent WinDivert redirector (Windows) |
| Routing | `internal/router` | Working — explicit / sticky / classifier / Starlark / token-ceiling chain |
| Cross-CLI delegation | `internal/vendors`, `internal/ngl` | Working for claude / cursor-agent / agy — headless run, denial detection, permission relay, resume |
| Backends + registry | `internal/backend` | Working — Ollama, vLLM, OpenAI, Anthropic |
| Config hot-reload | `internal/config`, `backend.Reloader` | Working — rules/aliases/threshold/log/backends live; ports/MITM CA/hosts need restart |
| Context graph | `internal/contextgraph`, `internal/contextkit` | Working — turn-family sticky, episode summaries, entity/community index |
| Tools + MCP | `internal/tools`, `internal/mcp` | Working as a library (builtins, GitHub MCP, dashboard-exposed); no active caller of the agentic tool loop since the hoop/swarm runner was removed |
| Dashboard | `internal/dashboard` | Working — Overview, MCP, Rules Engine, Config, Vendors, VRAM & Models, Workspace tabs |
| Orchestrator | `internal/orchestrator` | Working — fan-out executor (flag-gated), VRAM allocation, fallback chain |
| System tray | `internal/tray` | Working (Windows); no-op stub elsewhere |
| VRAM | `internal/vram` | Working (nvidia-smi) |

## What changed recently

The v1 CLI-interop pass removed the old `internal/loop`/`internal/swarm` hoop-and-swarm orchestration runtime (declarative YAML stages, HITL gates, graph editor UI) in favor of a simpler model: Glider routes/intercepts CLI traffic and can *delegate* a task to another real CLI (which brings its own agent loop and tool execution), rather than running its own. `internal/tools`/`internal/mcp` remain as a general-purpose registry — still live, still tested, just not currently wired into a runtime loop.

## How to run

```bash
go build -o glider.exe ./cmd/glider
./glider.exe --config configs/glider.yaml

# Pure local (Ollama only, no cloud keys):
./glider.exe --config configs/glider.local.yaml
```

- Gateway: `http://127.0.0.1:8080/v1`
- Dashboard: `http://127.0.0.1:8081`
- MITM: `:8082` (when enabled)
- History: `~/.glider/history`
- Context: `~/.glider/context` + `/api/context/*`
- Cloud keys optional unless routing to BYOK cloud (`OPENAI_API_KEY` / `ANTHROPIC_API_KEY`)

## Known gaps

| Item | Detail |
|---|---|
| Path B tool-loop fulfill | Text-only local fulfill ships for Cursor's MITM Connect plane; tool-call frames are opt-in/partial (`mitm.agent_rpc_tool_codec`) — prefer the gateway path for Agent+tools |
| `agy` resume prompt nudge | `WrapResumePrompt` did not reliably stop `agy`'s model from *describing* the now-granted action instead of performing it, in live testing — a documented negative result, not a guarantee |
| Transparent interception | Windows/WinDivert only; live-verified end-to-end with an unmodified `claude -p`, but not yet exercised against cursor-agent/agy in that same zero-cooperation mode |
| Dashboard | Functional, not pixel-matched to any mock |

See [planning/README.md](planning/README.md) for design-doc detail on each of these.

## Tests

```bash
go test ./...
```
