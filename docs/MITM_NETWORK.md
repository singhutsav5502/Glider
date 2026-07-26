# MITM / Cursor networking

How Glider sits on the wire between Cursor and Cursor cloud (and your local/BYOK backends). Setup steps live in [SETUP.md](SETUP.md); verification in [CURSOR_CHECKLIST.md](CURSOR_CHECKLIST.md).

---

## Listeners

Glider binds **all interfaces** (`:port` → `0.0.0.0`). Point Cursor and curl at **`127.0.0.1`**.

| Port | Role | Client URL |
|------|------|------------|
| **8080** | OpenAI-compatible gateway (Path A) | `http://127.0.0.1:8080` / `…/v1` |
| **8081** | Dashboard + embedded docs | `http://127.0.0.1:8081` |
| **8082** | HTTPS MITM forward proxy (Path B) | `http://127.0.0.1:8082` (`http.proxy`) |

Defaults from `configs/glider.yaml` (`server.proxy_port`, `server.dashboard_port`, `mitm.port`). Changing ports or MITM CA/hosts requires a process restart.

---

## Two paths

```
 Cursor
   │
   ├─ Path A (gateway)  Override OpenAI Base URL → http://127.0.0.1:8080/v1
   │                      → alias → route → local / BYOK cloud
   │
   └─ Path B (MITM)     http.proxy → http://127.0.0.1:8082
                          → CONNECT allowlisted hosts → TLS decrypt
                          → Agent RPC fulfill or origin passthrough
```

| | Path A — gateway | Path B — MITM |
|--|------------------|---------------|
| **Cursor config** | Models → Override OpenAI Base URL | `http.proxy` + trust Glider CA |
| **Traffic** | OpenAI / Responses JSON you pointed at Glider | Cursor subscription hosts (`api2`…`api5`) |
| **Auth** | Your `OPENAI_API_KEY` / Anthropic / local | Cursor session cookies / tokens to origin when passthrough |
| **Best for** | Agent + tools (`cus-…` model ids) | Text-only Agent local fulfill; `/cloud` sticky to Cursor origin |
| **Shared** | Same `PipelineCompleter` / routing stack when a request is harness-handled |

---

## CONNECT + TLS forge (Path B)

For each HTTPS CONNECT:

1. Client opens `CONNECT host:443` to the MITM proxy.
2. Proxy replies `200 Connection Established`.
3. **Allowlisted host** → mint a per-host leaf from the Glider CA, complete TLS with the client, read HTTP, then either:
   - **Local handler** (OpenAI-compat or Agent fulfill), or
   - **Origin passthrough** (new TLS to the real host; Cursor auth intact).
4. **Non-allowlisted host** → **blind tunnel** (no decrypt).

```
Cursor ──CONNECT──► Glider :8082
                      │ match hosts?
                      ├─ yes → forge leaf → decrypt → try local / passthrough HTTPS
                      └─ no  → blind TCP tunnel to origin
```

Implementation: `internal/mitm/proxy.go` (`handleCONNECT`, `mitmSession`, `blindTunnel`), leaf mint in `internal/mitm/ca.go`.

---

## Host allowlist

Default `mitm.hosts` (also applied when the list is empty at load time):

| Pattern | Notes |
|---------|--------|
| `api2.cursor.sh` | Primary Agent / Connect plane |
| `api3.cursor.sh` | Allowlisted |
| `api4.cursor.sh` | Allowlisted |
| `*.api5.cursor.sh` | Wildcard; apex `api5.cursor.sh` also matches |

Only these hosts are decrypted. Everything else through the proxy is a blind tunnel.

---

## CA + Cursor client

### CA on disk

| Path | Purpose |
|------|---------|
| `~/.glider/mitm/ca.crt` | Trust this (OS root + Node) |
| `~/.glider/mitm/ca.key` | Private; never share |

Config keys: `mitm.ca_cert`, `mitm.ca_key`. Created on first MITM start via `LoadOrCreateAuthority`.

### Trust + env

1. Install `ca.crt` into the OS trusted root store (Windows: Trusted Root Certification Authorities).
2. Point Node/Electron at the same file so Cursor’s TLS stack accepts forged leaves:

```powershell
$env:NODE_EXTRA_CA_CERTS = "$env:USERPROFILE\.glider\mitm\ca.crt"
```

Prefer a user/system env var so it survives relaunch. Fully quit Cursor after changing proxy or CA.

### `settings.json`

```json
{
  "http.proxy": "http://127.0.0.1:8082",
  "http.proxySupport": "override",
  "http2": { "disabled": true }
}
```

Stay on **HTTP/1.1**: some Cursor builds ignore `http.proxy` on Agent HTTP/2 paths.

---

## Path B Agent fulfill: BidiAppend → RunSSE

Cursor Agent splits **prompt** and **stream** across Connect RPCs:

| Step | RPC (typical) | Glider role |
|------|---------------|-------------|
| 1 | `…/BidiService/BidiAppend` | Extract user/context envelope; `DecideLocal` / explicit `/local` `/cloud`; arm offer |
| 2 | `…/AgentService/RunSSE` | Correlate by request UUID; local text RunSSE **or** wait-timeout → origin |

Hub: `internal/mitm/agent_fulfill_hub.go` (`AgentFulfillHub`). Wait for a late BidiAppend after RunSSE opens defaults to **800ms** (`defaultRunSSEFulfillWait`) so idle/prewarm RunSSE fail-soft to origin quickly.

**Path classification** (`internal/mitm/classify.go`): OpenAI-compat JSON, Agent RPC (Bidi / RunSSE / StreamChat*), Cursor control (analytics, plugins, …), other. Control and unrecognized paths pass through without harness fulfill.

With `mitm.agent_rpc_fulfill: true` (default in dual-mode profile):

- Root text RunSSE can be fulfilled locally when rules say local.
- Child / tool-loop RunSSE stays on **origin** unless `mitm.agent_rpc_tool_codec: true` (opt-in, partial map).

---

## Sticky `/cloud` turn family

`/cloud` (and DecideLocal → cloud) opens a **StickyCloud** family keyed by the root request UUID — not conversation-wide.

| Constant / config | Default | Meaning |
|-------------------|---------|---------|
| `DefaultTurnFamilyTTL` / `routing.turn_family_ttl` | **90s** | Family window for follow-ons (summary, title, chrome) |
| `DefaultCloudPostRunGrace` | **120s** | Extra StickyCloud life after last parent RunSSE closes |
| Deny-local while StickyCloud live | — | Only an allowlisted fresh TipTap user send may re-decide; wrap-ups stay origin |
| Workspace bind | — | `runs/<turn-id>/{work,out}` still associated under the tools sandbox |

StickyLocal works similarly for `/local` families. Explicit `/local` can beat StickyCloud on a fresh TipTap send; StickyLocal does **not** downgrade a live StickyCloud family.

---

## Config knobs

### `mitm` (restart for port / CA / hosts)

| Key | Typical | Notes |
|-----|---------|--------|
| `enabled` | `true` | Dual-mode profile |
| `port` | `8082` | Bind `0.0.0.0:8082` |
| `ca_cert` / `ca_key` | `~/.glider/mitm/ca.{crt,key}` | |
| `hosts` | api2–4 + `*.api5…` | Decrypt allowlist |
| `passthrough_default` | `true` | Prefer origin when not handling |
| `agent_rpc_fulfill` | `true` | BidiAppend → RunSSE text fulfill |
| `agent_rpc_canned_on_error` | `false` | Keep false in production (real Ollama or origin) |
| `agent_rpc_tool_codec` | `false` | Opt-in Path B tool frames; prefer Path A for tools |
| `origin_on_local_error` | default **true** | Hybrid fail-soft to Cursor origin |
| `require_local_healthy` | optional | Gate ArmLocal on live local health |
| `debug_agent_rpc` | dual-mode often `true` | Dumps under `~/.glider/mitm-debug` |

### Env overrides (also `ApplyMITMDebugEnv`)

| Env | Effect |
|-----|--------|
| `GLIDER_MITM_DEBUG_RPC=1` | Force `debug_agent_rpc` |
| `GLIDER_MITM_AGENT_RPC_FULFILL=1` | Force fulfill on |
| `GLIDER_MITM_AGENT_RPC_CANNED=1` | Canned RunSSE on local error |
| `GLIDER_MITM_AGENT_RPC_TOOL_CODEC=1` | Enable tool codec |

### Routing (hot-reloadable)

| Key | Default dual-mode | Notes |
|-----|-------------------|--------|
| `routing.turn_family_ttl` | `90s` | Sticky family TTL |

---

## Key file map

| Path | Responsibility |
|------|----------------|
| `cmd/glider/main.go` | Wires gateway `:8080`, MITM `:8082`, dashboard `:8081`, fulfill hub |
| `internal/mitm/proxy.go` | CONNECT, decrypt session, blind tunnel, origin passthrough |
| `internal/mitm/ca.go` | CA load/create, per-host leaf forge |
| `internal/mitm/hosts.go` | Allowlist matcher (`*` patterns) |
| `internal/mitm/classify.go` | Path kinds (openai / agent_rpc / control / other) |
| `internal/mitm/intercept.go` | Decrypted request → harness / fulfill |
| `internal/mitm/agent_fulfill_hub.go` | BidiAppend↔RunSSE correlation, StickyCloud/Local |
| `internal/api/` | Path A OpenAI + Responses gateway |
| `internal/cursorrpc/` | Connect/protobuf helpers, RunSSE encode |
| `configs/glider.yaml` | Dual-mode defaults |
| `docs/SETUP.md` | Install + Cursor Mode A/B steps |
| `docs/CURSOR_CHECKLIST.md` | Mode A/B verification |

---

## Gaps / known limits

| Topic | Status |
|-------|--------|
| Path B text Agent fulfill | Shipped (`agent_rpc_fulfill`) |
| Path B tool codec | Opt-in; extended ToolCall map ships; live UI + grind/VM/computer_use still Truncated — prefer **Path A** for Agent+tools |
| MITM / listen-port live reload | Not supported; restart required |
| Native Cursor IDE plugin | Out of scope (gateway + MITM only) |
| Full Cursor protocol RE | Non-goal beyond fulfillable RunSSE / mapped tools |
| HTTP/2 via proxy | Fragile; disable HTTP/2 in Cursor |

Tracked in [planning/remaining_gaps.md](../planning/remaining_gaps.md) (§1.8–1.9) and [planning/intentional_backlog.md](../planning/intentional_backlog.md).

---

## Related docs

| Doc | Use |
|-----|-----|
| [SETUP.md](SETUP.md) | Step-by-step install and Cursor Mode A/B |
| [CURSOR_CHECKLIST.md](CURSOR_CHECKLIST.md) | Verification checklist |
| [site/path-a-b.html](site/path-a-b.html) | Product overview of gateway vs MITM |
| [planning/cursor_agent_protocol_interception.md](../planning/cursor_agent_protocol_interception.md) | Protocol notes |
