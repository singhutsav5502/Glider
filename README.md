# Glider

**Local-first AI harness for Cursor** — a dual-mode proxy that routes inference to Ollama/vLLM locally or to your own OpenAI/Anthropic keys (BYOK), with an optional MITM path that intercepts Cursor's Agent traffic for selective local fulfillment.

Glider adds intelligent routing, a Loop Engineering runtime (hoops, swarms, parallel fan-out), sandboxed tool execution, MCP integration, and a live dashboard — all without requiring a cloud subscription for local work.

---

## Features

| Category | What it does |
|----------|-------------|
| **Dual-mode proxy** | Gateway (`:8080`) for BYOK base-URL override + MITM (`:8082`) for TLS-intercepted Cursor Agent traffic |
| **Smart routing** | Explicit commands, turn-family sticky, task classifier, Starlark scripts, token ceiling — routes each request to local or cloud |
| **Loop Engineering** | Declarative YAML hoops: planner → actor → critic cycles, HITL gates, parallel fan-out, swarm mode, context seeds, governance budgets |
| **Swarm orchestration** | Multi-worker fan-out with critique-merge, nested swarm runners, wave-based thread coordination |
| **Sandboxed tools** | Built-in filesystem, git, shell, web search, artifact write — all scoped under `~/.glider/workspace/runs/<id>/` |
| **MCP integration** | GitHub tools via device-flow OAuth or PAT; extensible server registry |
| **Dashboard** | Real-time web UI: routing stats, VRAM/GPU, config editor, hoop lifecycle, graph editor, workspace browser, agent logs |
| **Hot-reload** | Edit routing rules, model aliases, backends, thresholds, and log level without restarting |
| **Context graph** | Turn-indexed event store for sticky routing, episode summaries, and analytics |
| **Model aliases** | Map Cursor model IDs (e.g. `gpt-4o`) onto local models (e.g. `qwen2.5-coder:14b`) transparently |

---

## Quick start

> **Full walkthrough:** [docs/SETUP.md](docs/SETUP.md)

### Prerequisites

- **Go 1.22+** — build the binary
- **Ollama** — local inference (`ollama pull qwen2.5-coder:14b`)
- Optional: OpenAI / Anthropic API keys for cloud fallback

### Build and run

```bash
go build -o glider.exe ./cmd/glider
./glider.exe --config configs/glider.yaml
```

### Verify

| Service | URL | Purpose |
|---------|-----|---------|
| Gateway | `http://127.0.0.1:8080` | OpenAI-compatible endpoint |
| Dashboard | `http://127.0.0.1:8081` | Web UI |
| MITM proxy | `127.0.0.1:8082` | Cursor `http.proxy` target |

```bash
curl http://127.0.0.1:8080/healthz
# → ok
```

Open the dashboard at [http://127.0.0.1:8081](http://127.0.0.1:8081).

---

## How it works

```
 Cursor / any client
       │
       ├── Mode A (BYOK): Override Base URL → :8080/v1
       │       → Resolve model alias → Tokenize → Route → Transform → Execute
       │
       └── Mode B (MITM): http.proxy → :8082
               → TLS decrypt allowlisted hosts → same pipeline (or origin passthrough)
```

**Model alias** — Cursor often sends a familiar ID (e.g. `gpt-4o`). Glider’s `model_aliases` map rewrites that to a registry name (e.g. `qwen2.5-coder:14b`) *before* routing, so the IDE keeps its label while inference hits your local or BYOK model.

Both modes share the same completion path (`PipelineCompleter`). That path only answers **which model/backend handles this request**. Loop Engineering (below) defines the **mission shape** — who runs, in what order, what they share, when a human vetoes, and when to stop.

### Routing priority (highest first)

1. Explicit `/local` `/cloud` `/heavy` `/fast` commands
2. Turn-family sticky (Path B follow-on traffic)
3. Tool-step re-decide (`tool_followup`)
4. Task classifier (tools → cloud, simple → local)
5. Starlark scripts
6. Token ceiling
7. Default rule

---

## Configuration

Primary config: [`configs/glider.yaml`](configs/glider.yaml)

| Profile | File | Use case |
|---------|------|----------|
| Dual mode (default) | `configs/glider.yaml` | Gateway + MITM + dashboard |
| Cloud-oriented | `configs/glider.cloud.yaml` | BYOK cloud bias, MITM off |

Cloud keys via env: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`. Copy [`.env.example`](.env.example) → `.env.local` for local credential loading.

**Hot-reload** (no restart): routing rules, model aliases, context threshold, log level, backend URLs/clients.
**Requires restart**: listen ports, MITM CA/hosts.

---

## Loop Engineering (Hoops)

Routing picks a backend for one completion. A **hoop** is the job graph above that: stages (planner → actor → critic), edges (`feeds` / control flow), parallel fan-out or swarm, shared memory (context seed), `human_gate` pauses, and stop conditions (eval score, `max_iterations`, token budgets).

```yaml
stages:
  - { id: seed, kind: context }
  - { id: research, kind: actor }
  - { id: synth, kind: actor, parallel: 2, parallel_mode: fanout }
  - { id: critic, kind: critic }
governance:
  soft_token_limit: 50000
  hard_token_limit: 100000
graph_edges:
  - { source: research, target: synth, kind: feeds }
human_gate: true
max_iterations: 3
```

**Run a sample hoop:**

```bash
go run ./scripts/loadhoop -file samples/hoops/hello-critic.yaml -start
```

Or use the dashboard: **Hoops & Swarm** tab → select a hoop → **Start**.

See [samples/hoops/](samples/hoops/) for enterprise-grade examples (incident command, compliance packs, security audits, research synthesis).

---

## Architecture

| Package | Responsibility |
|---------|---------------|
| `cmd/glider` | Entrypoint — wires all subsystems |
| `internal/api` | OpenAI + Responses gateway, SSE streaming |
| `internal/mitm` | HTTPS MITM forward proxy (CONNECT, CA, Agent RPC fulfill) |
| `internal/backend` | Ollama, vLLM, OpenAI, Anthropic clients |
| `internal/router` | Explicit / classifier / Starlark / tool_followup routing |
| `internal/loop` | Hoop cycles, HITL, context seed, parallel fan-out/swarm, governance |
| `internal/swarm` | FanOut / Merge / multi-wave threads, hot-swap modules |
| `internal/tools` | Builtins (fs/git/web/artifacts) + MCP registry, ScopeRel sandbox |
| `internal/orchestrator` | Lifecycle, queue, fallback chain, rate/budget, VRAM allocation |
| `internal/dashboard` | Embedded web UI + REST API + WebSocket push |
| `internal/contextgraph` | Turn-family event graph (sticky routing + analytics) |
| `internal/config` | YAML load + file-watcher hot-reload |
| `internal/transform` | Tokenizer, context trim/augment |
| `internal/vram` | nvidia-smi GPU monitor + allocation strategy |
| `internal/metrics` | Route/token/cost/latency collector + event bus |
| `internal/mcp` | MCP server management (GitHub HTTP + stdio) |

---

## Documentation

| Resource | Description |
|----------|-------------|
| [docs/SETUP.md](docs/SETUP.md) | Step-by-step install, build, first hoop, Cursor integration |
| [docs/README.md](docs/README.md) | Documentation index |
| [docs/CURSOR_CHECKLIST.md](docs/CURSOR_CHECKLIST.md) | Mode A / Mode B verification checklist |
| [docs/MITM_NETWORK.md](docs/MITM_NETWORK.md) | MITM / Cursor networking (ports, CONNECT, CA, sticky `/cloud`) |
| [docs/site/](docs/site/) | Hostable product docs (architecture, routing, API, samples) |
| [samples/hoops/](samples/hoops/) | Runnable hoop YAML files |
| [planning/README.md](planning/README.md) | Engineering planning index |
| [planning/remaining_gaps.md](planning/remaining_gaps.md) | Feature status matrix (16 areas) |

Serve docs locally:

```bash
# Via dedicated script
powershell -File scripts/serve-docs.ps1

# Or with Glider running (served at dashboard port)
# → http://127.0.0.1:8081/docs/
```

---

## Tests

```bash
go test ./...
go test ./bench -bench=. -benchtime=1s
```

---

## Project status

Glider is functional and actively developed. The [feature matrix](planning/remaining_gaps.md) tracks 16 areas — all core systems are shipped and tested. Key items on the intentional backlog:

- Full Path B ToolCall catalog / live Cursor UI verification (prefer Mode A for Agent+tools)
- Enterprise features (SSO/RBAC, SIEM, Temporal HITL, chargeback billing) — deferred by design
- Dashboard pixel-perfect polish

See [planning/intentional_backlog.md](planning/intentional_backlog.md) for deferred-by-design decisions.

---

## Contributing

```bash
# Run tests before submitting changes
go test ./...

# Build
go build -o glider.exe ./cmd/glider
```

---

*Built for developers who want local-first AI coding without giving up cloud quality when it matters.*
