# Glider

Local AI harness above Cursor: dual-mode proxy that routes simple work to Ollama/vLLM and otherwise either (A) your OpenAI/Anthropic keys or (B) Cursor’s original upstream via HTTPS MITM.

## Quick start

```bash
go test ./...
go build -o glider.exe ./cmd/glider
./glider.exe --config configs/glider.yaml
```

| Listener | Port | Role |
|----------|------|------|
| OpenAI-compatible **gateway** | `8080` | BYOK Base URL override |
| **MITM** forward proxy | `8082` | Cursor `http.proxy` → inspect / local / origin passthrough |
| Dashboard | `8081` | Overview, VRAM & Models, Rules, Config, Hoops & Swarm, Graph, MCP, Workspace, agent logs |

Cloud keys (gateway cloud path): `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`.

**Local backends:** Ollama alone is enough. Default local model is **`qwen2.5-coder:14b`** (`ollama pull qwen2.5-coder:14b`). Configs probe `http://127.0.0.1:11434` (prefer `127.0.0.1` over `localhost` on Windows). vLLM is optional — uncomment it in `configs/glider.yaml` only if you run a server on `:8001`.

Open the dashboard at [http://localhost:8081](http://localhost:8081) → **Config** to edit the running config (structured form primary; Edit YAML optional). **Workspace** browses `runs/<id>/work` and `out` under the tools sandbox. **MCP** connects GitHub tools. Save writes the config file and hot-reloads routing, model aliases, context threshold, and log level (GPU assignments persist on the same path). Restart Glider after changing listen ports, MITM, backends, or cloud provider registration.

**Hostable docs:** [docs/site/](docs/site/) (Architecture, Routing, Context, Loop Engineering, Sample hoops). Serve with `powershell -File scripts\serve-docs.ps1` or open `docs/site/index.html`. With Glider running from the repo root, docs are also at [http://127.0.0.1:8081/docs/](http://127.0.0.1:8081/docs/).

**Sample Loop Engineering hoops:** [samples/hoops/](samples/hoops/) — `go run ./scripts/loadhoop -file samples/hoops/hello-critic.yaml -start` (Ollama + Glider required).

### Shipped features (easy snippets)

Status authority: [planning/remaining_gaps.md](planning/remaining_gaps.md) (16-area matrix). Snippets below are **SHIPPED** unless marked otherwise.

| Feature | Status | Snippet / API |
|---------|--------|----------------|
| **Context seed** | SHIPPED | Stage `kind: context` → `[context_digest]` ([docs](docs/site/context.html#hoop-seed)) |
| **Feeds edges** (in-hoop) | SHIPPED | `graph_edges: [{ kind: feeds, … }]` → `FEEDS:` ([sample](samples/hoops/feeds-edge-mvp.yaml)); cross-hoop Phase 3 **DEFERRED** |
| **Skills** | SHIPPED | `skill: security-audit` → `skills/…/SKILL.md` (plain-string fallback) |
| **Parallel swarm** | SHIPPED | `parallel: 2` + `parallel_mode: swarm` ([sample](samples/hoops/parallel-swarm-mode.yaml)) |
| **Worktrees** | SHIPPED | `orchestration.loops.worktrees: true` → `runs/<id>/work/wN/` |
| **CapHooks** | SHIPPED | Plugin `hooks` → enter/exit around stages (no-op if none) |
| **Budget chips** | SHIPPED | Hoop `governance:` soft/hard → dashboard spend chips (no chargeback UI — **DEFERRED**) |
| **Episode chips** | SHIPPED | Overview → `GET /api/context/episodes` |
| **Agent-log cursor** | SHIPPED | `GET /api/agent-logs?scope=hoop&id=<id>&after_seq=<n>` |
| **Path B tool codec** (common map) | SHIPPED | Opt-in `mitm.agent_rpc_tool_codec: true` — Read/Grep/Edit/Shell/Glob/Ls/Web* → Cursor oneofs + TruncatedToolCall fallback (`toolcall_map.go`); full Cursor catalog / live UI verify **DEFERRED** — prefer Path A for Agent+tools demos |
| **CycleExecutor** (SOLID extract) | SHIPPED | `cycle_executor.go` (~444 lines) owns completion bodies; Manager thin wrappers; `runCycle` still on Manager |

Not shipped as product features: SSO/RBAC, SIEM hash-chain, Temporal multi-day HITL, chargeback billing, Leiden communities.

```yaml
# feeds + context seed (see samples/hoops/)
stages:
  - { id: seed, kind: context }
  - { id: research, kind: actor }
  - { id: synth, kind: actor, parallel: 2, parallel_mode: fanout }  # or swarm
graph_edges:
  - { source: research, target: synth, kind: feeds }
```

```bash
# Incremental agent logs (dashboard uses this for poll merge)
curl "http://127.0.0.1:8081/api/agent-logs?scope=hoop&id=my-hoop&after_seq=42&limit=50"
curl "http://127.0.0.1:8081/api/context/episodes"
```

---

## Shared harness

Both the gateway (`:8080`) and MITM (`:8082`) run the same pipeline for recognizable chat/Responses bodies:

**Alias → Tokenizer → Router → Transform → Orchestrator/Executor**

| Decision | Gateway (Mode A) | MITM (Mode B) |
|----------|------------------|---------------|
| `target: local` | Ollama / vLLM | Ollama / vLLM |
| `target: cloud` (or non-local) | BYOK OpenAI / Anthropic | **Origin passthrough** to Cursor’s upstream Host (auth intact) |

Unrecognized Cursor-proprietary envelopes always blind-passthrough on MITM.

**Routing priority:** explicit (`/local`, `/cloud`, `/heavy`, `/fast`) → **turn-family sticky** (Path B) → `tool_followup` → **task_classifier** → Starlark → context-token **ceiling** → default. See `configs/glider.yaml` and [planning/README.md](planning/README.md).

**Path A tools:** gateway preserves `tools` / `tool_choice` and tool-loop message fields (`assistant.tool_calls`, `role=tool` + `tool_call_id`). Anthropic-shaped `tool_use` / `tool_result` blocks are normalized to OpenAI. Stream `tool_calls` are re-emitted on Cursor SSE. Ollama/vLLM attach tools best-effort; models that reject tools fall through `FallbackChain` to BYOK cloud (`ToolsUnsupportedError`). Default `routing.task_classifier.tools_force_cloud: true` (allowlisted tools can skip via `tool_followup`). Path B child/tool RunSSE stays origin.

**Analytics:** Overview LOCAL/CLOUD % counts `origin_passthrough` as cloud. Live snapshot: `GET /api/metrics` (`distribution`, `tokens_saved_est`).

---

## Two modes

### Mode A — BYOK gateway (Chat / Ask / Cmd+K)

1. Run Glider.
2. Cursor **Settings → Models**:
   - Enable **OpenAI API Key** (BYOK).
   - **Override OpenAI Base URL** → `http://localhost:8080/v1`
3. Pick a model that uses the OpenAI BYOK path.
4. Optional prompt commands: `/local`, `/cloud`, `/heavy`.

Gateway “cloud” means **your** OpenAI/Anthropic endpoints — not Cursor subscription traffic.

Default-to-cloud profile (no MITM):

```bash
./glider.exe --config configs/glider.cloud.yaml
```

### Mode B — MITM (Agent TLS decrypt + selective harness)

Use this when you want Agent HTTPS to Cursor API hosts (`api2`/`api3`/`api4`/`*.api5.cursor.sh`) to hit Glider first.

**What MITM can do today**

- Decrypt allowlisted hosts and keep subscription Agent working via **origin passthrough** when not fulfilling.
- Route **local** for OpenAI/Responses JSON (`/v1/chat/completions`, `/v1/responses`).
- **Path B text-only Agent:** with `mitm.agent_rpc_fulfill: true`, extract prompts from `BidiAppend` and fulfill correlated `RunSSE` locally (Ollama; leave `agent_rpc_canned_on_error: false` so real backends are preferred). Legacy `AiService/StreamChat*` remains fulfillable too.

**What MITM still cannot do (by default)**

- Full Agent **tool loops** / child RunSSE stay on Cursor origin unless you opt in to `agent_rpc_tool_codec` (common ToolCall map ships; full Cursor catalog + live UI acceptance still **DEFERRED**). For proven Agent+tools, use **Mode A gateway** (Override OpenAI Base URL + `cus-` model prefix). See [planning/cursor_agent_protocol_interception.md](planning/cursor_agent_protocol_interception.md).

**Path B (Agent RPC fulfill + debug)**

```yaml
mitm:
  agent_rpc_fulfill: true          # BidiAppend extract → RunSSE text fulfill
  agent_rpc_canned_on_error: false # prefer Ollama; set true only to dry-run codec
  agent_rpc_tool_codec: false      # SHIPPED opt-in: common ToolCall map + Truncated fallback; full catalog DEFERRED
  debug_agent_rpc: true            # or: GLIDER_MITM_DEBUG_RPC=1
```

Restart Glider, send an Agent chat, then inspect `~/.glider/mitm-debug/` dumps and `GET http://127.0.0.1:8081/api/mitm/debug/recent`.

Other HTTPS (updates, telemetry, non-allowlisted hosts) still goes through the proxy as a **blind tunnel** — Glider does not decrypt it.

1. Run Glider with `mitm.enabled: true` (default in `configs/glider.yaml`).
2. On first start Glider writes a CA to `~/.glider/mitm/ca.crt` (+ `ca.key`).
3. **Trust the CA**
   - Windows: import `ca.crt` into **Trusted Root Certification Authorities** (current user or local machine). Glider is a Cursor-only proxy — putting the CA in Trusted Root does not change normal Windows internet for apps that are not using this proxy.
   - Also set env when launching Cursor: `NODE_EXTRA_CA_CERTS=%USERPROFILE%\.glider\mitm\ca.crt`
4. Cursor `settings.json`:

```json
{
  "http.proxy": "http://127.0.0.1:8082",
  "http.proxySupport": "override",
  "http.proxyStrictSSL": false,
  "cursor.general.disableHttp2": true
}
```

5. Fully quit and relaunch Cursor (not just Reload Window).
6. Use Agent / any Cursor model normally. Watch the dashboard at `http://localhost:8081`.

**Notes**

- Prefer HTTP/1.1 (`disableHttp2`). Some Cursor builds bypass `http.proxy` on HTTP/2 / Agent singleton paths.
- With `agent_rpc_fulfill: true`, **text-only** root Agent turns (BidiAppend → RunSSE) can fulfill locally; **tool loops / child RunSSE stay origin by default** (opt-in `agent_rpc_tool_codec` for common map). Unrecognized envelopes always passthrough.
- Force local with `/local` on chat/Responses bodies (gateway Mode A, or OpenAI-shaped MITM hits). On Path B, `/local` / `/cloud` apply via TipTap extract when present.

---

## Model aliases

In `glider.yaml`:

```yaml
model_aliases:
  "gpt-4o": "qwen2.5-coder:14b"
  "gpt-4o-mini": "llama3:8b-instruct"
```

Applied before routing so Cursor-selected model IDs can map onto local registry names. Explicit routing rule `action.model` still wins after the alias step when a rule matches.

---

## Responses API

Gateway accepts:

- `POST /v1/chat/completions` (standard)
- `POST /v1/responses`
- Responses-shaped JSON posted to `/v1/chat/completions` (Cursor Agent quirk)

---

## Architecture

| Package | Role |
|---------|------|
| `internal/api` | OpenAI + Responses gateway + SSE |
| `internal/mitm` | HTTPS MITM forward proxy (CONNECT, CA, passthrough / local) |
| `internal/backend` | Ollama, vLLM, OpenAI, Anthropic |
| `internal/config` | `glider.yaml` load + hot-reload |
| `internal/router` | Explicit / classifier / Starlark / tool_followup |
| `internal/contextgraph` | Turn-family event graph (sticky + analytics) |
| `internal/swarm` | FanOut / Merge / multi-wave threads / nested hoop `parallel_mode: swarm` |
| `internal/tools` | Builtins (fs/git/web/artifacts) + MCP registry; ScopeRel run layout |
| `internal/loop` | Hoop cycles, HITL resume, context seed, parallel fanout/swarm; `CycleExecutor` completion bodies + `prompt.go` + `CheckGovernance` (**SHIPPED**) |
| `internal/transform` | Tokenizer + opt-in trim/augment |
| `internal/orchestrator` | Lifecycle, queue, fallback, aliases, FanOut |
| `internal/vram` | nvidia-smi monitor + allocation |
| `internal/metrics` | Route/token/cost/latency + event bus |
| `internal/dashboard` | Embedded Web UI + REST + WebSocket |

Feature status matrix: [planning/remaining_gaps.md](planning/remaining_gaps.md). Planning index: [planning/README.md](planning/README.md).

## Tests

```bash
go test ./...
go test ./bench -bench=. -benchtime=1s
```

Manual Cursor checklist: [docs/CURSOR_CHECKLIST.md](docs/CURSOR_CHECKLIST.md).

## Config

- [configs/glider.yaml](configs/glider.yaml) — **intro / full-system demo** (default): MITM on; Agent host allowlist; model_aliases; Starlark + `/local`/`/cloud` overrides; Ollama/vLLM + BYOK placeholders; dashboard. Gateway cloud → BYOK; MITM cloud → origin passthrough.
- [configs/glider.cloud.yaml](configs/glider.cloud.yaml) — gateway default cloud (BYOK), MITM off

Dashboard APIs (selected): `GET|PUT /api/config`, `GET /api/vram`, `GET /api/models`, `GET|POST /api/loops`, `GET /api/workspace`, `GET /api/agent-logs`, `GET|POST /api/mcp/*`, `POST /api/swarm/run`. Full tables: [docs/site/api.html](docs/site/api.html). Session history lives under `~/.glider/history` (one session = one Glider process run). Tools sandbox (default `~/.glider/workspace`) holds `runs/<hoop-or-turn-id>/work` + `out` — see [planning/tools_mcp.md](planning/tools_mcp.md).
