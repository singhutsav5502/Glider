# Slate weave + Graphify dual-layer context

> Plan + ship note **2026-07-19**. Answers: *why deferred weave?* and *can we unify event-log + Graphify-style graph?*  
> Prior art: [swarm_orchestration.md](./swarm_orchestration.md), [remaining_gaps.md](./remaining_gaps.md), [graphify_context_notes.md](./graphify_context_notes.md), [context_management.md](./context_management.md).  
> External: [Slate / Random Labs](https://randomlabs.ai/blog/slate) · [Graphify](https://github.com/Graphify-Labs/graphify)

---

## 1. Why aren't we doing durable Slate-style engineering and weaving?

**Short answer:** overnight MVP prioritized FanOut-first productization, sticky routing, tools/MCP, and hoop SM — not full Thread Weaving. Weave was **intentionally deferred**, not rejected.

### Evidence from planning

| Source | What it says |
|--------|----------------|
| [swarm_orchestration.md](./swarm_orchestration.md) §1 | Multi-agent ~65%: FanOut+Merge+CritiqueMerge+Loop bind; **not Slate weave**. Slate-like thread weaving **~0%** — aspirational. |
| [swarm_orchestration.md](./swarm_orchestration.md) §2–4 | Take episodes + role routing; **do not copy** TypeScript DSL / hive-mind. Swarm wave “FanOut + Episode weave” listed as **Stub**. Next steps: Path B verify → Episode wire → sample FanOut — weave later. |
| [remaining_gaps.md](./remaining_gaps.md) | Product decisions deferred: “Slate-style episode thread weaving / dynamic subagent spawn from planner”; “Richer PathSummary entity graph / separate Fact index persistence”. |
| [leftovers_overnight_plan.md](./leftovers_overnight_plan.md) | Explicitly deferred: “Slate thread weaving / dynamic subagent spawn”. P0 was MCP live, tool loop, HITL, SM swarm, budgets. |
| [context_management.md](./context_management.md) | Blackboard / Slate-like mailbox: “Overkill before FanOut is default-on”; defer until FanOut productized. |

### Why that order was correct

1. **Overnight MVP timebox** — live MCP, agentic tools, mid-cycle HITL, TopologySwarm route paint, and enforceable budgets had to ship as real code (no stubs). Weave is a second runtime metaphor on top of that.
2. **FanOut-first** — Slate’s Thread Weaving assumes bounded workers + episodic returns. Glider needed a working fan-out + critique merge before durable multi-wave threads could read prior wave outputs.
3. **Gateway identity** — Glider is a local/cloud gateway + hoop orchestrator, not a Slate clone. Copying the TS DSL / hive-mind UX would dilute Path A/B sticky work that already unblocked `/cloud` wrap-up.
4. **What Slate actually is** (skim): orchestrator dispatches bounded worker **threads**; workers return compressed **episodes**; episodes are woven back so subsequent workers get relevant state without dumping full transcripts ([randomlabs.ai/blog/slate](https://randomlabs.ai/blog/slate)). That is the target shape — we adopt the *primitive* (durable thread + wave + episode), not the product shell.

**P0 of this plan starts closing the gap** with durable threads + multi-wave FanOut + concatenate/critic weave — not full dynamic planner spawn.

---

## 2. Can't we combine event-log query + our graph in the same Graphify methodology?

**YES.** Same query surface, dual layers — that *is* the Graphify methodology applied to orchestration context.

### What Graphify teaches (skim)

Graphify builds a **queryable knowledge graph** (not vector RAG): structural extract + semantic links; every edge tagged **EXTRACTED** / **INFERRED** (/ AMBIGUOUS); `query` / `path` / `explain` over the graph ([Graphify-Labs/graphify](https://github.com/Graphify-Labs/graphify), [graphify_context_notes.md](./graphify_context_notes.md)).

### Dual-layer mapping for Glider

| Layer | Role | Provenance | Persist |
|-------|------|------------|---------|
| **Event log** (runtime provenance) | Append-only verbs: RouteDecided, SwarmFanOut, LoopTick, EpisodeMerged, … | Prefer **RUNTIME** (happened in process); EXTRACTED when attrs come from tool/source | `~/.glider/context/events-*.jsonl` |
| **Entity / edge store** (structural graph) | Threads, waves, workers, episodes, relations (`produced`, `follows`, `merged_into`) | **EXTRACTED** (explicit tool/AST later) · **INFERRED** (critic/path) · **RUNTIME** (swarm/hoop writes) | `~/.glider/context/entities.jsonl` |

`Store.Query` / `context_query` searches **both** layers. `PathSummary` prefers entity edges, falls back to event scan. Hoop + swarm **RecordFact** thread/wave/episode nodes so wave N+1 can pull prior outputs without re-reading transcripts.

We still **defer** tree-sitter codebase indexing, Leiden communities, and full Graphify `explain` UX — those are repo-knowledge features, not orchestrator MVP. P1 adds **lightweight file-tree EXTRACTED** indexing (dirs/files only) as a bridge.

---

## 3. Architecture (P0+P1 target)

```
                    ┌─────────────────────────────────────┐
                    │  contextgraph.Store (dual-layer)    │
                    │  ┌─────────────┐  ┌───────────────┐ │
  hoop / swarm ────►│  │ event log   │  │ entity/edges  │ │
  FanOut waves ────►│  │ (RUNTIME)   │  │ EXTRACTED /   │ │
  file-tree audit ─►│  │             │  │ INFERRED /    │ │
                    │  │ QueryOpts ∪ │  │ RUNTIME       │ │
  context_query ◄───│  │ PathSummary │  │ + file/dir    │ │
  PathSummary  ◄────│  │ Neighbor    │  │               │ │
                    │  └─────────────┘  └───────────────┘ │
                    │         persist ~/.glider/context/  │
                    └─────────────────────────────────────┘
                                      ▲
                    ┌─────────────────┴──────────────────┐
                    │  swarm.ThreadStore (~/.glider/     │
                    │  swarm/threads/*.json)             │
                    │  List / Resume / weave policies    │
                    │  planner SubTasks → waves          │
                    └────────────────────────────────────┘
```

---

## 4. Phases

### P0 — foundation (shipped)

- [x] Dual-layer `Query` (events + entities); provenance EXTRACTED | INFERRED | RUNTIME
- [x] Persist entities under `~/.glider/context/`; warm-load with events
- [x] Production `RecordFact` / `PathSummary` used by swarm sink + hoop parallel / cycle
- [x] Durable swarm threads on disk; multi-wave FanOut; weave = concatenate + CritiqueMerge
- [x] `context_query` hits both layers; remaining_gaps + docs note

### P1 — weave quality + Graphify query depth (this ship)

#### Weave

- [x] **Planner → SubTasks** — parse planner/plan-role output into bounded `SubTask` prompts; wave N uses SubTask i (still capped, no free spawn)
- [x] **Multi-wave from template** — YAML `waves` + `weave_policy` + optional `subtasks` / `decompose: true`
- [x] **Thread List / Resume** — `ThreadStore.List`; resume after process restart continues next wave(s) with same `thread_id`
- [x] **Weave policies** beyond concat:
  - [x] `concatenate` — baseline join of wave merges
  - [x] `role_weighted` — weight plan/research/exec/critic summaries
  - [x] `critic` — CritiqueMerge ranking across waves (P0 path)
  - [x] `conflict_callouts` — flag disagreeing worker summaries (token overlap / polarity cues)
- [x] **Dashboard/API** — `GET /api/swarm/threads`, `GET /api/swarm/threads/{id}`, `POST .../resume`; weave status on swarm graph
- [x] **Per-thread/wave agent logs** — log attrs `thread` + `wave`; agent-log focus by thread id
- [x] **Sample** — `samples/swarms/multi-wave-weave.yaml`
- [x] **Episode digest** — artifact digest fields on woven episode

#### Graphify-style context

- [x] **Richer kinds** — `file`, `dir`, `subtask`, `conflict`, `symbol` (label-only); edges `contains`, `conflicts_with`, `seeds`
- [x] **QueryOpts** — keyword + `from`/`to` path + neighborhood (1-hop) + provenance filter
- [x] **PathSummary in prod** — hoop cycle + swarm weave seed use PathSummary for decision narrative
- [x] **File-tree EXTRACTED** — `IndexFileTree(root)` when auditing a repo path (depth/file caps); post-`git_clone` + `/api/context/index-tree`
- [x] **Hoop/swarm dual R/W** — writes + Query used by relevancy SM hint path
- [x] **`context_query` filters** — parse `prov=RUNTIME path=a->b neigh=id ...` in tool input
- [x] **Docs** — deep update `docs/site/context.html` (dual-layer, weave, query filters)

### P2 — Slate-adjacent + Graphify-adjacent (shipped 2026-07-19)

- [x] Dynamic subagent spawn from planner (`free_spawn`; role invent via `[role:]` / `role=` / `@role`; capped ≤4)
- [x] Optional LLM critic after CritiqueMerge (`llm_critic` policy + `CriticFn` / Completer)
- [x] Lightweight symbol/AST EXTRACTED ingest (Go `go/parser` + JS/TS/Python regex; `SymbolIndexer` for tree-sitter later)
- [x] Community detection MVP + `explain` UX (connected components + god-nodes; API + `context_query`)
- [x] Dashboard Cytoscape timeline of durable thread waves (View → wave→worker→woven)
- [ ] Temporal-class multi-day HITL — **deferred** (process-local cursor ≠ weave; needs Temporal/Cadence-class durability)
- [ ] SSO / RBAC / SIEM — **deferred** (enterprise control plane)
- [ ] Full Leiden at repo scale — **deferred** (MVP communities ship; Leiden needs denser graph + dependency)
- [ ] Live tree-sitter grammars on Windows — **deferred** behind `SymbolIndexer` interface

---

## 5. Code anchors

| Piece | Path |
|-------|------|
| Dual-layer store | `internal/contextgraph/` (`graph.go`, `entity.go`, `query.go`, `filetree.go`, `symbols.go`, `communities.go`) |
| Durable threads + waves + weave policies | `internal/swarm/thread.go`, `waves.go`, `weave.go`, `decompose.go` |
| Graph sink (facts + events) | `internal/dashboard/swarm_api.go` |
| Hoop writes + PathSummary | `internal/loop/cycle.go` |
| Tool filters | `internal/tools/builtins.go` (`context_query`) |
| Sample | `samples/swarms/multi-wave-weave.yaml` |
| Docs | `docs/site/context.html` |

---

## 6. Verify

```powershell
go test ./internal/contextgraph/ ./internal/swarm/ ./internal/loop/ ./internal/dashboard/ ./internal/tools/ -count=1
# Multi-wave: POST /api/swarm/run {"prompt":"...","waves":2,"weave_policy":"conflict_callouts","decompose":true,"free_spawn":true}
# LLM critic: POST /api/swarm/run {"prompt":"...","waves":2,"weave_policy":"llm_critic"}
# Resume:     POST /api/swarm/threads/{id}/resume {"waves":1}
# Threads:    GET  /api/swarm/threads
# Symbols:    POST /api/context/index-symbols {"root":".","turn_id":"audit"}
# Explain:    GET  /api/context/explain?turn_id=audit&id=sym:...
# Communities:GET  /api/context/communities?turn_id=audit
```
