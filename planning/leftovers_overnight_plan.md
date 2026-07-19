# Leftovers overnight plan (2026-07-19)

> Unattended. Source: [remaining_gaps.md](./remaining_gaps.md) + overnight audit.  
> Rule: **no stubs as deliverable** — real transports, tool→model loop, mid-cycle HITL, SM swarm, enforceable budgets.  
> No push. Checkpoint commits. Final: rewrite remaining_gaps.md to only truly blocked items.

---

## P0 (must ship)

### MCP live transport
- [x] P0-MCP1 Stdio JSON-RPC client (newline-delimited) + initialize / tools/list / tools/call
- [x] P0-MCP2 Streamable HTTP client (POST + Accept json/sse, session header)
- [x] P0-MCP3 Manager wires real sessions; remove stub CallTool payloads when connected
- [x] P0-MCP4 GitHub config: `GITHUB_PERSONAL_ACCESS_TOKEN` | `GITHUB_TOKEN` | `GH_TOKEN`; docker stdio docs
- [x] P0-MCP5 Validate: reject inline PAT in YAML (`token` empty; `token_env` only)
- [x] P0-MCP6 LocalServer.Serve over stdio JSON-RPC (outbound Glider tools)
- [x] P0-MCP7 Tests: pipe Serve + HTTP httptest

### LLM tool use
- [x] P0-TOOL1 OpenAI `tools[]` schema from `tools.Catalog` / node refs
- [x] P0-TOOL2 Agentic loop: model tool_calls → Invoke → inject results → continue until done/budget
- [x] P0-TOOL3 Hoop stages + swarm workers with `tools:` use the loop (results feed prompts)
- [x] P0-TOOL4 Parallel `InvokeAll` + typed results into SM/context
- [x] P0-TOOL5 `shell_exec` enable via config allowlist (default off)

### HITL mid-cycle resume
- [x] P0-HITL1 Persist machine resume cursor (stage index/id, path, partial texts)
- [x] P0-HITL2 Resume continues mid-cycle after gate (not brand-new cycle from stage 0)
- [x] P0-HITL3 Dashboard Approve/Reject → resume path correct; tests

### Swarm SM + route viz
- [x] P0-SW1 FanOut through `statemachine` TopologySwarm (FromSwarmRoles)
- [x] P0-SW2 DecisionRoute / Progress on RunResponse for Cytoscape
- [x] P0-SW3 Merge-failure narrative paint on swarm graph
- [x] P0-SW4 Pending HITL + live route visibility parity with hoop

### Governance budgets MVP
- [x] P0-GOV1 Soft+hard token / latency / cost budgets on hoop (and swarm run)
- [x] P0-GOV2 Soft → degrade (prefer local / skip tools); hard → stop
- [x] P0-GOV3 Rate-limit + tool denylist hooks enforceable on hoop/swarm
- [x] P0-GOV4 Document MVP vs deferred (SSO/RBAC stay deferred)

### Redundancy
- [x] P0-RED1 Collapse/delete nodetools dual path; one `internal/tools`
- [x] P0-RED2 Remove dead stub transports once live works
- [x] P0-RED3 Align hoop/swarm tool + SM entrypoints

### UX
- [x] P0-UX1 Stage edit: tools + MCP binding panel (no alert hacks)
- [x] P0-UX2 Edge-kind picker modal
- [x] P0-UX3 Swarm live route + merge-fail + HITL pending visibility
- [x] P0-UX4 ASCII-safe; don't break hoop create/start/graph

---

## P1 (if time)

- [x] Docs page for tools/MCP install (GitHub docker/http) — `planning/tools_mcp.md`
- [x] Persist Fact index separately (partial) — entities.jsonl + dual-layer Query (see slate_weave_graphify_plan.md)
- [ ] Stronger empty states for unbound agent log

## Explicitly deferred (honest)

- SSO / RBAC / multi-tenant
- SIEM / hash-chained audit
- Temporal-class multi-day durable HITL
- tree-sitter / codebase knowledge graph
- Full Slate dynamic subagent spawn (P0 shipped: durable threads + multi-wave weave)

---

## Verify

```powershell
go test ./internal/statemachine/ ./internal/tools/ ./internal/mcp/ ./internal/plugin/ ./internal/contextgraph/ ./internal/loop/ ./internal/swarm/ ./internal/dashboard/ -count=1
```
