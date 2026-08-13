# Glider — step-by-step setup

Windows-focused; Linux commands are analogous (`./glider` instead of `.\glider.exe`). Windows and Linux are both supported, and transparent interception exists on both — WinDivert on Windows, iptables on Linux.

---

## 0. Prerequisites

Most of this is optional. Only the first table is needed to build, and only one
line of the second is needed for the feature most people come for.

### To build

| Tool | Why |
|---|---|
| **Go 1.25+** | `go.mod` declares `go 1.25.0`; an older toolchain refuses the module |
| **Git** | Clone this repo |
| **A C compiler — Windows only** | `cgo` is mandatory on Windows. The tray and the native dashboard window bind to Win32, and `CGO_ENABLED=0` fails with "build constraints exclude all Go files" in `webview_go`. Any gcc works: MSYS2 UCRT64, or TDM-GCC |

Linux needs no C compiler. `tray_other.go` and `webviewshell_other.go` are pure
Go fallbacks, so `CGO_ENABLED=0 go build ./cmd/glider` succeeds there.

```powershell
go version          # want 1.25 or newer
gcc --version       # Windows only
```

### To run

| What you want | What it needs |
|---|---|
| **Delegate a task to another CLI** — the headline feature | At least one other agent CLI on `PATH`: `claude`, `cursor-agent` or `agy`. Glider discovers them; see step 7. **No certificate, and no local model.** |
| Route inference to a local model | **Ollama** (or vLLM), plus a model that fits your GPU — see the note below |
| The dashboard in its own window (Windows) | **WebView2 Runtime**. Windows 11 ships it; some Windows 10 installs do not. Without it the dashboard is still fully usable at <http://127.0.0.1:8081> in any browser |
| Transparent interception (Windows) | **WinDivert** `WinDivert.dll` + `WinDivert64.sys` in `~/.glider/mitm/windivert/`, and **Administrator**. Without the DLL Glider falls back to ordinary MITM |
| Transparent interception (Linux) | **iptables**, and **root** or `CAP_NET_ADMIN` |
| MITM or transparent mode, either OS | Glider's CA trusted by the OS — step 6. **Not needed for delegation or for gateway mode** |
| Cloud fallback / BYOK | An **OpenAI or Anthropic API key** |
| The GitHub MCP server over stdio | **Docker**. The HTTP transport needs no Docker |
| VRAM accounting | **nvidia-smi** — on `PATH` with an NVIDIA GPU. Without it Glider reports VRAM as unmetered and stops limiting local models rather than inventing a size |

> **Fit the model to the card.** Glider reads your real VRAM at startup and
> refuses a model that cannot fit, in about a tenth of a second, rather than
> letting Ollama fail with a CUDA error half a minute later. The default
> `qwen2.5-coder:14b` estimates 9000 MB and needs roughly a 12 GB card. On a
> 4 GB card use something like `qwen2.5-coder:3b`. The Overview page shows the
> measured total and the headroom.

---

## 1. Clone and build

```powershell
cd D:\___repos\Glider   # or your clone path
go test ./... -count=1
# -H=windowsgui: Glider is a background tray app — this stops a console
# window from popping up behind the tray icon on launch.
go build -ldflags="-H=windowsgui" -o glider.exe ./cmd/glider
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

**Transparent (Windows and Linux, no CLI cooperation needed):** set `mitm.transparent: true` in config (on Windows also `mitm.windivert_dll_path`), restart Glider. It redirects outbound HTTPS on the configured ports for allowlisted process names — via WinDivert on Windows, via `iptables` REDIRECT plus `SO_ORIGINAL_DST` on Linux — works retroactively on a CLI session that's already running, no env var or proxy setting required.

Deep dive: [`docs/MITM_NETWORK.md`](MITM_NETWORK.md).

---

## 7. Delegate a task to another CLI

From inside any CLI Glider is routing, a message with a vendor flag hands the prompt to a different installed CLI. The flag goes at the **end** of the message, not the start — some CLIs (Claude Code confirmed) read a leading `/` as their own local slash command and never send the message at all:

```
fix the failing test in internal/foo /claude
refactor this function /cursor-agent
summarize recent commits /agy:headless
```

**Headless or interactive depends on the vendor's default template.** `/claude` and `/cursor-agent` are headless: Glider runs them with no window and returns the final answer to you. `/agy` is **interactive** by default — it opens Antigravity in a new window with your task, and nothing comes back to your session. Use `/agy:headless` for a reply you can act on, and `/claude:interactive` or `/cursor-agent:interactive` to open a window instead.

### Telling Glider which directory to use

A delegate needs a working directory, and Glider cannot read one out of your CLI. The first handoff of a session comes back asking for it. Reply once with the path and the trailing flag, then send the task again:

```
. /workspace
C:/projects/myapp /workspace
```

Glider asks **every session**, even with a default set on the Vendors tab — it cannot see which project a new CLI is in, so it will not assume. A default pre-fills the question; you send it back. That is what stops a second CLI opened in a different project from silently inheriting the first project's directory.

### Permission relay

If a headless run pauses for a permission prompt, Glider relays it back as the reply along with a resume token — reply with the token first, then the flag:

```
<token> /claude:allow
<token> /claude:deny
```

The dashboard's **Vendors** tab lists discovered CLIs, lets you rescan (`configs/vendor_candidates.yaml` defines the candidates), enable/disable one, set a default workspace directory, and switch the reply format between `clean` (the final answer only, the default) and `raw` (the full captured transcript, for debugging). Design detail: [`planning/permission_relay_design.md`](../planning/permission_relay_design.md).

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
| Delegated CLI keeps re-describing an already-granted action instead of doing it | Known limitation for `agy`'s resume path — see `planning/permission_relay_design.md` |
| `mitm.transparent: true` has no effect | Check `windivert_dll_path` points at a real `WinDivert.dll`/`WinDivertNN.sys` pair, and that you're on Windows |

Still stuck: run with `log_level: debug` and check the dashboard's **Overview** tab and process logs.
