# MITM / Cursor networking

How Glider sits on the wire between Cursor and Cursor cloud (and your local/BYOK backends). Setup steps live in [SETUP.md](SETUP.md); verification in [CURSOR_CHECKLIST.md](CURSOR_CHECKLIST.md).

---

## Listeners

Glider binds **all interfaces** (`:port` → `0.0.0.0`). Point clients and curl at **`127.0.0.1`**.

| Port | Role | Client URL |
|------|------|------------|
| **8080** | OpenAI-compatible gateway (Path A) | `http://127.0.0.1:8080` / `…/v1` |
| **8081** | Dashboard + embedded docs | `http://127.0.0.1:8081` |
| **8082** | HTTPS MITM forward proxy (Path B) | `http://127.0.0.1:8082` (`http.proxy`), or transparent redirect (below) |

Defaults from `configs/glider.yaml` (`server.proxy_port`, `server.dashboard_port`, `mitm.port`). Changing ports or MITM CA/hosts requires a process restart.

---

## Two paths

```
 CLI (Claude Code / Cursor Agent / agy)
   │
   ├─ Path A (gateway)  Override Base URL → http://127.0.0.1:8080/v1
   │                      → alias → route → local / BYOK cloud
   │
   └─ Path B (MITM)     http.proxy, or transparent WinDivert redirect → :8082
                          → CONNECT allowlisted hosts → TLS decrypt
                          → local fulfill, delegation, or origin passthrough
```

| | Path A — gateway | Path B — MITM |
|--|------------------|---------------|
| **CLI config** | Override Base URL setting | `http.proxy`, or nothing at all (transparent mode) |
| **Traffic** | OpenAI / Responses / Anthropic JSON you pointed at Glider | The CLI's own cloud plane (`api2`–`api5.cursor.sh`, `api.anthropic.com`, Google's `*.googleapis.com` / `antigravity-unleash.goog` for agy) |
| **Auth** | Your `OPENAI_API_KEY` / Anthropic / local | The CLI's own session credentials, forwarded to origin on passthrough |
| **Best for** | Agent + tools (`cus-…` model ids); works with any CLI | Text-only Cursor Agent local fulfill; `/cloud` sticky; delegation (any vendor); transparent interception is Path B's mechanism |
| **Shared** | Same `PipelineCompleter` / routing stack when a request is harness-handled |

---

## Transparent interception (Windows, WinDivert)

The cooperative forms above require the CLI to be configured to use Glider — a proxy setting, an env var, an Override Base URL. Transparent interception removes that requirement: Glider redirects outbound HTTPS on chosen ports for chosen process names at the OS packet level, so an unmodified CLI (no flags, no env vars, no settings changes) has its traffic intercepted — including a session that was already running before Glider started.

```yaml
mitm:
  transparent: true
  transparent_port: 8083
  transparent_ports: [443]
  windivert_dll_path: ~/.glider/mitm/windivert/WinDivert.dll   # WinDivertNN.sys must sit alongside it
```

The redirector narrows by process name using the same vendor registry the delegation route reads (`internal/vendors`, `configs/vendor_candidates.yaml`) — run discovery from the dashboard's **Vendors** tab first, or transparent mode has no process-based narrowing and only the IP/port filter applies. Implementation: `internal/mitm/redirector_windows.go` (`internal/mitm/redirector_other.go` is a no-op stub on other platforms). Design notes: [`planning/transparent_redirector_design.md`](../planning/transparent_redirector_design.md).

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

`mitm.hosts` in `configs/glider.yaml`:

| Pattern | Vendor | Notes |
|---------|--------|--------|
| `api2.cursor.sh` | Cursor | Primary Agent / Connect plane |
| `api3.cursor.sh` / `api4.cursor.sh` | Cursor | Allowlisted |
| `*.api5.cursor.sh` | Cursor | Wildcard; apex `api5.cursor.sh` also matches |
| `api.anthropic.com` | Claude Code | Confirmed live via TLS-trust of a forged leaf |
| `daily-cloudcode-pa.googleapis.com`, `cloudcode-pa.googleapis.com`, `antigravity-unleash.goog`, `oauth2.googleapis.com`, `www.googleapis.com`, `play.googleapis.com` | agy (Antigravity CLI) | Completion plane + auxiliary calls, all confirmed to honor a redirected connection and trust a forged leaf |

Only these hosts are decrypted; everything else through the proxy is a blind tunnel. This list is hand-maintained today (see the `TODO` in `configs/glider.yaml`) — the intent is to eventually derive it from the vendor registry rather than a static array.

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
| `cmd/glider/main.go` | Wires gateway `:8080`, MITM `:8082`, dashboard `:8081`, fulfill hub, redirector |
| `internal/mitm/proxy.go` | CONNECT, decrypt session, blind tunnel, origin passthrough |
| `internal/mitm/ca.go` | CA load/create, per-host leaf forge |
| `internal/mitm/hosts.go` | Allowlist matcher (`*` patterns) |
| `internal/mitm/classify.go` | Path kinds (openai / agent_rpc / control / other) |
| `internal/mitm/intercept.go` | Decrypted request → harness / fulfill |
| `internal/mitm/redirector_windows.go` | WinDivert transparent redirection (Windows) |
| `internal/mitm/delegate_handler.go` | trailing `<prompt> /vendor-name` flag → cross-CLI delegation, ahead of normal interception |
| `internal/mitm/agent_fulfill_hub.go` | BidiAppend↔RunSSE correlation, StickyCloud/Local (Cursor) |
| `internal/api/` | Path A OpenAI + Responses gateway |
| `internal/cursorrpc/` | Cursor Connect/protobuf helpers, RunSSE encode |
| `internal/vendors/` | Vendor registry, headless CLI execution, delegation/resume flow |
| `configs/glider.yaml` | Dual-mode defaults |
| `docs/SETUP.md` | Install + integration steps |
| `docs/CURSOR_CHECKLIST.md` | Gateway/MITM verification |

---

## Gaps / known limits

| Topic | Status |
|-------|--------|
| Path B Cursor text Agent fulfill | Shipped (`agent_rpc_fulfill`) |
| Path B Cursor tool codec | Opt-in; extended ToolCall map ships; live UI + grind/VM/computer_use still Truncated — prefer **Path A** for Agent+tools |
| MITM / listen-port live reload | Not supported; restart required |
| Full Cursor protocol RE | Non-goal beyond fulfillable RunSSE / mapped tools |
| HTTP/2 via proxy | Fragile; disable HTTP/2 in the CLI when using cooperative MITM mode |
| Transparent interception scope | Windows/WinDivert only; live-verified end-to-end for Claude Code, not yet for cursor-agent/agy in that same zero-cooperation mode |

See [STATUS.md](../STATUS.md) for the current gap list.

---

## Related docs

| Doc | Use |
|-----|-----|
| [SETUP.md](SETUP.md) | Step-by-step install and CLI integration |
| [CURSOR_CHECKLIST.md](CURSOR_CHECKLIST.md) | Verification checklist |
| [site/mitm.html](site/mitm.html) | Product overview of gateway vs MITM, transparent interception |
| [site/delegation.html](site/delegation.html) | Cross-CLI delegation and permission relay |
| [planning/cursor_agent_research.md](../planning/cursor_agent_research.md) | Cursor wire-format research |
| [planning/transparent_redirector_design.md](../planning/transparent_redirector_design.md) | WinDivert design |
| [planning/permission_relay_design.md](../planning/permission_relay_design.md) | Delegation / permission-relay design |
