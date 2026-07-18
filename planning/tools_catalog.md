# Glider tool catalog

> Overnight **2026-07-19**. Packages: `internal/tools`, `internal/mcp`, `internal/plugin`.  
> Node bindings: `StageSpec.tools` / swarm via shared `tools.Registry`.

## Unified registry

`tools.Registry` dispatches:

| Kind | Source |
|------|--------|
| `builtin` | Standard agent tools (workspace-scoped) |
| `plugin` | `plugin.Registry` ToolProvider |
| `mcp` | `mcp.Client` (Manager + GitHub config) |

## Builtin tools

| Name | Description |
|------|-------------|
| `fs_read` | Read UTF-8 file under workspace |
| `fs_write` | Write file (`path` + newline + content) |
| `fs_list` | List directory |
| `fs_search` | Glob by filename |
| `code_grep` | Substring search in code-like extensions |
| `shell_exec` | Allowlisted shell (disabled by default) |
| `http_fetch` | HTTP GET (optional host allowlist) |
| `git_status` / `git_diff` / `git_log` / `git_clone` | Git audit helpers |
| `context_query` | Shared contextgraph query |
| `datetime` | UTC RFC3339 |
| `calculator` | Simple a+b/a-b/a*b/a/b |

## MCP

- Interfaces: `Client`, `ServerAdapter`, `Session`, `ServerConfig`, `AuthConfig`, `NodeBinding`
- GitHub: `mcp.DefaultGitHubConfig()` → `https://api.githubcopilot.com/mcp/` with `GITHUB_PERSONAL_ACCESS_TOKEN`
- Stdio docker: `mcp.DefaultGitHubStdioConfig()` → `ghcr.io/github/github-mcp-server`
- Live JSON-RPC/stdio transport: TODO (Manager returns documented stubs until wired)

## Plugin lifecycle

`plugin.Plugin`: Register, Init, Start, Stop, Health, Capabilities, Tools (ListTools/CallTool + JSON Schema).

## Node YAML

```yaml
tools:
  - name: git_clone
    kind: builtin
  - name: get_me
    kind: mcp
    server: github
```
