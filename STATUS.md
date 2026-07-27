# Status

| Area | Package(s) | State |
|---|---|---|
| Gateway + MITM proxy | `internal/api`, `internal/mitm` | Working — dual-mode, CONNECT + TLS forge (real HTTP/2 on both sides), transparent WinDivert redirector (Windows); a failed transparent redirector no longer takes the whole process down |
| Routing | `internal/router` | Working — explicit / sticky / classifier / Starlark / token-ceiling chain |
| Cross-CLI delegation | `internal/vendors`, `internal/ngl` | Working for claude / cursor-agent / agy — headless run, denial detection, permission relay, resume, `:interactive` hand-off to a real native session, clean (non-raw) reply rendering by default |
| Native dashboard shell | `internal/webviewshell`, `internal/tray` | Working (Windows) — dashboard opens in a native window (WebView2) from the tray's "Open Dashboard" item instead of requiring a browser tab |
| Backends + registry | `internal/backend` | Working — Ollama, vLLM, OpenAI, Anthropic |
| Config hot-reload | `internal/config`, `backend.Reloader` | Working — rules/aliases/threshold/log/backends live; ports/MITM CA/hosts need restart |
| Context graph | `internal/contextgraph`, `internal/contextkit` | Working — turn-family sticky, episode summaries, entity/community index |
| Tools + MCP | `internal/tools`, `internal/mcp` | Working as a library (builtins, GitHub MCP, dashboard-exposed); no active caller of the agentic tool loop since the hoop/swarm runner was removed |
| Dashboard | `internal/dashboard` | Working — Overview, MCP, Rules Engine, Config, Vendors, VRAM & Models, Workspace tabs; binds to `127.0.0.1` only |
| Orchestrator | `internal/orchestrator` | Working — fan-out executor (flag-gated), VRAM allocation, fallback chain |
| System tray | `internal/tray` | Working (Windows); no-op stub elsewhere |
| VRAM | `internal/vram` | Working (nvidia-smi) |

## What changed recently

The v1 CLI-interop pass removed the old `internal/loop`/`internal/swarm` hoop-and-swarm orchestration runtime (declarative YAML stages, HITL gates, graph editor UI) in favor of a simpler model: Glider routes/intercepts CLI traffic and can *delegate* a task to another real CLI (which brings its own agent loop and tool execution), rather than running its own. `internal/tools`/`internal/mcp` remain as a general-purpose registry — still live, still tested, just not currently wired into a runtime loop.

A 2026-07-28 pass added `internal/ngl`'s `OriginAdapter`/`DelegateRenderer` interfaces (see [docs/site/ngl.html](docs/site/ngl.html)), a native dashboard window, and a security/reliability audit — see [Security & reliability notes](#security--reliability-notes) below for what that audit found and fixed.

## How to run

```bash
# Windows: -H=windowsgui suppresses the console window a plain build
# would otherwise pop up behind the tray icon on every launch — Glider is
# a background tray app, not a CLI, so it has no console output anyone's
# meant to watch live (logs still work the same either way: text-format
# to stdout, redirect to a file if you want them).
go build -ldflags="-H=windowsgui" -o glider.exe ./cmd/glider
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
| Transparent interception | Windows/WinDivert only. Live-verified end-to-end with an unmodified `claude -p`; cursor-agent's/agy's own wire shapes are now confirmed (`internal/ngl`'s `OriginAdapter`s, `planning/agent_cli_interop.md`) and the MITM proxy itself gained real HTTP/2 support (2026-07-28, needed for cursor-agent's h2-only completion host) — but a full live pass through Glider's *own* transparent redirector against cursor-agent/agy specifically (as opposed to the standalone `tools/wirecapture` used for wire-format research) hasn't been re-run since that fix |
| `pidNameCache` (transparent redirector) | Per-PID process-name cache with no eviction — bounded by how many distinct PIDs connect through the redirector over the process's lifetime, unbounded in principle on very long uptimes. Low severity (tiny per-entry cost); `flows` (the higher-traffic map) already has TTL-based sweeping, this one doesn't yet |
| Dashboard | Functional, not pixel-matched to any mock |

See [planning/README.md](planning/README.md) for design-doc detail on each of these.

## Security & reliability notes

A 2026-07-28 audit (secrets-in-logs, injection surface, file-write safety, unbounded state) found and fixed:

| Finding | Fix |
|---|---|
| `configs/glider.yaml` (the default profile `README.md`/this doc point at) shipped with `log_level: debug` and `debug_agent_rpc: true` — the latter dumps a preview of every intercepted request/response to disk indefinitely, no rotation | Both switched to production-sane defaults (`info` / `false`), matching `glider.cloud.yaml`/`glider.local.yaml`'s already-correct values. Opt back in per-session with `GLIDER_MITM_DEBUG_RPC=1` rather than flipping the config permanently |
| Interactive CLI launch (`:interactive` delegate flag) routed a human-typed prompt through `cmd.exe /C start "title" ...` — cmd.exe re-parses its whole reconstructed command line for `&`\|`%VAR%` even across argument boundaries a caller believes are separate, a real (if narrow) injection surface for untrusted chat text | Launches the target binary directly via `CreateProcess` (`CREATE_NEW_CONSOLE`), bypassing cmd.exe's parser entirely — no shell ever re-interprets the prompt. Costs the custom window title cmd's `start` used to set (cosmetic only) |
| `agy_grant.go`'s permission-grant/revert cycle and `permissions.go`'s project-settings writer both wrote the user's **real, external** agy config files via plain `os.WriteFile` (truncate-then-write) — a crash mid-write corrupts a file Glider can't help recover. `vendors.SaveRegistry` (Glider's own state) had the same gap | New `internal/atomicfile` package: write-to-temp-in-same-dir, then rename — never leaves the target partially written. Used by all three call sites |
| `defaultResumeStore` (pending permission-grant tokens) only ever evicted an entry when someone replied to that specific token — a denial the human never answers stayed in memory forever on a long-running process | `RegisterPendingResume` now opportunistically sweeps expired entries on every call — bounded by the map's own natural growth rate, no separate goroutine needed |
| Every `exec.Command` child process (nvidia-smi polling every 5s, delegate headless runs, MCP stdio servers, vendor discovery probes) started flashing its own console window once `glider.exe` began building as a GUI-subsystem binary (`-H=windowsgui`, needed to stop glider.exe's own console from appearing) | New `internal/procutil.HideWindow` sets `CREATE_NO_WINDOW`; wired into every subprocess-spawn site |

**Known, accepted (not fixed this pass):** the dashboard API has no authentication of its own beyond binding to `127.0.0.1` — any other local process running as the same OS user can reach it. Consistent with most local dev-tool dashboards' threat model (matches the machine's own login boundary), but worth a real token check if this ever needs to be more defensible. Sensitive files (`internal/mitm`'s CA private key, the GitHub token, agy settings) are written with restrictive Unix-style mode bits (`0600`), which Go's `os.WriteFile` does **not** translate into an equivalent Windows ACL — on a genuinely shared, multi-user Windows machine another local account could still read them; a single-user personal machine (this tool's primary target so far) isn't exposed to this.

## Tests

```bash
go test ./...
staticcheck ./...   # go install honnef.co/go/tools/cmd/staticcheck@latest
```
