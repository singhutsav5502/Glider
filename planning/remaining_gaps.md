# Remaining gaps after leftovers overnight (2026-07-19)

Honest leftover list after live MCP transport, LLM tool loop, mid-cycle HITL, swarm SM route, governance budgets, tools/MCP UI, and redundancy collapse.

See also: [leftovers_overnight_plan.md](./leftovers_overnight_plan.md), [tools_mcp.md](./tools_mcp.md).

---

## Shipped this leftovers run

| Track | Status |
|-------|--------|
| Live MCP stdio + Streamable HTTP JSON-RPC | Shipped + tests (pipes + httptest); Manager no longer stub-calls when connected |
| GitHub MCP config (`GITHUB_*` / `GH_TOKEN`) + install notes | Shipped; connect fails soft without token |
| `LocalServer.Serve` stdio outbound | Shipped + pipe test |
| Inline PAT rejected (`auth.token` forbidden) | Shipped validate |
| Agentic tool loop + OpenAI `tools[]` from Catalog | Shipped; hoop stages feed tool results into model |
| Parallel `InvokeAllParallel` | Shipped |
| `shell_exec` via `orchestration.tools.allow_shell` | Shipped (default off) |
| Mid-cycle HITL resume (`MachineCursor`) | Shipped + tests |
| Swarm FanOut via `TopologySwarm` + DecisionRoute progress | Shipped; merge-failure narrative on response + UI |
| Soft/hard token/latency/cost/RPM + tool denylist | Shipped (enforce stop/degrade) |
| Stage tools/MCP editor + edge-kind modal | Shipped |
| Dedicated dashboard MCP tab + `/api/mcp/*` (live Manager) | Shipped |
| nodetools StubMCP removed (alias-only facade) | Shipped |

---

## Truly still blocked / deferred

### External deps (not code stubs)

- [ ] **Live GitHub MCP against real network** — requires operator PAT + docker/network; Glider connects when `GITHUB_TOKEN` / `GITHUB_PERSONAL_ACCESS_TOKEN` / `GH_TOKEN` is set. CI cannot assert live GitHub without secrets.
- [ ] **Hosted Copilot MCP quirks** — session headers / toolset filters may need field tweaks against production once a PAT is available.

### Product decisions (intentionally deferred)

- SSO / RBAC / multi-tenant control plane
- SIEM / hash-chained audit export
- Temporal-class multi-day durable HITL (beyond process-local cursor JSON)
- tree-sitter / codebase knowledge graph
- Slate-style episode thread weaving / dynamic subagent spawn from planner
- Chargeback UI for budgets (spend is tracked; no billing UI)
- Richer `PathSummary` entity graph / separate Fact index persistence

### Minor polish (non-blocking)

- [ ] Stronger empty states for unbound agent log (partial)
- [x] Dedicated docs/site HTML page for tools/MCP (`docs/site/mcp.html`)

---

## How to verify

```powershell
go test ./internal/statemachine/ ./internal/tools/ ./internal/mcp/ ./internal/plugin/ ./internal/contextgraph/ ./internal/loop/ ./internal/swarm/ ./internal/dashboard/ -count=1
# Live GitHub (optional):
# $env:GITHUB_TOKEN="ghp_..."; go run ./cmd/glider -config configs/glider.local.yaml
```
