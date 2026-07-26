# Orchestrating agents with Glider: hoops, swarms, feeds, and workspaces

*How Glider turns “ask a model something” into a designed Loop Engineering system — with graph stages, parallel workers, shared context, and a sandbox per run.*

---

## Why orchestration, not one-shot chat

Cursor (and every chat UI) is great at **one turn**. Production agent work needs a **system that prompts agents**: observe → plan → act → evaluate → learn, with stop conditions, human gates, and tools that cannot walk all over your disk.

Glider’s orchestration layer is that system. It sits above the shared inference harness (gateway + MITM → resolve model alias → tokenize → route → transform → execute) and owns the **mission shape** — not which model answers a single call, but the job graph:

| Concern | Mechanism |
|---------|-----------|
| Who runs, in what order | **Hoop stages** (planner → actor → critic, …) |
| Parallel specialists | **Swarm** (`internal/swarm`) + in-hoop `parallel` |
| What stages share | **Context seed** + **feeds** edges |
| Human veto / pause | **`human_gate`** (HITL approve/resume) |
| When to stop | Eval score, `max_iterations`, governance budgets |
| Scratch vs deliverables | **Workspace binding** `runs/<id>/{work,out}` |

Open the accompanying diagrams in [Excalidraw](https://excalidraw.com) (File → Open):

| Diagram | File |
|---------|------|
| Stack | [`diagrams/01-orchestration-stack.excalidraw`](diagrams/01-orchestration-stack.excalidraw) |
| Hoop cycle | [`diagrams/02-hoop-cycle.excalidraw`](diagrams/02-hoop-cycle.excalidraw) |
| Fanout vs swarm | [`diagrams/03-fanout-vs-swarm.excalidraw`](diagrams/03-fanout-vs-swarm.excalidraw) |
| Workspace | [`diagrams/04-workspace-binding.excalidraw`](diagrams/04-workspace-binding.excalidraw) |

---

## 1. Where orchestration sits in the architecture

```text
Cursor / Dashboard
        │
        ▼
┌───────────────────┐     ┌────────────────────────────┐
│ Gateway :8080     │     │ MITM :8082 (/cloud sticky) │
│ Dashboard :8081   │────▶│ Shared harness             │
└───────────────────┘     │ alias→tokenize→route→…     │
                          └────────────┬───────────────┘
                                       │
                    ┌──────────────────┼──────────────────┐
                    ▼                  ▼                  ▼
              Loop Manager       Swarm Runner        Tools Registry
              (hoops/stages)     (templates)         + workspace
                    │                  │                  │
                    └──────────────────┴──────────► Ollama / vLLM / BYOK
```

**Model alias** (first harness step): map the client model ID (`model_aliases`, e.g. `gpt-4o` → `qwen2.5-coder:14b`) before tokenize/route.

Inference routing (`/local`, `/cloud`, classifier, sticky turn families) picks **which backend** buys the tokens for one completion. Orchestration defines the **mission shape**: stage graph, shared memory, parallel fan-out, human gates, and stop conditions. A hoop stage can force `route: cloud` or inherit the hoop route; a Cursor `/cloud` turn family also gets a `runs/<turn-id>/{work,out}` bind for sandbox association.

---

## 2. Hoops: Loop Engineering as a product primitive

A **hoop** is a persisted mission: goal, stages, graph edges, eval, optional schedule. Runtime lives under `~/.glider/loops/<id>.json`.

### Minimal smoke hoop

```yaml
# samples/hoops/hello-critic.yaml (abbreviated)
kind: hoop
id: hello-critic
goal: Produce a one-sentence greeting that includes "glider"
route: local
max_iterations: 2
human_gate: true
eval:
  min_score: 0.5
  goal: Greeting must contain "glider"
stages:
  - kind: router
    route: local
  - kind: planner
    prompt: Plan the shortest valid greeting.
  - kind: actor
    prompt: Emit only the greeting sentence.
  - kind: critic
    prompt: Score 0.0–1.0. Reply with SCORE=… and REASON=…
  - kind: memory
```

### Stage kinds (compose UI palette)

| Kind | Default job |
|------|-------------|
| `workspace` | Bind `work`/`out` — fresh run **or** existing sandbox path |
| `router` | Bias following stages local / cloud / auto |
| `planner` | Decompose / triage |
| `context` | Seed shared contextgraph → later `[context_digest]` |
| `actor` | Produce artifacts (optional `parallel`) |
| `critic` | Maker ≠ checker — must emit `SCORE:` |
| `memory` | Persist / load hoop memory |
| `human_gate` | Pause for approve / reject |

Cycle sketch:

```text
workspace → planner → actor → critic → (learn / memory)
                ↑_______________|  score fail / feedback edge
                         human_gate may pause
```

Implementation notes that matter for readers of the code:

- `Manager.runCycle` still owns the cycle walk; **`CycleExecutor`** holds completion bodies (`CompleteOnce` / tools / parallel / nested swarm).
- Critic is intentionally tool-light and must emit a parseable score (`SCORE:` or JSON `{score,reason}`).
- HITL sets `StatusWaitingHuman` with a gate `ask` payload (plan/actor/critic excerpts).

```go
// Conceptual — see internal/loop/stages.go
const (
    StageWorkspace StageKind = "workspace"
    StagePlanner   StageKind = "planner"
    StageActor     StageKind = "actor"
    StageCritic    StageKind = "critic"
    // ...
)
```

---

## 3. Graph edges: control flow vs data feeds

Edges are first-class on the hoop (`graph_edges`). Most are **control** (`flow`, `feedback`, `on_fail`, …). **`feeds`** is different: it is a **data seed**, not a state-machine walk.

```yaml
stages:
  - { id: research, kind: actor }
  - { id: synth, kind: actor }
graph_edges:
  - { source: research, target: synth, kind: feeds }  # → FEEDS: block in synth prompt
  - { source: research, target: synth, kind: flow }     # still need control flow to run synth
```

Producer stage summary is injected into the consumer as a `FEEDS:` prompt section. Cross-hoop / Temporal-class feeds remain deferred; in-hoop feeds are shipped (`samples/hoops/feeds-edge-mvp.yaml`).

---

## 4. Parallel actors: fanout vs nested swarm

When `parallel > 1` on an actor (typically), Glider fans work:

```yaml
- id: audit
  kind: actor
  parallel: 3
  parallel_mode: fanout   # default
  # parallel_mode: swarm  # nests swarm.Runner
  roles: [quality, security, secrets]
  tools:
    - { name: context_query, kind: builtin }
    - { name: artifact_write, kind: builtin }
```

| Mode | Runtime | Use when |
|------|---------|----------|
| `fanout` | In-process `swarm.FanOut` + critique merge | Quick multi-angle pass inside one stage |
| `swarm` | Nested `Runner.Run` / `RunWaves` | Full template, weave policies, multi-wave |

Requires `orchestration.swarm.enabled: true` for nested swarm (default in `configs/glider.yaml`). See `samples/hoops/parallel-swarm-mode.yaml`.

Optional **worktrees** (`orchestration.loops.worktrees: true`) isolate parallel workers under `runs/<id>/work/wN/`.

---

## 5. Swarm templates (team sheets, not forever-jobs)

A **swarm template** is a reusable fan-out recipe (`kind: swarm_template`) stored under `~/.glider/hoops/`. You do not “start the template forever” — you **run** it with a goal + turn id.

```yaml
# Conceptual swarm template shape
kind: swarm_template
template:
  id: code-review-swarm
  prompt: Review the change for correctness and risk.
  roles: [plan, security, tests, docs]
  tools:
    - { name: fs_list, kind: builtin }
    - { name: artifact_write, kind: builtin }
```

Dashboard: **Hoops & Swarm → Templates**, then Run. Samples: `samples/swarms/`. Seed:

```powershell
powershell -File scripts\seed-samples.ps1
```

---

## 6. Context seed and tools

### `kind: context`

Upserts hoop-scoped keys into the contextgraph (goal, plan, clone_path, file-tree digest, …). Later actors / swarm workers see a **CONTEXT digest** and can `context_query key=…` instead of re-cloning or inventing paths.

### Tools sandbox

All builtins are scoped under `~/.glider/workspace` (not your Glider git clone):

```text
~/.glider/workspace/
  runs/<hoop-or-turn-id>/
    work/     # action: clones, scratch, intermediate
    out/      # output: reports, packs, finals
```

Bare paths like `audit-target` resolve via **ScopeRel** into that run’s `work/`. Prefer:

```text
artifact_write kind=out path=report.md
```

### Workspace graph node

```yaml
- id: bind
  kind: workspace
  workspace_mode: run          # default — Ensure runs/<id>/{work,out}
  # workspace_mode: existing
  # workspace_path: projects/demo
  # out_path: projects/demo/out   # optional
```

Sample: `samples/hoops/workspace-existing-bind.yaml`. Status APIs expose `workspace.{work_rel,out_rel,…}`; browse with `GET /api/workspace?run=<id>`.

---

## 7. Skills, CapHooks, governance, HITL

```yaml
skill: security-audit   # → skills/security-audit/SKILL.md (or skills_dir)
autonomy: L2            # L1 report / L2 assisted / L3 unattended (gated)
governance:
  soft_tokens: 50000
  hard_tokens: 200000
  prefer_local_on_soft: true
human_gate: true
eval:
  min_score: 0.7
  on_fail_n: 3
```

- **Skills** inject SKILL.md (or plain-string fallback) into stage prompts.
- **CapHooks** (plugins) fire enter/exit around stages when registered.
- **Budgets** surface as dashboard spend chips (chargeback UI deferred).
- **HITL**: `POST /api/loops/{id}/approve` then resume; gate shows what the agent asked.

---

## 8. End-to-end: clone audit pattern (orchestration story)

Enterprise-shaped sample: `samples/hoops/clone-repo-security-audit.yaml`.

```text
1. workspace / EnsureRunLayout → runs/clone-repo-security-audit/{work,out}
2. planner (+ tools) → plan (no poisoned absolute paths)
3. kind: context → seed clone_path / goal into contextgraph
4. parallel actors (fanout or swarm) → context_query, fs_*, artifact_write
   ⚠ do not re-clone; undeclared git_clone rejected
5. critic → SCORE:
6. human_gate → operator reviews ask payload
7. out/ holds the report pack
```

Tool-loop budgets (stage vs parallel workers) and critic “no tools by default” keep local 14b models from thrashing.

---

## 9. API surface (orchestration)

```http
GET  /api/loops
POST /api/loops
POST /api/loops/{id}/start
POST /api/loops/{id}/stop
POST /api/loops/{id}/approve
GET  /api/agent-logs?scope=hoop&id={id}&after_seq=N

POST /api/swarm/run
GET  /api/swarm/templates
GET  /api/workspace?run={id}
GET  /api/hotswap/modules
```

Live graph paint uses hoop `progress` / swarm live snapshots over REST + WebSocket `agent_log` events.

---

## 10. Design principles we optimized for

1. **Maker ≠ checker** — separate critic stage with a hard score contract.
2. **Sandbox by default** — tools never treat the Glider repo as the workspace.
3. **Run-scoped artifacts** — every hoop, swarm turn, and `/cloud` turn family gets work/out association.
4. **Compose, don’t hardcode** — stages and edges are data (YAML + graph editor).
5. **Explicit nesting** — `parallel_mode: swarm` is opt-in, not a silent rewrite of fanout.
6. **Fail closed on escape** — `workspace_mode: existing` must stay under the tools root.

---

## 11. What we deliberately deferred

SSO/RBAC, SIEM hash-chain export, Temporal multi-day HITL, Leiden-scale communities, chargeback billing, Phase 3 cross-hoop feeds. See [`planning/intentional_backlog.md`](../../planning/intentional_backlog.md).

---

## Try it

```powershell
# From repo root — see also docs/SETUP.md
ollama pull qwen2.5-coder:14b
go build -o glider.exe ./cmd/glider
.\glider.exe --config configs\glider.yaml
# Dashboard http://127.0.0.1:8081
powershell -File scripts\seed-samples.ps1
go run ./scripts/loadhoop -file samples/hoops/hello-critic.yaml -start
```

Related docs: [Loop Engineering](../site/loop-engineering.html) · [Samples](../site/samples.html) · [Tools & MCP](../site/mcp.html) · [Setup guide](../SETUP.md).
