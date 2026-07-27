# Glider — step-by-step setup

Windows-focused; Linux/macOS commands are analogous (`./glider` instead of `.\glider.exe`, transparent interception is Windows-only).

---

## 0. Prerequisites

| Tool | Why |
|---|---|
| **Go 1.22+** (1.25 OK) | Build `cmd/glider` |
| **Git** | Clone this repo |
| **Ollama** | Local inference (default model `qwen2.5-coder:14b`) |
| Optional: **OpenAI / Anthropic API keys** | Cloud fallback / BYOK |
| Optional: **Claude Code / Cursor / agy** | Any CLI you want Glider to route or delegate to |

```powershell
go version
ollama --version
```

---

## 1. Clone and build

```powershell
cd D:\___repos\Glider   # or your clone path
go test ./... -count=1
go build -o glider.exe ./cmd/glider
```

---

## 2. Pull the default local model

```powershell
ollama serve          # if not already running as a service
ollama pull qwen2.5-coder:14b
```

Configs probe `http://127.0.0.1:11434` (prefer `127.0.0.1` over `localhost` on Windows).

---

## 3. Choose a config profile

| Profile | File | Use when |
|---|---|---|
| **Dual mode** (default) | `configs/glider.yaml` | Gateway + MITM + dashboard |
| **Pure local** | `configs/glider.local.yaml` | Ollama only, no cloud fallback |
| **Gateway-oriented cloud** | `configs/glider.cloud.yaml` | Bias toward BYOK cloud |

Optional env file (not committed): copy [`.env.example`](../.env.example) → `.env.local` for API keys. Glider loads dotenv on start when present.

---

## 4. Start Glider

From the **repo root** (so relative config + `docs/site` resolve):

```powershell
.\glider.exe --config configs\glider.yaml
```

On Windows this also puts Glider in the system tray — right-click the icon for **Exit**.

| Port | URL | Role |
|---|---|---|
| **8080** | http://127.0.0.1:8080 | OpenAI-compatible gateway |
| **8081** | http://127.0.0.1:8081 | Dashboard |
| **8082** | MITM proxy | CLI `http.proxy` target (if MITM enabled) |

```powershell
curl http://127.0.0.1:8080/healthz
# → ok
```

Open the dashboard: [http://127.0.0.1:8081](http://127.0.0.1:8081)

---

## 5. Point a CLI at Glider (gateway mode)

Works with any OpenAI-compatible "Override Base URL" setting (Cursor's Models settings, for example):

1. Override Base URL → `http://127.0.0.1:8080/v1`
2. Set a real API key for cloud fallback, or leave it dummy for local-only via model aliases.
3. Model ids: prefix with `cus-` when you want to force the request through Glider instead of the client's built-in routing (e.g. `cus-qwen2.5-coder:14b`).
4. Try `/local …` and `/cloud …` in a prompt — the dashboard **Overview** tab should reflect the route.

Cursor-specific checklist: [`docs/CURSOR_CHECKLIST.md`](CURSOR_CHECKLIST.md).

---

## 6. MITM mode (TLS-decrypt a CLI's own cloud traffic)

Two ways to get a CLI's HTTPS traffic to Glider's MITM proxy (`:8082`):

**Cooperative (any OS):** point the CLI's proxy setting at `http://127.0.0.1:8082` and trust Glider's CA.

1. Trust `~/.glider/mitm/ca.crt` in your OS trust store.
2. Point `NODE_EXTRA_CA_CERTS` at that file for Node/Electron-based CLIs.
3. Configure the CLI's proxy (e.g. Cursor's `settings.json` → `http.proxy`).
4. Fully quit and relaunch the CLI.

**Transparent (Windows only, no CLI cooperation needed):** set `mitm.transparent: true` and `mitm.windivert_dll_path` in config, restart Glider. It redirects outbound HTTPS on the configured ports for allowlisted process names via WinDivert — works retroactively on a CLI session that's already running, no env var or proxy setting required.

Deep dive: [`docs/MITM_NETWORK.md`](MITM_NETWORK.md).

---

## 7. Delegate a task to another CLI

From inside any CLI Glider is routing, a message with a vendor flag hands the prompt to a different installed CLI. The flag goes at the **end** of the message, not the start — some CLIs (Claude Code confirmed) read a leading `/` as their own local slash command and never send the message at all:

```
fix the failing test in internal/foo /claude
refactor this function /cursor-agent
summarize recent commits /agy
```

Glider runs that CLI headless. If it pauses for a permission prompt, Glider relays the prompt back as the reply along with a resume token — reply with the token first, then the flag:

```
<token> /claude:allow
<token> /claude:deny
```

The dashboard's **Vendors** tab lists discovered CLIs, lets you rescan (`configs/vendor_candidates.yaml` defines the candidates), enable/disable one, and set a default workspace directory for delegated runs. Design detail: [`planning/permission_relay_design.md`](../planning/permission_relay_design.md).

---

## 8. GitHub MCP (optional)

1. Dashboard → **MCP** tab.
2. **Sign in with GitHub** (OAuth device flow) or **Paste PAT**.
3. Confirm `source=live` on the tools list once connected.

See [`docs/site/mcp.html`](site/mcp.html) and [`planning/tools.md`](../planning/tools.md).

---

## 9. Config hot-reload vs restart

**Hot** (edit `glider.yaml` while running, or save from dashboard Config): routing, model aliases, context threshold, log level, backend/model clients.

**Restart required:** listen ports, MITM CA/hosts/transparent-interception settings.

Check reload status: dashboard **Config** tab hot-swap module list, or response header `X-Glider-Backend-Reload` after a Config save.

---

## 10. Troubleshooting

| Symptom | Fix |
|---|---|
| Local completions hang or truncate | Raise `thresholds.request_timeout` / `default_max_tokens` |
| Tools write into the Glider repo itself | `orchestration.tools.workspace` should be `~/.glider/workspace`, not `.` |
| MITM Agent+tools broken via Cursor | Use gateway mode for tool-heavy Agent work; the MITM tool codec is opt-in/partial |
| Delegated CLI keeps re-describing an already-granted action instead of doing it | Known limitation for `agy`'s resume path — see `STATUS.md` |
| `mitm.transparent: true` has no effect | Check `windivert_dll_path` points at a real `WinDivert.dll`/`WinDivertNN.sys` pair, and that you're on Windows |

Still stuck: run with `log_level: debug` and check the dashboard's **Overview** tab and process logs.
