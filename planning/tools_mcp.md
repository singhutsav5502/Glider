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

Never put a PAT in YAML. Use env only:

```bash
export GITHUB_PERSONAL_ACCESS_TOKEN=ghp_...   # or GITHUB_TOKEN / GH_TOKEN
```

**HTTP (hosted Copilot MCP):**

```go
cfg := mcp.DefaultGitHubConfig() // URL https://api.githubcopilot.com/mcp/
mgr.Connect(ctx, cfg)
```

**Docker stdio (official image):**

```bash
docker pull ghcr.io/github/github-mcp-server
# Glider: mcp.DefaultGitHubStdioConfig()
docker run -i --rm -e GITHUB_PERSONAL_ACCESS_TOKEN ghcr.io/github/github-mcp-server
```

Config validation rejects `auth.token` inline secrets; use `token_env`.

## Node binding (dashboard)

Stage edit dialog: Tools JSON + MCP server ids. Persisted on `StageSpec.tools`.

## Governance MVP

Per-hoop `governance:` soft/hard token, latency, cost, MaxRPM, tool denylist. Soft → prefer local / skip optional tools; hard → stop `budget_exceeded`.

Deferred: SSO, RBAC, SIEM hash-chain, Temporal multi-day HITL.
