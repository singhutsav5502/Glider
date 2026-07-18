# Remaining gaps after overnight run (2026-07-19)

Honest leftover list after AI-first SM, HITL, tools/MCP interfaces, shared context, Graphify notes, samples, and UX polish.

---

## Shipped this run

| Track | Status |
|-------|--------|
| AI-first `internal/statemachine` (graph/tree/loop/swarm) | Shipped + tests; hoop cycle walks SM + DecisionRoute |
| HITL `waiting_human` + approve/reject/resume API + dashboard | Shipped + tests |
| Live route viz (path/current/next/waiting paint) | Shipped (Cytoscape + progress fields) |
| Conditional edges (`on_fail`, `escalate`, `conditional`, …) | Shipped normalize + UI cycle |
| `internal/tools` standard builtins + unified registry | Shipped + tests |
| `internal/mcp` Client/ServerAdapter/Auth/GitHub config | Interfaces + Manager stub; live transport TODO |
| `internal/plugin` lifecycle + ToolProvider | Shipped + MemRegistry tests |
| Shared contextgraph Query / RelevancyScore / provenance | Shipped; swarm injects `context_query` into workers |
| Graphify notes | `planning/graphify_context_notes.md` |
| Clone + audit sample | `samples/hoops/clone-repo-security-audit.yaml` + swarm |
| Docs/site + dashboard UX visibility | Partial polish (tokens, HITL, log focus) |

---

## Gaps / TODOs

### MCP (real transport)

- [ ] Stdio JSON-RPC client for `docker run … github-mcp-server`
- [ ] HTTP MCP client against `https://api.githubcopilot.com/mcp/` with live PAT
- [ ] `ServerAdapter.Serve` over stdio/HTTP for exposing Glider tools outbound
- [ ] Dashboard UI to attach MCP server ids to nodes (fields exist in YAML; editor form incomplete)
- [ ] Secrets: never persist PAT in YAML — only `token_env` (documented; enforce in config validate)

### Tools

- [ ] Enable `shell_exec` via config allowlist (default off)
- [ ] Parallel `InvokeAll` + tool result typed channels into SM context
- [ ] Wire Path A OpenAI `tools[]` schema from `tools.Catalog` for hoop LLM stages

### State machine / HITL

- [ ] Mid-cycle resume after `human_gate` (today Resume starts a new cycle)
- [ ] Durable multi-day HITL (Temporal-class) — enterprise deferred
- [ ] Swarm Cytoscape merge failure node labels (CritiqueMerge text exists; UI paint partial)

### Context / Graphify

- [ ] tree-sitter / codebase knowledge graph (explicitly out of scope)
- [ ] Richer `PathSummary` as real entity graph (not event substring)
- [ ] Persist Fact index separately from event ring

### Product / enterprise (deferred)

- SSO / RBAC / multi-tenant
- SIEM / hash-chained audit
- Soft→hard budgets + chargeback UI
- Slate thread weaving
- Dynamic subagent spawn from planner

### UX

- [ ] Edge kind picker modal (today cycles kinds on toggle)
- [ ] Tools/MCP panel on stage edit dialog
- [ ] Stronger empty states for agent log when unbound
- [ ] Docs: dedicated tools/MCP page (catalog is in planning/)

---

## How to verify quickly

```powershell
go test ./internal/statemachine/ ./internal/tools/ ./internal/mcp/ ./internal/plugin/ ./internal/contextgraph/ ./internal/loop/ ./internal/swarm/ ./internal/dashboard/ -count=1
go run ./scripts/loadhoop -file samples/hoops/clone-repo-security-audit.yaml
# Set GITHUB_PERSONAL_ACCESS_TOKEN for live GitHub MCP when transport lands
```

See also: [orchestrator_overnight_plan.md](./orchestrator_overnight_plan.md), [tools_catalog.md](./tools_catalog.md), [graphify_context_notes.md](./graphify_context_notes.md).
