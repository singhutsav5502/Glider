# SOLID Refactor Backlog — Glider

> Audit date: 2026-07-24  
> Scope: `internal/loop`, `internal/tools`, `internal/swarm`, `internal/backend`, `internal/dashboard`, `internal/config`, `internal/contextgraph`

---

## Applied Fixes (this session)

| # | Change | Files | Principle |
|---|--------|-------|-----------|
| ✅ | Split `backend/interfaces.go` → `types.go` + `request.go` + `interfaces.go` (interfaces only) | `internal/backend/` | SRP |
| ✅ | Split `tools/builtins.go` (738 lines) into per-family files: `builtin_fs.go`, `builtin_git.go`, `builtin_shell.go`, `builtin_context.go`, `builtin_util.go`, `builtin_artifact.go` | `internal/tools/` | SRP |
| ✅ | Moved `GraphContext` + `WaveWorkerOut` + `SubtaskOut` from `swarm/waves.go` → `swarm/swarm.go` (co-located with `GraphSink`, `Swarm`, `Worker`) | `internal/swarm/` | SRP / cohesion |
| ✅ | Removed duplicate `GraphSink` definition from `swarm/run.go` | `internal/swarm/run.go` | DRY |
| ✅ | `contextgraph.Store` facade: extract `EventLog` (`event_log.go`) + `EntityIndex` (`entity_index.go`); Store embeds both; public API unchanged | `internal/contextgraph/` | SRP |
| ✅ | Split `dashboard/api.go` by handler group → `api_config.go`, `api_context.go`, `api_loop.go` (sessions/MITM/metrics remain in `api.go`) | `internal/dashboard/` | SRP |
| ✅ | Split `tools/agentloop.go` → `parse.go` (text tool-call parsing), `format.go` (`FlattenToolArgs` / `FormatToolResults`); moved `OpenAIToolsJSON` + `InvokeAll*` → `registry.go` | `internal/tools/` | SRP |
| ✅ | Split `tools/web.go` → `builtin_web_search.go`, `builtin_web_fetch.go`, `builtin_http_helpers.go` | `internal/tools/` | SRP |
| ✅ | Extract eval score parsing → `internal/loop/eval.go` | `internal/loop/eval.go` | SRP |
| ✅ | `LoopGraphSink` interface; `Manager.Graph` is DIP’d | `internal/loop/graph_sink.go`, `runner.go` | DIP |
| ✅ | `CycleExecutor` owns `CompleteOnce` / `CompleteWithTools` / `CompleteParallel*` bodies; Manager thin wrappers | `internal/loop/cycle_executor.go` | SRP |
| ✅ | Extract `stagePrompt` + `StagePrompt` → `prompt.go` | `internal/loop/prompt.go` | SRP |
| ✅ | Extract `CheckGovernance` standalone (+ Manager wrapper) | `internal/loop/governance.go` | SRP |
| ✅ | Dashboard `Server` DIP: `Loops LoopAPI` + `Swarm SwarmAPI` | `internal/dashboard/deps.go`, `server.go` | DIP |
| ✅ | Remove `contextgraph.Default`/`SetDefault`; explicit `*Store` injection | `contextgraph/graph.go`, `mitm/agent_fulfill_hub.go`, `cmd/glider` | DIP |

Skipped (risky / not low-risk): `swarm.Registry` → `ModuleRegistry` rename and `hotswap` package move — leave as P3.

`gofmt` + `go test` green for `./internal/dashboard/`, `./internal/tools/`, `./internal/swarm/`, `./internal/loop/`, `./internal/plugin/`.

---

## Prioritized Debt Backlog

### P1 — High severity, medium effort

#### `loop.Manager` god object (`runner.go` + `cycle.go` ~2 173 lines combined)

**Violation:** SRP + OCP  
**Detail:**  
`Manager` owns: hoop CRUD, lifecycle goroutines, swarm fan-out nesting, HITL gate logic, artifact layout, context seeding, progress tracking, and state persistence. Eval parsing + Graph DIP + **CycleExecutor completion bodies** + **prompt.go** + **CheckGovernance** extracted.

**Refactor targets (do not implement together):**

| Sub-concern | Extract to | Risk |
|-------------|-----------|------|
| LLM tool-loop execution (`completeOnce`, `completeWithTools`, `completeParallel`, `completeParallelSwarm`) | `CycleExecutor` ✅ bodies moved | — |
| Eval score parsing (`scoreLine` regex + parse logic) | `internal/loop/eval.go` | ✅ Done |
| Stage prompt construction (`buildStagePrompt`, `StagePrompt`) | `internal/loop/prompt.go` | ✅ Done |
| Governance checking (`checkGovernance`) | `CheckGovernance` in `governance.go` | ✅ Done |
| Context seeding | Already in `context_seed.go`; extract `ContextSeeder` interface | Med |

**DIP:** ✅ `Manager.Graph LoopGraphSink` (was `*contextgraph.Store`).

---

#### `contextgraph.Store` god object (`graph.go` + helpers)

**Violation:** SRP (partially addressed)  
**Detail:** One facade still owns the dual layer, but event log vs entity indexing are now separate embedded types.

**Done (incremental):**

| Concern | Type | File |
|---------|------|------|
| Event log + turn index | `EventLog` | `event_log.go` ✅ |
| Entity/edge map | `EntityIndex` | `entity_index.go` ✅ (+ `entity.go` public API) |
| Hoop context digest | (methods on Store) | `hoop_context.go` ✅ already split |

**Still to extract:**

| Concern | Suggested type | File |
|---------|---------------|------|
| Sticky routing (BindRequest, CloudTurnLive, LiveCloudFamily) | `StickyRouter` | `sticky.go` |
| Query engine | `Querier` | already in `query.go`, extract as standalone |
| Indexers (file-tree, symbols) | `Indexer` | already split by file |

**Approach:** Keep `Store` as the public facade composing sub-stores. No caller API changes.

**Global singleton risk:** ✅ Removed — `Default()` / `SetDefault()` deleted; composition root injects `*Store` into MITM hub, pipeline, loop Manager, swarm, dashboard.

---

### P2 — Medium severity, low-to-medium effort

#### `dashboard.Server` mega-composition-root — file split ✅

**Done:** Handlers grouped into `api_config.go`, `api_context.go`, `api_loop.go`; `api.go` keeps shared stores/controllers + sessions/MITM/metrics + `writeJSON`. Already split: `swarm_api.go`, `mcp_api.go`, `workspace_api.go`, `agent_logs.go`.

**Remaining (DIP):** ✅ Done — `Loops LoopAPI`, `Swarm SwarmAPI` in `deps.go`; concrete `*loop.Manager` / `*swarm.Runner` assigned at construction.

---

#### `swarm.Runner` multi-concern struct (`run.go` ~600 lines + `waves.go` ~492 lines)

**Violation:** SRP  
**Detail:** `Runner` owns: FanOut dispatch, multi-wave sequencing, live progress snapshots, governance, durable thread storage, template management, and LLM critic.

**Refactor targets:**

| Concern | Suggested move |
|---------|---------------|
| `liveRuns` / `LiveProgress` | `LiveProgressStore` embedded or separate (already in `live.go`) |
| `Templates *TemplateStore` | Caller-injected; already separate in `templates.go` ✅ |
| Governance check | Standalone func (small, ~20 lines) in `governance.go` |
| Wave sequencing | `RunWaves` / `ResumeThread` already in `waves.go` ✅ |

**Naming collision:** `swarm.Registry` (hotswap, `hotswap.go`) collides conceptually with `backend.Registry` and `tools.Registry`. Rename to `swarm.ModuleRegistry` to clarify scope.

---

#### `tools/agentloop.go` mixed concerns (603 lines)

**Violation:** SRP  
**Detail:** `agentloop.go` contains: OpenAI tools JSON builder (`OpenAIToolsJSON`), text tool-call parser (`ParseTextToolCalls`, `LooksLikeTruncatedToolJSON`), the agent loop itself (`RunAgentLoop`), parallel invocation helpers (`InvokeAll`, `InvokeAllParallel`), and format utilities (`FlattenToolArgs`, `FormatToolResults`).

**Refactor targets:**

| Concern | Extract to |
|---------|-----------|
| `OpenAIToolsJSON` | `registry.go` (it's a `*Registry` method) |
| Text tool-call parsing | `tools/parse.go` |
| `RunAgentLoop` | Keep in `agentloop.go` |
| `InvokeAll` / `InvokeAllParallel` | `registry.go` (dispatcher) |
| `FlattenToolArgs` / `FormatToolResults` | `tools/format.go` |

---

#### `tools/web.go` two unrelated providers (663 lines)

**Violation:** SRP  
**Detail:** `web.go` contains both `webSearchTool` (SerpAPI/Brave search) and `webFetchTool` (HTML-to-text fetcher) plus the shared URL/host-allowlist helpers used by `httpFetch` in `builtin_shell.go`.

**Refactor targets:**

| Concern | Extract to |
|---------|-----------|
| `webSearchTool` + search providers | `builtin_web_search.go` |
| `webFetchTool` | `builtin_web_fetch.go` |
| `parseURLArg` / `checkHostAllowed` | `builtin_http_helpers.go` or keep in one of the above |

---

### P3 — Low severity / architectural notes (no immediate action)

#### `backend/interfaces.go` — now clean ✅
Four focused interfaces (`InferenceBackend`, `ModelManager`, `LoRAManager`, `HealthChecker`) satisfy ISP well. No action needed.

#### `config` package — acceptable large DTO
`config.go` is 505 lines of nested structs. This is expected for a config package and is a _coupling magnet_ rather than an SRP violation. Consider splitting into `config/server.go`, `config/routing.go`, `config/tools.go`, etc., only if adding significantly more config sections.

#### `tools.ContextStore` / `swarm.GraphSink` / `swarm.GraphContext` — DIP already applied ✅
These interfaces prevent `tools` and `swarm` from importing `contextgraph`. Keep this pattern. The risk is that `loop/runner.go` still holds `Graph *contextgraph.Store` directly — see P1.

#### `swarm.hotswap` module registry out-of-place
`hotswap.go` (module live-reload registry) is a config/runtime concern, not a fan-out execution concern. Long-term: move to `internal/hotswap/` package.

#### OCP in `tools.StandardBuiltins`
Adding a new builtin still requires editing `StandardBuiltins` and adding a new `builtin_*.go` file. An OCP-compliant solution is a `RegisterBuiltinFactory` mechanism, but the current slice approach is simple and low-risk. Not worth fixing until tool count grows significantly.

---

## Summary Table

| Severity | Count | Packages |
|----------|-------|---------|
| High (P1) | 1 | `contextgraph` (partial — sticky/indexer globals) |
| Medium (P2) | 4 | `dashboard`, `swarm`, `tools/agentloop`, `tools/web` |
| Low/note (P3) | 5 | `config`, `backend` (clean ✅), `tools` (OCP), `swarm` (hotswap rename), `loop` (DIP) |

### Quick-wins if resuming this work

1. ~~Extract eval score parsing out of `cycle.go` → `internal/loop/eval.go`~~ ✅
2. ~~Split `api.go` into `api_config.go` + `api_context.go` + `api_loop.go`~~ ✅ (file split; Server DIP still **PARTIAL**)
3. ~~Split `tools/agentloop.go` text-parse helpers → `tools/parse.go`~~ ✅
4. ~~Define `LoopGraphSink` interface in `internal/loop/`~~ ✅
5. ~~`CycleExecutor` body migration~~ ✅ — bodies on `CycleExecutor`; Manager thin wrappers
6. ~~Extract `buildStagePrompt` → `prompt.go`~~ ✅
7. ~~Extract `checkGovernance` → `CheckGovernance`~~ ✅
