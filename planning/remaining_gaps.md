# Remaining gaps after leftovers overnight (2026-07-19)

Honest leftover list after live MCP transport, LLM tool loop, mid-cycle HITL, swarm SM route, governance budgets, tools/MCP UI, redundancy collapse, dual-layer context + durable multi-wave threads (P0), P1 weave policies + Graphify query depth, **and** P2 free-spawn / LLM critic / symbol ingest / communities / wave timeline.

See also: [leftovers_overnight_plan.md](./leftovers_overnight_plan.md), [tools_mcp.md](./tools_mcp.md), [slate_weave_graphify_plan.md](./slate_weave_graphify_plan.md).

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
| Dual-layer contextgraph (events + entity/edges; EXTRACTED\|INFERRED\|RUNTIME) | Shipped; persist `~/.glider/context/entities.jsonl`; `Query`/`context_query` searches both |
| Durable swarm threads + multi-wave FanOut + concatenate/critic weave | Shipped; `~/.glider/swarm/threads/`; hoop/swarm write thread/wave/episode facts |
| Weave policies (concatenate / role_weighted / critic / conflict_callouts) | Shipped + tests |
| Planner → SubTasks decompose + template waves | Shipped; sample `multi-wave-weave.yaml` |
| Thread List / Resume APIs + dashboard panel | Shipped; per-thread/wave agent logs |
| QueryOpts (keyword + path + neighborhood + provenance) | Shipped; `context_query` filter syntax |
| PathSummary in prod (hoop cycle + wave seed) | Shipped |
| File-tree EXTRACTED indexing (`IndexFileTree` + post-clone + `/api/context/index-tree`) | Shipped + tests |
| Richer entity kinds (file/dir/subtask/conflict/symbol) + contains/conflicts_with/seeds | Shipped |
| Free/dynamic subagent spawn (`free_spawn`, role invent, capped ≤4) | Shipped + tests |
| LLM critic weave (`llm_critic` + Completer CriticFn) | Shipped + tests |
| Symbol/AST EXTRACTED ingest (Go parser + JS/TS/Python regex; SymbolIndexer) | Shipped + tests; post-clone |
| Community MVP + explain/path UX (API + context_query filters) | Shipped + tests |
| Cytoscape durable-thread wave timeline | Shipped (View paints waves→workers→woven) |

---

## Truly still blocked / deferred

### External deps (not code stubs)

- [ ] **Live GitHub MCP against real network** — requires operator PAT + docker/network; Glider connects when `GITHUB_TOKEN` / `GITHUB_PERSONAL_ACCESS_TOKEN` / `GH_TOKEN` is set. CI cannot assert live GitHub without secrets.
- [ ] **Hosted Copilot MCP quirks** — session headers / toolset filters may need field tweaks against production once a PAT is available.

### Still deferred (enterprise / multi-day — why)

- **SSO / RBAC / multi-tenant control plane** — needs IdP + tenancy model; out of local gateway scope
- **SIEM / hash-chained audit export** — needs retention/compliance product decisions
- **Temporal-class multi-day durable HITL** — process-local `MachineCursor` ≠ workflow engine; multi-day pause needs Temporal/Cadence-class durability
- **Full Leiden communities at repo scale** — connected-component MVP + god-nodes ship; Leiden needs denser EXTRACTED graph + dependency
- **Live tree-sitter grammars on Windows** — deferred behind pragmatic `SymbolIndexer` (Go/JS/TS/Python work today)
- **Chargeback UI for budgets** — spend tracked; no billing product

### Minor polish (non-blocking)

- [ ] Stronger empty states for unbound agent log (partial)
- [x] Dedicated docs/site HTML page for tools/MCP (`docs/site/mcp.html`)
- [x] Dual-layer context + weave deep-dive on `docs/site/context.html`

---

## How to verify

```powershell
go test ./internal/statemachine/ ./internal/tools/ ./internal/mcp/ ./internal/plugin/ ./internal/contextgraph/ ./internal/loop/ ./internal/swarm/ ./internal/dashboard/ -count=1
# Live GitHub (optional):
# $env:GITHUB_TOKEN="ghp_..."; go run ./cmd/glider -config configs/glider.local.yaml
# Multi-wave swarm: POST /api/swarm/run {"prompt":"...","waves":2,"weave_policy":"conflict_callouts","decompose":true,"free_spawn":true}
# LLM critic: POST /api/swarm/run {"prompt":"...","waves":2,"weave_policy":"llm_critic"}
# Threads: GET /api/swarm/threads ; POST /api/swarm/threads/{id}/resume {"waves":1}
# Index tree: POST /api/context/index-tree {"root":".","turn_id":"audit"}
# Index symbols: POST /api/context/index-symbols {"root":".","turn_id":"audit"}
# Explain: GET /api/context/explain?turn_id=audit&id=...
# Communities: GET /api/context/communities?turn_id=audit
```
