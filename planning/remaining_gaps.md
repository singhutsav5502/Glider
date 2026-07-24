# Remaining gaps — definitive code audit 2026-07-24

> **Authority: code beats this file.** Every status below was verified against `internal/`, `cmd/`, `configs/`, `samples/`, and `planning/solid_refactor.md` on 2026-07-24.  
> Legend: **SHIPPED** = fully implemented + tested · **PARTIAL** = works but specific sub-feature missing · **STUB** = scaffolding exists, body absent · **DEFERRED** = explicit non-goal or enterprise decision

---

## 1. Feature Matrix

### 1.1 Dashboard (all tabs)

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| **Overview tab** — session list, CLASS rates, spend chips, episode chips | SHIPPED | `api.go`, `app.js` `loadEpisodes`/`spendChipHTML`/`live-spend` | — |
| **VRAM & Models tab** — GPU assignment, model pull/list | SHIPPED | `api_config.go`, `handleGetVRAM`, `handlePatchGPUAssignments` | — |
| **Rules Engine tab** — routing rules editor | SHIPPED | `api_config.go`, `handleGetConfig`/`handlePutConfig`; `panel-rules` in HTML | — |
| **Hoops & Swarm tab** — create/run/stop hoops, swarm run, stage graph, thread list | SHIPPED | `api_loop.go`, `swarm_api.go`, `index.html` `panel-hoops` | — |
| **Graph Editor tab** — Cytoscape canvas, undo/redo, stage/swarm nodes, wave timeline | SHIPPED | `app.js` history stacks (Ctrl+Z/Y), `panel-graphs`; edge-kind modal; live stage highlight | — |
| **MCP tab** — server list, connect/disconnect, GitHub PAT/device flow | SHIPPED | `mcp_api.go`, `panel-mcp` HTML, device flow UI | — |
| **Workspace tab** — run file tree | SHIPPED | `workspace_api.go`, `panel-workspace` HTML | — |
| **Config/Settings tab** — live config edit + validate | SHIPPED | `api_config.go`, `handleValidate` | — |
| **Docs tab** | SHIPPED | `/docs/` served from `DocsDir`; all `docs/site/*.html` linked | — |
| **WebSocket live push** | SHIPPED | `/ws`, `metrics.Bus` subscribe, `handleWS` | — |
| **Dashboard DIP** — concrete `*loop.Manager` / `*swarm.Runner` vs narrow interfaces | PARTIAL | File splits done (`api_config/context/loop.go`). `Server` struct still holds concrete types. | Replace fields with handler-group-scoped interfaces (P2, `solid_refactor.md`) |

---

### 1.2 Hoops / Loop Engine

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Hoop CRUD + lifecycle | SHIPPED | `runner.go` `CreateLoop`, `Start/Stop/Delete`, `LoopState` persist | — |
| Multi-stage cycle execution | SHIPPED | `cycle.go` `runCycle`, `WalkOrder`, stage kinds: planner/actor/critic/memory/context/free | — |
| Parallel actor fan-out + CritiqueMerge | SHIPPED | `cycle.go` `completeParallel`, `fanout.go` | — |
| `parallel_mode: swarm` | SHIPPED | `stages.go`, `context_seed.go`, nested `Runner.Run`/`RunWaves` | — |
| Context seed stage | SHIPPED | `context_seed.go`, `RecordHoopContext` | — |
| StagePrefs (hoop learning L5) | SHIPPED | `hoop.go:103`, `loop_test.go:727` | — |
| Governance — soft/hard token/latency/cost/RPM + tool denylist | SHIPPED | `governance.go` `CheckGovernance` + structs; cycle calls wrapper | — |
| HITL `ask` + `on_fail_n` safety valve | SHIPPED | `cycle.go`, `hitl_test.go` | — |
| MachineCursor mid-cycle resume | SHIPPED | `governance.go` `MachineCursor`, `cycle.go` resume path, tests | — |
| L3 per-stage autonomy gating (`human_gate` / `autonomy`) | SHIPPED | `stages.go` `StageSpec.HumanGate`, `cycle.go` | — |
| MaxLatencyMS stop | SHIPPED | `cycle.go:689`, `loop_test.go:363` | — |
| Schedule (interval + cron) | SHIPPED | `schedule.go` full cron parser | — |
| SKILL.md file resolution | SHIPPED | `skill.go` `ResolveSkillContent`, path/id/absolute, string fallback | — |
| Feeds edges MVP (`kind=feeds`) | SHIPPED | `feeds.go`, `RelFeeds`, `feedsPromptBlock`, WalkOrder skip, `feeds-edge-mvp.yaml` | — |
| Graph edges persisted + Cytoscape live-stage paint | SHIPPED | `spec.go`, `app.js` `stageLiveCurrent`/`stageLiveEdges` | — |
| CycleProgress + mid-cycle stage highlight | SHIPPED | `spec.go`, `runner.go`, dashboard poll | — |
| **`CycleExecutor` body migration** | SHIPPED | `cycle_executor.go` (~444 lines) owns `CompleteOnce` / `CompleteWithTools` / `CompleteParallel*` bodies; Manager keeps thin wrappers | — |
| **Stage prompt builder extraction** | SHIPPED | `prompt.go` — `stagePrompt` + `StagePrompt` | — |
| **GovernanceChecker / CheckGovernance** | SHIPPED | `governance.go` `CheckGovernance` standalone; Manager `checkGovernance` wraps it | Full interface type not needed |
| Feeds Phase 3 (cross-hoop/swarm + Temporal canvas) | DEFERRED | In-hoop MVP ships; `feeds.go` + `RelFeeds` work today | Full hoop↔swarm bidirectional / Temporal topology |

---

### 1.3 Swarm

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Swarm FanOut + DecisionRoute | SHIPPED | `fanout.go`, `run.go`, `swarm.go` | — |
| Multi-wave sequencing | SHIPPED | `waves.go` `RunWaves`/`ResumeThread` | — |
| Weave policies (concatenate / role_weighted / critic / conflict_callouts) | SHIPPED | `merge.go`, tests | — |
| LLM critic weave (`llm_critic`) | SHIPPED | `merge.go`, `loop_test` | — |
| Free/dynamic subagent spawn (`free_spawn`, ≤4) | SHIPPED | `swarm.go` `FreeSpawn`, capped | — |
| Durable threads + `~/.glider/swarm/threads/` | SHIPPED | `thread.go`, `thread_test.go` | — |
| Planner → SubTasks decompose + template waves | SHIPPED | `decompose.go`, `multi-wave-weave.yaml` | — |
| Thread List / Resume APIs + dashboard panel | SHIPPED | `swarm_api.go` `/api/swarm/threads` | — |
| Live progress snapshots | SHIPPED | `live.go` `LiveProgressStore` | — |
| Hotswap module registry + `/api/hotswap/modules` | SHIPPED | `hotswap.go`, `swarm.Registry` | — |
| Weave: `group.go` + `weave.go` decompose | SHIPPED | `group.go`, `weave.go` | — |
| **`swarm.Registry` naming collision** | **PARTIAL** | `swarm.Registry` (hotswap) clashes conceptually with `backend.Registry` + `tools.Registry` | Rename to `swarm.ModuleRegistry`; deferred as P3 |
| **`swarm.Runner` SRP** | **PARTIAL** | `Runner` owns FanOut + governance + wave-seq + live progress. `LiveProgressStore` already in `live.go`; templates in `templates.go`. | Extract remaining governance check (~20 lines) to `governance.go` (P2 SOLID) |

---

### 1.4 Tools / MCP / Web

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Agent tool loop (`RunAgentLoop`) | SHIPPED | `agentloop.go`, `InvokeAllParallel` | — |
| OpenAI tools JSON builder | SHIPPED | `registry.go` `OpenAIToolsJSON` | — |
| Text tool-call parser | SHIPPED | `parse.go` `ParseTextToolCalls` | — |
| Format utilities | SHIPPED | `format.go` `FlattenToolArgs`/`FormatToolResults` | — |
| Builtin FS tools (`fs_list`, `fs_read`, `fs_write`, etc.) | SHIPPED | `builtin_fs.go` | — |
| Builtin Git tools (`git_clone`, `git_status`, etc.) | SHIPPED | `builtin_git.go` | — |
| Builtin Shell (`shell_exec`, gated `allow_shell`) | SHIPPED | `builtin_shell.go` | — |
| Builtin Context (`context_query`) | SHIPPED | `builtin_context.go` | — |
| Builtin Util | SHIPPED | `builtin_util.go` | — |
| Artifact write (`artifact_write`, ScopeRel) | SHIPPED | `artifacts.go`, `builtin_artifact.go`, `runs/<id>/work/` | — |
| `web_search` (SerpAPI/Brave + provider chain) | SHIPPED | `builtin_web_search.go`, `.env.example` | — |
| `web_fetch` (HTML-to-text) | SHIPPED | `builtin_web_fetch.go` | — |
| HTTP helpers (URL/host allowlist) | SHIPPED | `builtin_http_helpers.go` | — |
| MCP stdio transport + Manager | SHIPPED | `mcp/stdio.go`, `mcp/manager.go`, tests | — |
| MCP streamable HTTP JSON-RPC transport | SHIPPED | `mcp/http_transport.go`, `mcp/jsonrpc.go` | — |
| GitHub MCP device flow + PAT UI | SHIPPED | `github_device.go`, `credentials.go`, credential file | — |
| GitHub OAuth web flow | SHIPPED | `github_oauth_web.go`, `/oauth/callback` | — |
| Hosted Copilot MCP session hardening | SHIPPED | `http_transport.go` — session persist/retry + `X-MCP-Toolsets` | — |
| MCP validate / status | SHIPPED | `mcp/validate.go`, `mcp/status.go` | — |
| **Hosted Copilot MCP live PAT verify** | **PARTIAL** | Code hardened (retry, headers); production quirks still unverified without live PAT | Requires live Copilot PAT test — ops/infra step, not code |

---

### 1.5 ContextGraph

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Dual-layer store (EventLog + EntityIndex) | SHIPPED | `event_log.go`, `entity_index.go`, `graph.go` facade | — |
| Entity kinds (file/dir/symbol/subtask/conflict) | SHIPPED | `entity.go` | — |
| File-tree EXTRACTED indexing | SHIPPED | `filetree.go` `IndexFileTree` | — |
| Symbol/AST ingest (Go/JS/TS/Python) | SHIPPED | `symbols.go` `SymbolIndexer` | — |
| Community MVP + god-nodes + explain/path | SHIPPED | `communities.go`, query filters | — |
| QueryOpts (keyword + path + neighborhood + provenance) | SHIPPED | `query_opts.go`, `query.go` | — |
| Thread facts (`hoop_context.go`) | SHIPPED | `hoop_context.go`, `thread_facts.go` | — |
| Persist to `~/.glider/context/entities.jsonl` | SHIPPED | `entity_index.go` | — |
| Dashboard context tab (episodes, turns, export, prune) | SHIPPED | `api_context.go` | — |
| **Store global singleton (`Default()`/`SetDefault()`)** | **PARTIAL** | Global still exists; mostly injected via interfaces but not fully removed | Replace package-level global with explicit injection everywhere (P2 SOLID) |
| **Full Leiden communities at repo scale** | **DEFERRED** | Connected-component + god-nodes MVP works; Leiden needs denser graph | Denser EXTRACTED graph + Go dependency ingest |
| **Live tree-sitter grammars on Windows** | **DEFERRED** | `SymbolIndexer` is pragmatic floor (Go/JS/TS/Python work today) | Live tree-sitter deferred |

---

### 1.6 HITL (Human-in-the-Loop)

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| `ask` prompt at stage boundary | SHIPPED | `cycle.go`, HITL test | — |
| `on_fail_n` critic safety valve | SHIPPED | `cycle.go`, `hitl_test.go` | — |
| `MachineCursor` mid-cycle resume (process-local) | SHIPPED | `governance.go`, `cycle.go` resume path | — |
| **Temporal-class multi-day durable HITL** | **DEFERRED** | Process-local `MachineCursor` ≠ workflow engine | Needs Temporal/Cadence |

---

### 1.7 Artifacts / Workspace

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| `artifact_write` + ScopeRel + bare path → `runs/<id>/work/` | SHIPPED | `artifacts.go`, `builtin_artifact.go` | — |
| Workspace API (`GET /api/workspace`) | SHIPPED | `workspace_api.go` | — |
| Workspace tab in dashboard | SHIPPED | `panel-workspace` HTML, JS | — |
| Run file-tree listing | SHIPPED | `workspace_api.go` | — |

---

### 1.8 Path A MITM (OpenAI-compat + Agent RPC decode)

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| MITM proxy (CA, host, TLS) | SHIPPED | `ca.go`, `hosts.go`, `proxy.go` | — |
| OpenAI `/v1/chat/completions` intercept | SHIPPED | `intercept.go` `handleOpenAI` | — |
| Responses API (`/v1/responses`) intercept | SHIPPED | `intercept.go` `isResponses` branch | — |
| Anthropic shape normalization | SHIPPED | `api/anthropic_normalize.go` | — |
| Agent RPC protobuf decode (Path A) | SHIPPED | `intercept.go` `handleAgentRPC`, `cursorrpc/decode.go` | — |
| Debug RPC observer (`/api/mitm/debug/recent`) | SHIPPED | `debug_rpc.go`, `mitm_api` in server | — |
| Path classify + metrics | SHIPPED | `classify.go`, `paths.go`, `metrics.Record` | — |
| `SurfaceLocalError` for pure-local mode | SHIPPED | `intercept.go` | — |
| `LocalHealthy` gate | SHIPPED | `intercept.go` `allowArmLocal` | — |

---

### 1.9 Path B MITM (BidiAppend + RunSSE fulfill)

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| BidiAppend extract (`cursorrpc.ExtractBidiCompletionRequest`) | SHIPPED | `cursorrpc/bidi_extract.go`, tests | — |
| `AgentFulfillHub` — correlate BidiAppend ↔ RunSSE | SHIPPED | `agent_fulfill_hub.go` | — |
| Turn-family sticky (StickyLocal/StickyCloud TTL) | SHIPPED | `intercept.go` `ShouldStickyCloudOrigin` / `OpenTurnFamily` | — |
| Composer-wrapup-origin belt | SHIPPED | `intercept.go` `tryArmComposerWrapupOrigin` | — |
| RunSSE text codec fulfill | SHIPPED | `cursorrpc/runsse_codec.go` `HasRunSSETextCodec` | — |
| `CannedOnError` / `SurfaceLocalError` for RunSSE | SHIPPED | `intercept.go` `tryRunSSEFulfill` | — |
| Tool-loop child RunSSE re-decide (`tool_followup`) | SHIPPED | `intercept.go` `evaluateToolFollowupRunSSE`, `router/tool_followup.go` | — |
| **Path B tool codec (common Cursor ToolCall map)** | SHIPPED | Opt-in `mitm.agent_rpc_tool_codec`; `toolcall_map.go` maps Read/Grep/Edit/Shell/Glob/Ls/Web* → Cursor oneofs (+ Truncated fallback); tests | Full Cursor catalog / live UI chrome still prefer Path A |

---

### 1.10 Backends — Ollama / vLLM / Cloud

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Ollama `Complete()` streaming | SHIPPED | `backend/ollama/backend.go` | — |
| vLLM `Complete()` streaming | SHIPPED | `backend/vllm/backend.go` | — |
| Cloud Anthropic adapter | SHIPPED | `backend/cloud/anthropic.go` | — |
| Cloud OpenAI adapter | SHIPPED | `backend/cloud/openai.go` | — |
| Backend registry + hot-swap | SHIPPED | `backend/registry.go`, `swarm/hotswap.go` | — |
| VRAM allocator + nvidia-smi monitor | SHIPPED | `vram/` package | — |
| Orchestrator pipeline (fallback, circuit breaker, rate limit, queue) | SHIPPED | `orchestrator/` package | — |
| **Backend live hot-reload without restart** | **DEFERRED** | Only router/aliases/threshold/log/GPU hot-swap today; backend/MITM/ports need restart | Design decision; out of current scope |

---

### 1.11 Governance / Budgets

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Soft/hard token + latency + cost + RPM enforcement | SHIPPED | `governance.go`, `cycle.go` | — |
| Tool denylist | SHIPPED | `governance.go` `denylistHit` | — |
| PreferLocalOnSoft | SHIPPED | `governance.go` | — |
| BudgetSpend dashboard chips | SHIPPED | `app.js` `spendChipHTML`, `live-spend` | — |
| **Chargeback UI / billing product** | **DEFERRED** | Spend tracked; no billing product decision | Enterprise scope |
| **SIEM / hash-chained audit export** | **DEFERRED** | No retention/compliance product decision | Enterprise scope |

---

### 1.12 Plugins / CapHooks / Skills

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Plugin lifecycle (Register/Init/Start/Stop/Health) | SHIPPED | `plugin/plugin.go`, `plugin/registry.go` | — |
| `CapTools` / `CapResources` / `CapHooks` / `CapAuth` | SHIPPED | `plugin.go` constants | — |
| `HookProvider` interface (`OnStageEnter`/`OnStageExit`) | SHIPPED | `plugin.go` | — |
| CapHooks dispatch around stages | SHIPPED | `plugin/registry.go` `HookProviders()`, `cycle.go` `DispatchStageEnter/Exit` | — |
| SKILL.md file resolution (path/id/absolute + string fallback) | SHIPPED | `loop/skill.go` | — |
| Skill prompt injection | SHIPPED | `loop/skill.go` `FormatSkillPrefix`, wired in `cycle.go` | — |

---

### 1.13 Worktrees

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| `isolateParallelWorkers` + `runs/<id>/work/wN` dirs | SHIPPED | `loop/worktree.go` | — |
| `git worktree add --detach` (with fallback to subdir) | SHIPPED | `worktree.go` `gitWorktreeAdd` | — |
| Feature-flag (`orchestration.loops.worktrees`) | SHIPPED | `config.go`, `runner.go` | — |
| `WORKER_ROOT` prompt hint | SHIPPED | `worktree.go` `isolationPromptHint` | — |

---

### 1.14 Feeds Edges

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| `kind: feeds` edge schema + `edgeKindFeeds` | SHIPPED | `feeds.go` | — |
| `RelFeeds` contextgraph edge record | SHIPPED | `feeds.go`, `contextgraph` | — |
| `feedSources` → `feedsPromptBlock` consumer injection | SHIPPED | `feeds.go` | — |
| WalkOrder skip control (feeds ≠ control-flow) | SHIPPED | `feeds.go` SM skip | — |
| Sample YAML (`feeds-edge-mvp.yaml`) | SHIPPED | `samples/hoops/` | — |
| **Phase 3: cross-hoop / swarm bidirectional feeds** | **DEFERRED** | In-hoop stage→stage MVP works | Full Temporal canvas topology |

---

### 1.15 AgentLog

| Area | Status | Evidence | What's missing |
|------|--------|----------|----------------|
| Per-scope ring store with `seq` | SHIPPED | `agentlog/store.go` `Store` | — |
| `After(afterSeq)` incremental poll cursor | SHIPPED | `store.go` `After` | — |
| `GET /api/agent-logs?after_seq=N` | SHIPPED | `dashboard/agent_logs.go` | — |
| Per-hoop / per-swarm-run isolation | SHIPPED | `Store.Append` by scope+id | — |
| Dashboard incremental merge (upsert by seq) | SHIPPED | `app.js` | — |
| Stronger empty states for unbound log | PARTIAL | Basic "no entries" shown; richer empty-state UX still rough | Minor polish; non-blocking |

---

### 1.16 SOLID Debt

| Area | Status | Evidence | Remaining work |
|------|--------|----------|----------------|
| `backend/interfaces.go` split → types/request/interfaces | SHIPPED ✅ | `backend/{types,request,interfaces}.go` | — |
| `tools/builtins.go` split → per-family files | SHIPPED ✅ | `builtin_{fs,git,shell,context,util,artifact,web_search,web_fetch,http_helpers}.go` | — |
| `swarm/{types,graph}` cohesion fix | SHIPPED ✅ | `GraphContext`/`WaveWorkerOut`/`SubtaskOut` moved to `swarm.go` | — |
| `contextgraph.Store` SRP split | SHIPPED ✅ | `EventLog` (`event_log.go`) + `EntityIndex` (`entity_index.go`) | — |
| `dashboard/api.go` split by handler group | SHIPPED ✅ | `api_config.go`, `api_context.go`, `api_loop.go` | — |
| `tools/agentloop.go` split (parse/format/registry) | SHIPPED ✅ | `parse.go`, `format.go`, `registry.go` | — |
| `tools/web.go` split | SHIPPED ✅ | `builtin_web_search.go`, `builtin_web_fetch.go`, `builtin_http_helpers.go` | — |
| `loop/eval.go` extract | SHIPPED ✅ | `eval.go` | — |
| `LoopGraphSink` interface (DIP) | SHIPPED ✅ | `loop/graph_sink.go`, `runner.go` | — |
| **`loop.Manager` god object** — `CycleExecutor` bodies | SHIPPED | Bodies on `CycleExecutor`; Manager thin wrappers; `runCycle` still on Manager | Further call-site migration optional |
| **`buildStagePrompt` extraction** | SHIPPED | `loop/prompt.go` | — |
| **`CheckGovernance` standalone** | SHIPPED | `governance.go` `CheckGovernance`; thin Manager wrapper | Interface type optional / not required |
| **Dashboard `Server` DIP** | **PARTIAL** | File split done; `*loop.Manager` + `*swarm.Runner` still concrete fields | Replace with narrow handler-group interfaces (P2) |
| **`contextgraph.Default()` global** | **PARTIAL** | Global singleton still present; mostly injected via interfaces | Remove global; explicit injection everywhere (P2) |
| **`swarm.Registry` → `ModuleRegistry` rename** | **PARTIAL** | Naming collision documented; not renamed (risky refactor) | P3 rename |

---

## 2. Prioritized Remaining Work

### P0 — None open
All P0 blockers closed.

---

### P1 — `loop.Manager` SRP (optional follow-ups)

**Shipped (2026-07-24 lock-in):** `CycleExecutor` owns completion bodies; `prompt.go` + `CheckGovernance` extracted. `runCycle` orchestration remains on `Manager` (further splits optional).

**Still optional:** migrate `runCycle` call sites to use `m.Exec().Complete*` directly (wrappers already work).

**Test surface:** `loop_test.go`, `governance_test.go`, `hitl_test.go`, `hoop_yaml_test.go`, `caphooks_skill_test.go`, `cycle_executor_test.go`.

---

### P2 — Medium debt (can be done in any order)

| # | Item | Effort |
|---|------|--------|
| P2-1 | Dashboard `Server` — replace `*loop.Manager` / `*swarm.Runner` with narrow handler-group interfaces | Med (DIP, no logic change) |
| P2-2 | `contextgraph.Default()` global removal — explicit injection everywhere | Low (search-replace + test) |
| P2-3 | Extract `swarm.Runner` inline governance check (~20 lines) → `governance.go` | Low |
| P2-4 | Agentlog dashboard empty-state polish | Low (UI-only) |
| P2-5 | Hosted Copilot MCP live PAT verify in prod | Ops: need live PAT |

---

### P3 — Low severity / cosmetic

| Item | Notes |
|------|-------|
| `swarm.Registry` → `swarm.ModuleRegistry` rename | Conceptual clarity; P3, skip until touching hotswap |
| `hotswap.go` move to `internal/hotswap/` | Architectural separation; P3 |
| OCP `RegisterBuiltinFactory` for `StandardBuiltins` | Only if tool count grows significantly |
| `config.go` split into per-section files | Only if config grows significantly |

---

### DEFERRED (Enterprise / Non-goals)

| Feature | Reason |
|---------|--------|
| SSO / RBAC / multi-tenant control plane | Needs IdP + tenancy model; out of local gateway scope |
| SIEM / hash-chained audit export | Needs retention/compliance product decisions |
| Temporal-class multi-day durable HITL | Process-local `MachineCursor` ≠ workflow engine |
| Full Leiden communities at repo scale | Needs denser EXTRACTED graph + Go dependency ingest |
| Live tree-sitter grammars on Windows | Deferred behind pragmatic `SymbolIndexer` |
| Chargeback UI for budgets | No billing product decision |
| Phase 3 feeds edges (cross-hoop / Temporal canvas) | In-hoop MVP ships; full topology deferred |
| Backend live hot-reload without restart | Only router/aliases/threshold/GPU swap today |
| Full Path B Cursor ToolCall catalog beyond common map | Common Read/Grep/Edit/Shell/Glob/Ls/Web map ships; full catalog / live UI chrome prefer Path A |
| Native Cursor IDE plugin | Operates as BYOK gateway + MITM proxy only |
| Full Cursor protocol RE beyond Path B text RunSSE | Non-goal |
| Multi-tenant SaaS control plane | Non-goal |
| Non-Go runtimes / live tree-sitter on Windows | SymbolIndexer is the pragmatic floor |

---

## 3. How to verify current state

```powershell
go test ./internal/agentlog/ ./internal/tools/ ./internal/mcp/ ./internal/cursorrpc/ ./internal/mitm/ ./internal/contextgraph/ ./internal/loop/ ./internal/swarm/ ./internal/dashboard/ ./internal/plugin/ ./internal/router/ ./internal/orchestrator/ ./internal/backend/... ./internal/vram/ -count=1

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

- **2026-07-24 (lock-in):** `CycleExecutor` body migration SHIPPED; `prompt.go` + `CheckGovernance` SHIPPED; Path B common ToolCall map SHIPPED (`toolcall_map.go`); worktrees/CapHooks/SKILL/L3/feeds verified
- Prior audit: code-truth feature matrix; promoted CapHooks/worktrees/SKILL/L3/feeds MVP to SHIPPED
