# Remaining gaps â€” definitive code audit 2026-07-24

> **Authority: code beats this file.** Every status below was verified against `internal/`, `cmd/`, `configs/`, `samples/`, and `planning/solid_refactor.md` on 2026-07-24.  
> Legend: **SHIPPED** = fully implemented + tested Â· **PARTIAL** = works but specific sub-feature missing Â· **STUB** = scaffolding exists, body absent Â· **DEFERRED** = explicit non-goal or enterprise decision

---

## 1. Feature Matrix

### 1.1 Dashboard (all tabs)

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| **Overview tab** â€” session list, CLASS rates, spend chips, episode chips | SHIPPED | `api.go`, `app.js` `loadEpisodes`/`spendChipHTML`/`live-spend` | â€” |
| **VRAM & Models tab** â€” GPU assignment, model pull/list | SHIPPED | `api_config.go`, `handleGetVRAM`, `handlePatchGPUAssignments` | â€” |
| **Rules Engine tab** â€” routing rules editor | SHIPPED | `api_config.go`, `handleGetConfig`/`handlePutConfig`; `panel-rules` in HTML | â€” |
| **Hoops & Swarm tab** â€” swarm run, stage graph, thread list | **PARTIAL (regressed 2026-07-25)** | `swarm_api.go` still wired; `api_loop.go` deleted with `internal/loop` (v1 strip-down) but `index.html` `panel-hoops` / `app.js` hoop UI (~420 lines) were **not** yet updated â€” hoop-side controls in that tab now call a 404'd API | Split the panel: remove hoop DOM/JS, keep swarm, rename tab; verify swarm still works live in-browser |
| **Graph Editor tab** â€” Cytoscape canvas, undo/redo, stage/swarm nodes, wave timeline | SHIPPED | `app.js` history stacks (Ctrl+Z/Y), `panel-graphs`; edge-kind modal; live stage highlight | â€” |
| **MCP tab** â€” server list, connect/disconnect, GitHub PAT/device flow | SHIPPED | `mcp_api.go`, `panel-mcp` HTML, device flow UI | â€” |
| **Workspace tab** â€” run file tree | SHIPPED | `workspace_api.go`, `panel-workspace` HTML | â€” |
| **Config/Settings tab** â€” live config edit + validate | SHIPPED | `api_config.go`, `handleValidate` | â€” |
| **Docs tab** | SHIPPED | `/docs/` served from `DocsDir`; all `docs/site/*.html` linked | â€” |
| **WebSocket live push** | SHIPPED | `/ws`, `metrics.Bus` subscribe, `handleWS` | â€” |
| **Dashboard DIP** â€” concrete `*swarm.Runner` vs narrow interfaces | PARTIAL | File splits done (`api_config/context/loop.go`; `Loops`/`LoopAPI` removed 2026-07-25). `Server` struct still holds concrete `*swarm.Runner`. | Replace fields with handler-group-scoped interfaces (P2, `planning/Depreceated/solid_refactor.md`) |

---

### 1.2 Hoops / Loop Engine — **REMOVED 2026-07-25**

`internal/loop` (CycleExecutor, stages, governance, feeds, schedule, SKILL.md resolution — everything previously tracked in this section) was deleted whole as part of the v1 CLI-interop strip-down (out of scope for that focus; not merged elsewhere). See `planning/native_glider_orchestration.md` and `planning/agent_cli_interop.md` for the current product direction. Cascading cleanup done: `internal/dashboard/api_loop.go`, `LoopConfig`/`HoopLearningConfig` in `internal/config`, `scripts/loadhoop`, `samples/hoops/`. **Not yet done:** the dashboard's `panel-hoops` frontend (see row above) and `docs/site` residual terminology.

---

### 1.3 Swarm — **REMOVED 2026-07-25**

`internal/swarm` (multi-agent fan-out, multi-wave sequencing, weave policies, durable threads, templates, dashboard Swarm tab) was deleted whole, same day as Â§1.2, when v1 scope narrowed to CLI interop + routing/rulesets/analytics only. Two pieces were extracted rather than deleted, since they're routing/rulesets concerns, not swarm-the-product:
- **Fan-out as a routing strategy** (`StrategyFanOut`, `FanOutExecutor`) survives, relocated into `internal/orchestrator/fanout.go` + `fanout_worker.go` — no more `internal/swarm` import.
- **The hot-swap module registry** (`swarm.Registry`/`swarm.Module`, previously mis-homed in the swarm package despite being generic config-reload infra) survives as a new standalone `internal/hotswap` package; the naming-collision note below is now moot since it's no longer `swarm.Registry`.

Multi-wave sequencing, weave policies, durable threads, templates, and the dashboard Swarm/Graph-editor tabs have **no replacement** — they were the product feature being cut, not relocated.

---

### 1.4 Tools / MCP / Web

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Agent tool loop (`RunAgentLoop`) | SHIPPED | `agentloop.go`, `InvokeAllParallel` | â€” |
| OpenAI tools JSON builder | SHIPPED | `registry.go` `OpenAIToolsJSON` | â€” |
| Text tool-call parser | SHIPPED | `parse.go` `ParseTextToolCalls` | â€” |
| Format utilities | SHIPPED | `format.go` `FlattenToolArgs`/`FormatToolResults` | â€” |
| Builtin FS tools (`fs_list`, `fs_read`, `fs_write`, etc.) | SHIPPED | `builtin_fs.go` | â€” |
| Builtin Git tools (`git_clone`, `git_status`, etc.) | SHIPPED | `builtin_git.go` | â€” |
| Builtin Shell (`shell_exec`, gated `allow_shell`) | SHIPPED | `builtin_shell.go` | â€” |
| Builtin Context (`context_query`) | SHIPPED | `builtin_context.go` | â€” |
| Builtin Util | SHIPPED | `builtin_util.go` | â€” |
| Artifact write (`artifact_write`, ScopeRel) | SHIPPED | `artifacts.go`, `builtin_artifact.go`, `runs/<id>/work/` | â€” |
| `web_search` (SerpAPI/Brave + provider chain) | SHIPPED | `builtin_web_search.go`, `.env.example` | â€” |
| `web_fetch` (HTML-to-text) | SHIPPED | `builtin_web_fetch.go` | â€” |
| HTTP helpers (URL/host allowlist) | SHIPPED | `builtin_http_helpers.go` | â€” |
| MCP stdio transport + Manager | SHIPPED | `mcp/stdio.go`, `mcp/manager.go`, tests | â€” |
| MCP streamable HTTP JSON-RPC transport | SHIPPED | `mcp/http_transport.go`, `mcp/jsonrpc.go` | â€” |
| GitHub MCP device flow + PAT UI | SHIPPED | `github_device.go`, `credentials.go`, credential file | â€” |
| GitHub OAuth web flow | SHIPPED | `github_oauth_web.go`, `/oauth/callback` | â€” |
| Hosted Copilot MCP session hardening | SHIPPED | `http_transport.go` — session persist/retry + `X-MCP-Toolsets` + initialize auth refresh + classified 401/403/timeout | — |
| MCP validate / status | SHIPPED | `mcp/validate.go`, `mcp/status.go` | — |
| **Hosted Copilot MCP live PAT verify** | **PARTIAL** | Code hardened; ops checklist in `tools_mcp.md` — **not** production-verified | Requires live Copilot PAT test — ops/infra step, not code |

---

### 1.5 ContextGraph

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Dual-layer store (EventLog + EntityIndex) | SHIPPED | `event_log.go`, `entity_index.go`, `graph.go` facade | â€” |
| Entity kinds (file/dir/symbol/subtask/conflict) | SHIPPED | `entity.go` | â€” |
| File-tree EXTRACTED indexing | SHIPPED | `filetree.go` `IndexFileTree` | â€” |
| Symbol/AST ingest (Go/JS/TS/Python) | SHIPPED | `symbols.go` `SymbolIndexer` | â€” |
| Community MVP + god-nodes + explain/path | SHIPPED | `communities.go`, query filters | â€” |
| QueryOpts (keyword + path + neighborhood + provenance) | SHIPPED | `query_opts.go`, `query.go` | â€” |
| Thread facts (`hoop_context.go`) | SHIPPED | `hoop_context.go`, `thread_facts.go` | â€” |
| Persist to `~/.glider/context/entities.jsonl` | SHIPPED | `entity_index.go` | â€” |
| Dashboard context tab (episodes, turns, export, prune) | SHIPPED | `api_context.go` | â€” |
| **Store global singleton (`Default()`/`SetDefault()`)** | **SHIPPED** | Removed; `cmd/glider` injects `*Store` into hub/pipeline/loop/swarm/dashboard | â€” |
| **Full Leiden communities at repo scale** | **DEFERRED** | Connected-component + god-nodes MVP works; Leiden needs denser graph | Denser EXTRACTED graph + Go dependency ingest |
| **Live tree-sitter grammars on Windows** | **DEFERRED** | `SymbolIndexer` is pragmatic floor (Go/JS/TS/Python work today) | Live tree-sitter deferred |

---

### 1.6 HITL (Human-in-the-Loop)

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| `ask` prompt at stage boundary | SHIPPED | `cycle.go`, HITL test | â€” |
| `on_fail_n` critic safety valve | SHIPPED | `cycle.go`, `hitl_test.go` | â€” |
| `MachineCursor` mid-cycle resume (process-local) | SHIPPED | `governance.go`, `cycle.go` resume path | â€” |
| **Temporal-class multi-day durable HITL** | **DEFERRED** | Process-local `MachineCursor` â‰  workflow engine | Needs Temporal/Cadence |

---

### 1.7 Artifacts / Workspace

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| `artifact_write` + ScopeRel + bare path â†’ `runs/<id>/work/` | SHIPPED | `artifacts.go`, `builtin_artifact.go` | â€” |
| Workspace API (`GET /api/workspace`) | SHIPPED | `workspace_api.go` | â€” |
| Workspace tab in dashboard | SHIPPED | `panel-workspace` HTML, JS | â€” |
| Run file-tree listing | SHIPPED | `workspace_api.go` | â€” |

---

### 1.8 Path A MITM (OpenAI-compat + Agent RPC decode)

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| MITM proxy (CA, host, TLS) | SHIPPED | `ca.go`, `hosts.go`, `proxy.go` | â€” |
| OpenAI `/v1/chat/completions` intercept | SHIPPED | `intercept.go` `handleOpenAI` | â€” |
| Responses API (`/v1/responses`) intercept | SHIPPED | `intercept.go` `isResponses` branch | â€” |
| Anthropic shape normalization | SHIPPED | `api/anthropic_normalize.go` | â€” |
| Agent RPC protobuf decode (Path A) | SHIPPED | `intercept.go` `handleAgentRPC`, `cursorrpc/decode.go` | â€” |
| Debug RPC observer (`/api/mitm/debug/recent`) | SHIPPED | `debug_rpc.go`, `mitm_api` in server | â€” |
| Path classify + metrics | SHIPPED | `classify.go`, `paths.go`, `metrics.Record` | â€” |
| `SurfaceLocalError` for pure-local mode | SHIPPED | `intercept.go` | â€” |
| `LocalHealthy` gate | SHIPPED | `intercept.go` `allowArmLocal` | â€” |

---

### 1.9 Path B MITM (BidiAppend + RunSSE fulfill)

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| BidiAppend extract (`cursorrpc.ExtractBidiCompletionRequest`) | SHIPPED | `cursorrpc/bidi_extract.go`, tests | â€” |
| `AgentFulfillHub` â€” correlate BidiAppend â†” RunSSE | SHIPPED | `agent_fulfill_hub.go` | â€” |
| Turn-family sticky (StickyLocal/StickyCloud TTL) | SHIPPED | `intercept.go` `ShouldStickyCloudOrigin` / `OpenTurnFamily` | â€” |
| Composer-wrapup-origin belt | SHIPPED | `intercept.go` `tryArmComposerWrapupOrigin` | â€” |
| RunSSE text codec fulfill | SHIPPED | `cursorrpc/runsse_codec.go` `HasRunSSETextCodec` | â€” |
| `CannedOnError` / `SurfaceLocalError` for RunSSE | SHIPPED | `intercept.go` `tryRunSSEFulfill` | â€” |
| Tool-loop child RunSSE re-decide (`tool_followup`) | SHIPPED | `intercept.go` `evaluateToolFollowupRunSSE`, `router/tool_followup.go` | â€” |
| **Path B tool codec (extended Cursor ToolCall map)** | SHIPPED | Opt-in `mitm.agent_rpc_tool_codec`; `toolcall_map.go` maps FS/web + Todos/Lints/MCP/SemSearch/Task/Plan/Mode/Exa/… → Cursor oneofs (+ Truncated fallback); inventory in `tools_mcp.md` | Live UI chrome sign-off + grind/VM/computer_use still Truncated; prefer Path A for demos |

---

### 1.10 Backends â€” Ollama / vLLM / Cloud

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Ollama `Complete()` streaming | SHIPPED | `backend/ollama/backend.go` | â€” |
| vLLM `Complete()` streaming | SHIPPED | `backend/vllm/backend.go` | â€” |
| Cloud Anthropic adapter | SHIPPED | `backend/cloud/anthropic.go` | â€” |
| Cloud OpenAI adapter | SHIPPED | `backend/cloud/openai.go` | â€” |
| Backend registry + hot-swap | SHIPPED | `backend/registry.go`, `swarm/hotswap.go` | â€” |
| VRAM allocator + nvidia-smi monitor | SHIPPED | `vram/` package | â€” |
| Orchestrator pipeline (fallback, circuit breaker, rate limit, queue) | SHIPPED | `orchestrator/` package | â€” |
| **Backend live hot-reload without restart** | **SHIPPED (MVP)** | `backend.Reloader` + `Registry.ReplaceAll`; hot-swap `backends` module; in-flight keeps old client; MITM/ports still restart | Full drain/quiesce; MITM/port live reload |

---

### 1.11 Governance / Budgets

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Soft/hard token + latency + cost + RPM enforcement | SHIPPED | `governance.go`, `cycle.go` | â€” |
| Tool denylist | SHIPPED | `governance.go` `denylistHit` | â€” |
| PreferLocalOnSoft | SHIPPED | `governance.go` | â€” |
| BudgetSpend dashboard chips | SHIPPED | `app.js` `spendChipHTML`, `live-spend` | â€” |
| **Chargeback UI / billing product** | **DEFERRED** | Spend tracked; no billing product decision | Enterprise scope |
| **SIEM / hash-chained audit export** | **DEFERRED** | No retention/compliance product decision | Enterprise scope |

---

### 1.12 Plugins / CapHooks / Skills

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Plugin lifecycle (Register/Init/Start/Stop/Health) | SHIPPED | `plugin/plugin.go`, `plugin/registry.go` | â€” |
| `CapTools` / `CapResources` / `CapHooks` / `CapAuth` | SHIPPED | `plugin.go` constants | â€” |
| `HookProvider` interface (`OnStageEnter`/`OnStageExit`) | SHIPPED | `plugin.go` | â€” |
| CapHooks dispatch around stages | SHIPPED | `plugin/registry.go` `HookProviders()`, `cycle.go` `DispatchStageEnter/Exit` | â€” |
| SKILL.md file resolution (path/id/absolute + string fallback) | SHIPPED | `loop/skill.go` | â€” |
| Skill prompt injection | SHIPPED | `loop/skill.go` `FormatSkillPrefix`, wired in `cycle.go` | â€” |

---

### 1.13 Worktrees

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| `isolateParallelWorkers` + `runs/<id>/work/wN` dirs | SHIPPED | `loop/worktree.go` | â€” |
| `git worktree add --detach` (with fallback to subdir) | SHIPPED | `worktree.go` `gitWorktreeAdd` | â€” |
| Feature-flag (`orchestration.loops.worktrees`) | SHIPPED | `config.go`, `runner.go` | â€” |
| `WORKER_ROOT` prompt hint | SHIPPED | `worktree.go` `isolationPromptHint` | â€” |

---

### 1.14 Feeds Edges — REMOVED 2026-07-25

`feeds.go` lived in `internal/loop`; deleted with it (see §1.2). `RelFeeds` was a `contextgraph` edge kind produced only by the loop stage runner — `contextgraph` itself is unaffected, but nothing currently writes `RelFeeds` edges.

---
### 1.15 AgentLog

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Per-scope ring store with `seq` | SHIPPED | `agentlog/store.go` `Store` | â€” |
| `After(afterSeq)` incremental poll cursor | SHIPPED | `store.go` `After` | â€” |
| `GET /api/agent-logs?after_seq=N` | SHIPPED | `dashboard/agent_logs.go` | â€” |
| Per-hoop / per-swarm-run isolation | SHIPPED | `Store.Append` by scope+id | â€” |
| Dashboard incremental merge (upsert by seq) | SHIPPED | `app.js` | â€” |
| Stronger empty states for unbound log | PARTIAL | Basic "no entries" shown; richer empty-state UX still rough | Minor polish; non-blocking |

---

### 1.16 SOLID Debt

| Area | Status | Evidence | Remaining work |
|------|--------|----------|----------------|
| `backend/interfaces.go` split â†’ types/request/interfaces | SHIPPED âœ… | `backend/{types,request,interfaces}.go` | â€” |
| `tools/builtins.go` split â†’ per-family files | SHIPPED âœ… | `builtin_{fs,git,shell,context,util,artifact,web_search,web_fetch,http_helpers}.go` | â€” |
| `swarm/{types,graph}` cohesion fix | SHIPPED âœ… | `GraphContext`/`WaveWorkerOut`/`SubtaskOut` moved to `swarm.go` | â€” |
| `contextgraph.Store` SRP split | SHIPPED âœ… | `EventLog` (`event_log.go`) + `EntityIndex` (`entity_index.go`) | â€” |
| `dashboard/api.go` split by handler group | SHIPPED âœ… | `api_config.go`, `api_context.go`, `api_loop.go` | â€” |
| `tools/agentloop.go` split (parse/format/registry) | SHIPPED âœ… | `parse.go`, `format.go`, `registry.go` | â€” |
| `tools/web.go` split | SHIPPED âœ… | `builtin_web_search.go`, `builtin_web_fetch.go`, `builtin_http_helpers.go` | â€” |
| `loop/eval.go` extract | SHIPPED âœ… | `eval.go` | â€” |
| `LoopGraphSink` interface (DIP) | SHIPPED âœ… | `loop/graph_sink.go`, `runner.go` | â€” |
| **`loop.Manager` god object** â€” `CycleExecutor` bodies | SHIPPED | Bodies on `CycleExecutor`; Manager thin wrappers; `runCycle` still on Manager | Further call-site migration optional |
| **`buildStagePrompt` extraction** | SHIPPED | `loop/prompt.go` | â€” |
| **`CheckGovernance` standalone** | SHIPPED | `governance.go` `CheckGovernance`; thin Manager wrapper | Interface type optional / not required |
| **Dashboard `Server` DIP** | **SHIPPED** | `Loops LoopAPI` + `Swarm SwarmAPI` in `dashboard/deps.go`; concretes at composition root | â€” |
| **`contextgraph.Default()` global** | **SHIPPED** | `Default`/`SetDefault` removed; explicit `*Store` injection only | â€” |
| **`swarm.Registry` â†’ `ModuleRegistry` rename** | **PARTIAL** | Naming collision documented; not renamed (risky refactor) | P3 rename |

---

## 2. Prioritized Remaining Work

### P0 â€” None open
All P0 blockers closed.

---

### P1 â€” `loop.Manager` SRP (optional follow-ups)

**Shipped (2026-07-24 lock-in):** `CycleExecutor` owns completion bodies; `prompt.go` + `CheckGovernance` extracted. `runCycle` orchestration remains on `Manager` (further splits optional).

**Still optional:** migrate `runCycle` call sites to use `m.Exec().Complete*` directly (wrappers already work).

**Test surface:** `loop_test.go`, `governance_test.go`, `hitl_test.go`, `hoop_yaml_test.go`, `caphooks_skill_test.go`, `cycle_executor_test.go`.

---

### P2 â€” Medium debt (can be done in any order)

| # | Item | Effort |
|---|------|--------|
| P2-1 | ~~Dashboard `Server` — narrow `LoopAPI` / `SwarmAPI`~~ | **SHIPPED** |
| P2-2 | ~~`contextgraph.Default()` global removal~~ | **SHIPPED** |
| P2-3 | Extract `swarm.Runner` inline governance check (~20 lines) → `governance.go` | Low |
| P2-4 | Agentlog dashboard empty-state polish | Low (UI-only) |
| P2-5 | Hosted Copilot MCP live PAT verify in prod | Ops: need live PAT |

---

### P3 â€” Low severity / cosmetic

| Item | Notes |
|------|-------|
| `swarm.Registry` â†’ `swarm.ModuleRegistry` rename | Conceptual clarity; P3, skip until touching hotswap |
| `hotswap.go` move to `internal/hotswap/` | Architectural separation; P3 |
| OCP `RegisterBuiltinFactory` for `StandardBuiltins` | Only if tool count grows significantly |
| `config.go` split into per-section files | Only if config grows significantly |

---

### DEFERRED (Enterprise / Non-goals)

> **Deep plans (why / prereqs / acceptance / effort / anti-goals):** [intentional_backlog.md](intentional_backlog.md).

| Feature | Reason |
|---------|--------|
| SSO / RBAC / multi-tenant control plane | Needs IdP + tenancy model; out of local gateway scope |
| SIEM / hash-chained audit export | Needs retention/compliance product decisions |
| Temporal-class multi-day durable HITL | Process-local `MachineCursor` â‰  workflow engine |
| Full Leiden communities at repo scale | Needs denser EXTRACTED graph + Go dependency ingest |
| Live tree-sitter grammars on Windows | Deferred behind pragmatic `SymbolIndexer` |
| Chargeback UI for budgets | No billing product decision |
| Phase 3 feeds edges (cross-hoop / Temporal canvas) | In-hoop MVP ships; full topology deferred |
| MITM / listen-port live reload | Backend clients/models hot-reload shipped; listeners still process-bound |
| Full Path B Cursor ToolCall catalog beyond mapped set / live UI | Extended map ships (see `tools_mcp.md`); live UI + grind/VM/computer_use still Truncated; prefer Path A |
| Native Cursor IDE plugin | Operates as BYOK gateway + MITM proxy only |
| Full Cursor protocol RE beyond Path B text RunSSE | Non-goal |
| Multi-tenant SaaS control plane | Non-goal |
| Non-Go runtimes / live tree-sitter on Windows | SymbolIndexer is the pragmatic floor |

---

## 3. How to verify current state

```powershell
go test ./internal/agentlog/ ./internal/tools/ ./internal/mcp/ ./internal/cursorrpc/ ./internal/mitm/ ./internal/contextgraph/ ./internal/swarm/ ./internal/dashboard/ ./internal/plugin/ ./internal/router/ ./internal/orchestrator/ ./internal/backend/... ./internal/vram/ -count=1

# Run Glider
go run ./cmd/glider -config configs/glider.local.yaml

# Key API endpoints
# Agent logs:   GET /api/agent-logs?scope=hoop&id=<id>&after_seq=<n>
# Workspace:    GET /api/workspace?run=<id>
# Swarm run:    POST /api/swarm/run {"prompt":"...","waves":2,"weave_policy":"conflict_callouts"}
# Thread list:  GET /api/swarm/threads
# Index tree:   POST /api/context/index-tree {"root":".","turn_id":"audit"}
# Communities:  GET /api/context/communities?turn_id=audit
# Episodes:     GET /api/context/episodes?session=<id>&limit=16
# MCP servers:  GET /api/mcp/servers
# Hotswap:      GET /api/hotswap/modules

# Path B (opt-in): mitm.agent_rpc_tool_codec: true  OR  $env:GLIDER_MITM_AGENT_RPC_TOOL_CODEC=1
```

---

## 4. Changelog

- **2026-07-24 (lock-in):** `CycleExecutor` body migration SHIPPED; `prompt.go` + `CheckGovernance` SHIPPED; Path B ToolCall map **extended** (`toolcall_map.go`); Copilot MCP ops checklist + auth refresh harden (live PAT still unverified); worktrees/CapHooks/SKILL/L3/feeds verified
- Prior audit: code-truth feature matrix; promoted CapHooks/worktrees/SKILL/L3/feeds MVP to SHIPPED
