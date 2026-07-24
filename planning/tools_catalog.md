# Glider tool catalog

> Thin index. **Canonical narrative + snippets:** [tools_mcp.md](./tools_mcp.md).  
> Packages: `internal/tools`, `internal/mcp`, `internal/plugin`.  
> Node bindings: `StageSpec.tools` / swarm via shared `tools.Registry`.

## Unified registry

`tools.Registry` dispatches:

| Kind | Source |
|------|--------|
| `builtin` | Standard agent tools (workspace-scoped) |
| `plugin` | `plugin.Registry` ToolProvider |
| `mcp` | Live `mcp.Manager` (stdio + Streamable HTTP JSON-RPC) |

## Builtin tools

| Name | Description |
|------|-------------|
| `fs_read` / `fs_write` / `fs_list` / `fs_search` | Workspace files; bare paths use **ScopeRel** → `runs/<id>/work/…` when a run is active |
| `code_grep` | Substring search in code-like extensions |
| `shell_exec` | Allowlisted shell (disabled by default) |
| `http_fetch` | HTTP GET raw status+body (optional host allowlist) |
| `web_search` | Query → ranked title/url/snippet (Brave/Tavily/SerpAPI/SearXNG/DDG) |
| `web_fetch` | URL → readable text (HTML stripped, size-capped) |
| `git_status` / `git_diff` / `git_log` / `git_clone` | Git helpers; `git_clone` dest uses same ScopeRel as `fs_*` |
| `artifact_write` | Write under `runs/<id>/work` or `…/out` (`kind=work\|out`) |
| `context_query` | Shared contextgraph query (`key=clone_path\|goal\|plan\|file-tree`) |
| `datetime` | UTC RFC3339 |
| `calculator` | Simple a±b / a*b / a/b |

## MCP

- Live transport: stdio + Streamable HTTP (not stubs).
- GitHub HTTP: `mcp.DefaultGitHubConfig()` → `https://api.githubcopilot.com/mcp/`
- GitHub stdio: `mcp.DefaultGitHubStdioConfig()` → `ghcr.io/github/github-mcp-server`
- Auth: env PAT / credential file / dashboard Sign-in — never inline `auth.token`

## Node YAML

```yaml
tools:
  - name: git_clone
    kind: builtin
  - name: web_search
    kind: builtin
  - name: artifact_write
    kind: builtin
  - name: "*"
    kind: mcp
    server: github
```

`name: "*"` is expanded by `ExpandRefs` before any CallTool (never invokes a literal `"*"` tool).
