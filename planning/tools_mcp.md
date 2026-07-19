# Tools and MCP

Glider unifies builtins, plugins, and MCP under `internal/tools`. Hoop stages and swarm workers declare `tools:` refs; the registry dispatches and (when wired) feeds results into the model via an agentic tool loop / OpenAI-compatible `tools[]`.

## Builtins

`fs_read`, `fs_write`, `fs_list`, `fs_search`, `code_grep`, `http_fetch`, `git_*`, `context_query`, `datetime`, `calculator`, `shell_exec`.

`shell_exec` is **off by default**. Enable in config:

```yaml
orchestration:
  tools:
    allow_shell: true
    shell_allowlist: ["git", "go", "rg"]
```

## GitHub MCP (live)

Never put a PAT in YAML. Use env only (first non-empty wins):

```bash
export GITHUB_PERSONAL_ACCESS_TOKEN=ghp_...   # preferred
# or GITHUB_TOKEN / GH_TOKEN
```

**HTTP (hosted Copilot MCP)** — server id `github`:

```go
cfg := mcp.DefaultGitHubConfig() // URL https://api.githubcopilot.com/mcp/
mgr.Connect(ctx, cfg)
```

**Docker stdio (official image)** — server id `github-stdio`:

```bash
docker pull ghcr.io/github/github-mcp-server
# Glider: mcp.DefaultGitHubStdioConfig() — connect from dashboard MCP tab
docker run -i --rm -e GITHUB_PERSONAL_ACCESS_TOKEN ghcr.io/github/github-mcp-server
```

Config validation rejects `auth.token` inline secrets; use `token_env`.

At process start Glider `Configure`s both presets and soft-connects HTTP GitHub when a token is present.

## Dashboard MCP UI

Tab **MCP** (`http://127.0.0.1:8081`):

- Server list: id, transport, connected/health, tool count, token yes/no
- Connect / Disconnect / Reconnect / Refresh
- Tools catalog (live when connected; GitHub documented catalog fallback)
- GitHub status card: token configured? HTTP / stdio connected?

APIs (live `internal/mcp.Manager`):

| Method | Path |
|--------|------|
| GET | `/api/mcp/servers` |
| GET | `/api/mcp/servers/{id}` · `.../tools` |
| POST | `/api/mcp/servers/{id}/connect` · `disconnect` · `reconnect` · `refresh` |
| GET | `/api/mcp/github` |

Hostable docs: [`docs/site/mcp.html`](../docs/site/mcp.html), [`docs/site/api.html#mcp`](../docs/site/api.html).

## Node binding (graph editor)

Stage edit dialog:

1. Check MCP servers (+ optional specific tools)
2. Advanced Tools JSON for builtins
3. Persisted on `StageSpec.tools` (wildcards expand to `list_tools` / server bind on create)

Hoops / Graph help panels link to the MCP tab.

## Seed all samples

```powershell
powershell -File scripts\seed-samples.ps1
powershell -File scripts\seed-samples.ps1 -Start
.\scripts\seed-samples.ps1 -Base http://127.0.0.1:8081
```

Loads every top-level YAML under `samples/hoops/` + `samples/swarms/` (hoops via dashboard API; swarms → `~/.glider/hoops`). See [`docs/site/samples.html#seed`](../docs/site/samples.html).

## Governance MVP

Per-hoop `governance:` soft/hard token, latency, cost, MaxRPM, tool denylist. Soft → prefer local / skip optional tools; hard → stop `budget_exceeded`.

Deferred: SSO, RBAC, SIEM hash-chain, Temporal multi-day HITL.
