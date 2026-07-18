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
| Dashboard | `8081` | Overview (sessions), VRAM & Models, Rules Engine, Config |

Cloud keys (gateway cloud path): `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`.

Open the dashboard at [http://localhost:8081](http://localhost:8081) → **Config** tab to edit the running config (structured form primary; Edit YAML optional). Save writes the config file and hot-reloads routing, model aliases, context threshold, and log level (GPU assignments persist on the same path). Restart Glider after changing listen ports, MITM, backends, or cloud provider registration.

---

## Shared harness

Both the gateway (`:8080`) and MITM (`:8082`) run the same pipeline for recognizable chat/Responses bodies:

**Alias → Tokenizer → Router → Transform → Orchestrator/Executor**

| Decision | Gateway (Mode A) | MITM (Mode B) |
|----------|------------------|---------------|
| `target: local` | Ollama / vLLM | Ollama / vLLM |
| `target: cloud` (or non-local) | BYOK OpenAI / Anthropic | **Origin passthrough** to Cursor’s upstream Host (auth intact) |

Unrecognized Cursor-proprietary envelopes always blind-passthrough on MITM.

**Routing priority:** explicit commands (`/local`, `/cloud`, `/heavy`, `/fast`) override everything; then Starlark scripts and context-token thresholds decide; the config default is a low-priority fallback (see `configs/glider.yaml`).

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

### Mode B — MITM (Agent + all Cursor models)

Use this when you want Agent traffic to Cursor API hosts (`api2`/`api3`/`api4`/`*.api5.cursor.sh`) to hit Glider first. If rules say local, Glider serves locally (when the body is OpenAI/Responses-shaped). Otherwise Glider **passthrough to the original Cursor host** with auth intact.

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
- Unrecognized Cursor-proprietary request bodies always **passthrough** — Agent should not break.
- Force local with `/local` in the prompt when the body is chat/completions or Responses-shaped.

---

## Model aliases

In `glider.yaml`:

```yaml
model_aliases:
  "gpt-4o": "codellama:7b"
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
| `internal/router` | Explicit / regex / context / Starlark rules |
| `internal/transform` | Tokenizer + opt-in trim/augment |
| `internal/orchestrator` | Lifecycle, queue, fallback, aliases |
| `internal/vram` | nvidia-smi monitor + allocation |
| `internal/metrics` | Route/token/cost/latency + event bus |
| `internal/dashboard` | Embedded Web UI + REST + WebSocket |

## Tests

```bash
go test ./...
go test ./bench -bench=. -benchtime=1s
```

Manual Cursor checklist: [docs/CURSOR_CHECKLIST.md](docs/CURSOR_CHECKLIST.md).

## Config

- [configs/glider.yaml](configs/glider.yaml) — **intro / full-system demo** (default): MITM on; Agent host allowlist; model_aliases; Starlark + `/local`/`/cloud` overrides; Ollama/vLLM + BYOK placeholders; dashboard. Gateway cloud → BYOK; MITM cloud → origin passthrough.
- [configs/glider.cloud.yaml](configs/glider.cloud.yaml) — gateway default cloud (BYOK), MITM off

Dashboard APIs: `GET|PUT /api/config`, `GET|POST /api/validate`, `GET /api/vram`, `PUT /api/gpu-assignments`, `GET /api/models`, `GET /api/sessions[/{id}[/requests]]`. Config validation uses `ParseConfig` plus optional discovered-model catalog checks. Session history lives under `~/.glider/history` (one session = one Glider process run).
