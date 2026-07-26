# Tools and MCP

`internal/tools` is Glider's unified tool registry — builtins, plugins
(`internal/plugin`), and MCP servers (`internal/mcp`) dispatch through one
`tools.Registry`, sandboxed to a workspace directory.

> **Current wiring:** the registry is built at startup and fed to the dashboard
> (browse/test tools, manage MCP servers) and to the Path B tool codec
> (`fulfillHub.Tools`, see [cursor_agent_research.md](cursor_agent_research.md)).
> `Registry.RunAgentLoop` (the agentic tool-call loop) still exists and is
> tested, but nothing in `cmd/glider` calls it — the hoop/swarm stage runner
> that used to drive it (`internal/loop`) was removed in the v1 CLI-interop
> strip-down. Today, actual multi-turn agent work happens by *delegating* to
> a real vendor CLI (see [permission_relay_design.md](permission_relay_design.md)),
> not by running Glider's own tool loop.

## Builtins

| Name | Description |
|------|-------------|
| `fs_read` / `fs_write` / `fs_list` / `fs_search` | Workspace files; bare paths resolve through **ScopeRel** (below) |
| `code_grep` | Substring search in code-like extensions |
| `shell_exec` | Allowlisted shell — **disabled by default** |
| `http_fetch` | Raw HTTP GET (status + body); prefer `web_fetch` for reading pages |
| `web_search` | Query → ranked title/url/snippet (Brave/Tavily/SerpAPI/SearXNG/DuckDuckGo, auto-chain) |
| `web_fetch` | URL → readable text (HTML stripped, size-capped), honors `allow_hosts` |
| `git_status` / `git_diff` / `git_log` / `git_clone` | Git helpers; `git_clone` destination goes through ScopeRel too |
| `artifact_write` | Write under `runs/<id>/work` or `runs/<id>/out` (`kind=work\|out`) |
| `context_query` | Query `contextgraph` (`key=clone_path\|goal\|plan\|file-tree`) |
| `datetime` / `calculator` | Utility |

Builtins cannot escape the workspace root (`safeJoin`).

## Workspace + ScopeRel

Default sandbox: `~/.glider/workspace` (`orchestration.tools.workspace` — never set this to `.` unless you want tools reading the Glider source tree itself). Layout when a run id is active:

```
~/.glider/workspace/
  runs/<id>/
    work/   ← clones, scratch, intermediate files
    out/    ← final deliverables
```

**ScopeRel** (`fs_*`, `code_grep`, `git_clone`): bare paths land under the active run's `work/` automatically —

```text
audit-target          →  runs/<id>/work/audit-target
runs/foo/work/x       →  unchanged (already under runs/)
.                     →  runs/<id>/work
```

`shell_exec` is opt-in:

```yaml
orchestration:
  tools:
    allow_shell: true
    shell_allowlist: ["git", "go", "rg"]
    allow_hosts: ["github.com"]   # empty = any host for http_fetch/web_fetch
    web_search:
      provider: auto   # auto|duckduckgo|brave|tavily|serpapi|searxng
      max_results: 5
```

**Web search provider chain** (`auto`): Brave (`BRAVE_SEARCH_API_KEY`/`BRAVE_API_KEY`) → Tavily (`TAVILY_API_KEY`) → SerpAPI (`SERPAPI_KEY`) → SearXNG (`SEARXNG_URL`) → DuckDuckGo HTML (no key). Forcing a specific keyed provider with an empty env returns a clear error rather than inventing results.

Dashboard **Workspace** tab and `GET /api/workspace` browse the same tree:

| Query | Meaning |
|-------|---------|
| `?run=<id>` | `work` + `out` trees for that run |
| `?path=runs/<id>/out` | List a relative path |
| `?file=runs/<id>/out/report.md` | Capped text preview |
| `?diff=1&a=…&b=…` | Unified diff of two paths |

## MCP

GitHub is the built-in MCP server (HTTP, hosted Copilot MCP + Docker stdio); the manager (`internal/mcp.Manager`) supports any additional stdio or Streamable-HTTP server.

**Auth (never inline `auth.token` in config):**

```bash
export GITHUB_PERSONAL_ACCESS_TOKEN=ghp_...   # or GITHUB_TOKEN / GH_TOKEN
```

Or Dashboard **MCP** tab → **Sign in with GitHub** (OAuth device flow — needs `GLIDER_GITHUB_OAUTH_CLIENT_ID` in `.env.local`) or **Paste PAT** (writes `~/.glider/credentials/github_token`, mode 0600). Glider hydrates the token from that credential file at startup if the env is empty.

```go
mcp.DefaultGitHubConfig()      // HTTP, https://api.githubcopilot.com/mcp/
mcp.DefaultGitHubStdioConfig() // ghcr.io/github/github-mcp-server via Docker
```

The Streamable HTTP client persists `Mcp-Session-Id`, sends `MCP-Protocol-Version` + `X-MCP-Toolsets` on follow-ups, and on a 401/403 or session-shaped 400 clears the session, re-resolves the token, and re-`initialize`s once before retrying.

Dashboard **MCP** tab: server list (id, transport, connected/health, tool count, token yes/no), Connect/Disconnect/Reconnect/Refresh, live tool catalog (falls back to a documented catalog when no session is connected).

| Method | Path |
|--------|------|
| GET | `/api/mcp/servers` · `/api/mcp/servers/{id}` · `.../tools` |
| POST | `/api/mcp/servers/{id}/connect` · `disconnect` · `reconnect` · `refresh` |
| GET | `/api/mcp/github` |
| POST | `/api/mcp/github/token` · `/device/start` · `/device/poll` |
| DELETE | `/api/mcp/github/token` |

Hostable reference: [`docs/site/mcp.html`](../docs/site/mcp.html), [`docs/site/api.html#mcp`](../docs/site/api.html).
