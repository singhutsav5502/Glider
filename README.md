# Glider

**A transparent routing and permission-relay layer for AI coding CLIs.** Glider sits between your machine and Claude Code, Cursor Agent, and Antigravity (`agy`) — routing their inference traffic to local models or your own cloud keys, and letting you delegate a task from one CLI to another (including handling that CLI's own permission prompts) from wherever you're already working.

---

## What it does

| Capability | Summary |
|---|---|
| **Dual-mode proxy** | Gateway (`:8080`, OpenAI-compatible BYOK) + HTTPS MITM forward proxy (`:8082`) that decrypts allowlisted CLI-cloud traffic |
| **Transparent OS-level interception** | Windows-only, WinDivert-based: redirects a CLI's outbound HTTPS to Glider without the CLI cooperating (no env var, no proxy setting) — works even on a session that's already running |
| **Smart routing** | Explicit commands, turn-family sticky, task classifier, Starlark scripts, token ceiling — routes each request to local or cloud |
| **Cross-CLI delegation** | Send a prompt from your current CLI to another installed CLI (`/claude`, `/cursor-agent`, `/agy …`); Glider runs it headless, detects when it pauses for a permission prompt, relays that prompt back to you, and resumes on your answer |
| **NGL (Native Glider Language)** | A canonical `Turn`/`Part` envelope every vendor's wire format is translated through, so core routing/delegation code never branches on which CLI is talking |
| **MCP integration** | GitHub tools via device-flow OAuth or PAT; extensible server registry |
| **Dashboard** | Real-time web UI: routing stats, VRAM/GPU, config editor, vendor registry, MCP servers, workspace browser |
| **Hot-reload** | Edit routing rules, model aliases, backends, thresholds, and log level without restarting |
| **Context graph** | Turn-indexed event store for sticky routing and analytics |
| **System tray** | Runs in the background with a right-click Exit (Windows) |

Glider does **not** hardcode behavior for any one CLI in its core routing/delegation code — everything vendor-specific lives behind two adapter boundaries (NGL wire format, `VendorAdapter` execution behavior). See [`planning/adapter_boundary.md`](planning/adapter_boundary.md).

---

## Quick start

> Full walkthrough: [docs/SETUP.md](docs/SETUP.md)

```bash
go build -o glider.exe ./cmd/glider
./glider.exe --config configs/glider.yaml
```

| Service | URL | Purpose |
|---|---|---|
| Gateway | `http://127.0.0.1:8080` | OpenAI-compatible endpoint |
| Dashboard | `http://127.0.0.1:8081` | Web UI |
| MITM proxy | `127.0.0.1:8082` | CLI `http.proxy` target (when MITM enabled) |

```bash
curl http://127.0.0.1:8080/healthz   # → ok
```

On Windows, Glider starts in the system tray — right-click the icon for **Exit**.

---

## How it works

```
CLI (Claude Code / Cursor Agent / agy)
       │
       ├── Gateway mode: Override Base URL → :8080/v1
       │       → resolve model alias → tokenize → route → transform → execute
       │
       └── MITM mode: http.proxy (or transparent WinDivert redirect) → :8082
               → TLS-decrypt allowlisted hosts → same pipeline, or origin passthrough
```

Both modes share one completion path (`PipelineCompleter`) that answers **which model/backend handles this request**. Routing priority (highest first): explicit `/local` `/cloud` `/heavy` `/fast` → turn-family sticky → tool-step re-decide → task classifier → Starlark scripts → token ceiling → default rule.

### Delegation (cross-CLI permission relay)

A message containing a flag like `/claude do X`, `/cursor-agent do Y`, or `/agy do Z` is claimed by a dedicated handler *before* normal routing — it never interferes with ordinary requests. Glider then:

1. Runs the target CLI headless with that prompt (per-vendor `CommandTemplate`, data-driven — see `configs/vendor_candidates.yaml`, editable from the dashboard's **Vendors** tab).
2. Uses that CLI's `VendorAdapter` to detect a permission denial in its output.
3. Relays the denial back to you as the reply, with a one-shot resume token (`/vendor:allow <token>` / `/vendor:deny <token>`).
4. On allow, grants the permission (mechanism is vendor-specific — flag, or, for `agy`, its own settings file) and resumes the same session.

See [`planning/permission_relay_design.md`](planning/permission_relay_design.md) for the full design, including known limits.

---

## Configuration

Primary config: [`configs/glider.yaml`](configs/glider.yaml)

| Profile | File | Use case |
|---|---|---|
| Dual mode (default) | `configs/glider.yaml` | Gateway + MITM + dashboard |
| Pure local | `configs/glider.local.yaml` | Ollama only, no cloud fallback |
| Cloud-oriented | `configs/glider.cloud.yaml` | BYOK cloud bias, MITM off |

Cloud keys via env: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`. Copy [`.env.example`](.env.example) → `.env.local` for local credential loading.

**Hot-reload** (no restart): routing rules, model aliases, context threshold, log level, backend URLs/clients.
**Requires restart**: listen ports, MITM CA/hosts/transparent-interception settings.

---

## Architecture

| Package | Responsibility |
|---|---|
| `cmd/glider` | Entrypoint — wires all subsystems, tray |
| `internal/api` | OpenAI + Responses gateway, SSE streaming |
| `internal/mitm` | HTTPS MITM forward proxy (CONNECT, CA, transparent WinDivert redirector, delegation handler) |
| `internal/vendors` | Vendor registry/discovery, headless CLI execution, permission-relay resume flow, workspace-per-PID resolution |
| `internal/ngl` | Native Glider Language — canonical `Turn`/`Part` envelope + per-vendor wire-format adapters |
| `internal/backend` | Ollama, vLLM, OpenAI, Anthropic clients |
| `internal/router` | Explicit / classifier / Starlark / tool_followup routing |
| `internal/orchestrator` | Lifecycle, queue, fallback chain, rate/budget, VRAM allocation, fan-out |
| `internal/tools` | Builtin tools (fs/git/web/artifacts) + MCP registry, workspace sandbox |
| `internal/dashboard` | Embedded web UI + REST API + WebSocket push |
| `internal/contextgraph` | Turn-family event graph (sticky routing + analytics) |
| `internal/procinfo` | PID/process-name resolution from a TCP connection (origin CLI identification) |
| `internal/tray` | System tray (Windows; no-op elsewhere) |
| `internal/config` | YAML load + file-watcher hot-reload |
| `internal/transform` | Tokenizer, context trim/augment |
| `internal/vram` | nvidia-smi GPU monitor + allocation strategy |
| `internal/metrics` | Route/token/cost/latency collector + event bus |
| `internal/mcp` | MCP server management (GitHub HTTP + stdio) |
| `internal/cursorrpc` | Cursor's Connect-RPC wire format (protobuf helpers, RunSSE encode) |

---

## Documentation

| Resource | Description |
|---|---|
| [docs/SETUP.md](docs/SETUP.md) | Step-by-step install, build, first run, CLI integration |
| [docs/CURSOR_CHECKLIST.md](docs/CURSOR_CHECKLIST.md) | Gateway / MITM verification checklist |
| [docs/MITM_NETWORK.md](docs/MITM_NETWORK.md) | MITM / transparent-interception networking detail |
| [docs/site/](docs/site/) | Hostable product docs (architecture, routing, API) |
| [planning/README.md](planning/README.md) | Design-doc index |

Serve `docs/site/` locally with `powershell -File scripts/serve-docs.ps1`, or via Glider's dashboard at `http://127.0.0.1:8081/docs/`.

---

## Tests

```bash
go test ./...
go test ./bench -bench=. -benchtime=1s
```

---

## Contributing

```bash
go test ./...
go build -o glider.exe ./cmd/glider
```

---

*Route AI coding CLIs to local models when it's cheap, cloud when it matters — and let them ask you for permission without leaving your terminal.*
