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
| Delegate replies over cursor-agent's own protocol — real progress 2026-07-29, **still NOT fully working, confirmed by live testing** | Original root cause (confirmed live): `DelegateHandler` used to compute the full reply text via `vendors.ResolveDelegate` *before* ever touching the response writer, so cursor-agent's own HTTP/2 client received zero bytes for the whole delegate run and gave up (`http2: stream closed`). Fixed structurally: `OriginAdapter.WriteReply` now takes `(header string, replyText <-chan string)` instead of a resolved string; `DelegateHandler` runs `ResolveDelegate` in a goroutine; `cursorOriginAdapter` uses a new `cursorrpc.WriteDelegateReplyWithKeepAlive` (heartbeat-first, periodic heartbeat every 10s while waiting). **Live end-to-end retesting after that change (real `cursor-agent -p "... /agy"` under the transparent redirector) found the fix incomplete** and, in the course of that testing, two further real bugs in the encoder itself, both fixed: (1) the final `turn_ended` write and the separate Connect end-stream trailer write raced against cursor-agent's client closing its read side the instant it saw `turn_ended` — routinely lost that race (`http2: stream closed` on the *last* write, not a slow one); fixed by combining both into one `Write` call. (2) The new encoder never wrote any `token_delta` frame at all, unlike the already-proven-working `WriteRunSSETextResponse` (one after `thinking_delta`, one after every `text_delta`) — fixed to match that shape exactly. **Despite both fixes, and despite every write now returning nil error, live testing still shows cursor-agent reporting "Connection lost, reconnecting" and retrying on every attempt** — the delegate header text (`"Delegated to agy:\n\n"`) never once appeared in cursor-agent's own printed output across multiple live runs, meaning the eventual "success" text some runs printed was cursor-agent's own model answering directly (having escaped interception on a later retry), not a delivered delegate reply — a false negative that could easily be mistaken for the fix working if not checked for the header specifically. Root cause of this remaining gap is NOT yet identified; suspects not yet ruled out: an HTTP/2 ALPN or framing mismatch specific to the transparent-redirected TLS-forged connection (untested), or a further wire-shape difference not caught by comparing frame *kinds* alone (byte-level packet capture against a real origin response, not yet done for this path, would be the next step). Regression tests for the two confirmed-and-fixed bugs: `cursorrpc.TestWriteDelegateReplyWithKeepAlive_HeaderArrivesBeforeResultResolves`/`..._SendsPeriodicHeartbeatsWhileWaiting` (the latter now also asserts the full frame-kind sequence matches `WriteRunSSETextResponse`'s). No structural risk of a delegated reply interleaving with the front CLI's own separate output regardless of the above: every inbound connection gets its own goroutine and its own `http.ResponseWriter` (stdlib `net/http.Server`), so two different CLI processes' streams are never on the same writer. |
| Transparent interception — root-caused and fixed 2026-07-28/29, confirmed working live | **Two real, independent bugs, both fixed and now live-verified together** (the delegate routing success above is the proof): (1) `ownerPID`'s single TCP-table refresh-on-miss wasn't enough for a short-lived process's *early* connections — a genuine kernel-side propagation delay between `connect()` and `GetExtendedTcpTable` reflecting it; fixed with a bounded retry (4×, 3ms apart). (2) `*.api5.cursor.sh` (cursor-agent's actual completion host lives under this) is a wildcard pattern, and `resolveAllowHosts` silently skips wildcards when building the WinDivert packet filter's IP allowlist (a wildcard has no single resolvable IP) — meaning traffic to that host never reached Glider's redirect *at the kernel level*, regardless of any application-layer host matching. Fixed by also listing the concrete confirmed subdomain (`agentn.global.api5.cursor.sh`) explicitly, and by logging a startup warning naming any other wildcard patterns silently affected the same way. Separately, a real data race (`redirector.Start()` starting `serveTransparent()`'s Accept loop before knowing whether the redirector itself would succeed, then nilling the same field on failure from a different goroutine) caused a live nil-pointer panic under `go test -race` — reordered so nothing touches the field until success is confirmed. Also fixed: both `cursorOriginAdapter.Matches` and `agyOriginAdapter.Matches` compared `r.Host` directly against a bare hostname suffix, which breaks under transparent interception specifically — the client's own HTTP/2 `:authority` (which `net/http` maps to `r.Host`) is not guaranteed to omit the port the way gateway-path Host headers conventionally do, and cursor-agent's real client does include it (`agentn.global.api5.cursor.sh:443`). New `ngl.HostWithoutPort` strips it before comparing. |
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

**Windows ACL hardening (2026-07-28, follow-up pass):** `0600`/`0700` mode bits alone don't restrict access on Windows the way they do on Unix (NTFS uses ACLs; Go's `os.WriteFile` doesn't translate mode bits into an equivalent ACL). New `internal/fileacl.RestrictToCurrentUser` (shells out to `icacls /inheritance:r /grant:r`, real-tested against actual `icacls`, not mocked) is now applied to the two highest-consequence files: `internal/mitm`'s CA private key (a compromise lets an attacker MITM every host the CA is trusted for) and the GitHub token. Best-effort — a failure logs a warning rather than blocking startup/token save, since the file is already safely written by that point.

**Known, accepted (not hardened this pass):** agy's `settings.json` (permission rules) is *not* ACL-restricted on every write — unlike the CA key/token, it's rewritten on every delegate permission grant *and* revert (so up to twice per resumed call), and `icacls` is a real subprocess spawn; the latency/overhead cost on a hot, user-waiting path outweighs the benefit for a file whose worst-case exposure (another local account reading/editing agy's own tool-permission rules) is much lower-severity than a CA-key or token leak. The dashboard API has no authentication of its own beyond binding to `127.0.0.1` — any other local process running as the same OS user can reach it. Consistent with most local dev-tool dashboards' threat model (matches the machine's own login boundary), but worth a real token check if this ever needs to be more defensible.

## Tests

```bash
go test ./...
staticcheck ./...   # go install honnef.co/go/tools/cmd/staticcheck@latest
```
