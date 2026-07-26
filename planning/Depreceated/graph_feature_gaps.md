# Graph feature gaps (swarm + hoop)

> **2026-07-19.** Graph-specific gaps vs prior art and enterprise expectations.  
> Complements [loop_swarm_gap_analysis.md](./loop_swarm_gap_analysis.md) (code checklist) and [enterprise_orchestrator_mvp.md](./enterprise_orchestrator_mvp.md) (product strategy).  
> Surfaces: Cytoscape hoop/swarm editors, `graph_edges` on `LoopSpec`, swarm templates, live paint.

---

## 1. What Glider graphs do today

| Surface | Capability |
|---------|------------|
| **Hoop graph** | Stages (`memory` / `router` / `planner` / `actor` / `critic`) + `graph_edges` (`flow` \| `feedback`); parallel actors via `parallel` + `roles` |
| **Swarm graph** | Role workers fan-out → merge → optional CritiqueMerge; templates YAML |
| **Runtime** | Order from flow edges; feedback to planner on critic fail; progress paint; per-instance agent logs |
| **UX** | Cytoscape + edgehandles; undo/modals targeted overnight (see gap analysis) |

Honest: graphs are **editable topology + runtime-aware paint**, not a full durable graph OS (LangGraph) or thread-weaving kernel (Slate).

---

## 2. Gaps vs prior art (by system)

### 2.1 LangGraph / LangSmith

| Prior art | Glider gap | Priority for graphs |
|-----------|------------|---------------------|
| Explicit state schema + reducers | Stage outputs are free text; no typed state channels | P1 |
| Checkpoint / PostgresSaver; resume after crash | Hoop state on disk; thin checkpoint fields; no cross-process resume of mid-stage | **P0** product, P1 graph |
| `interrupt()` + `Command(resume=…)` HITL | Critic score → human_gate status; no pause node with durable wait | **P0** |
| Conditional edges / dynamic routing | Only `flow` + `feedback`; no `if score < x → escalate` edge kind | **P0** |
| Subgraphs / nested graphs | Flat stage list | P2 |
| LangSmith cycle / branch visualization | Agent log + stage paint; no trace tree of LLM calls per edge | P1 |
| Guardrail nodes | Critic only; no dedicated policy/guardrail stage kind | P1 |

Sources: [LangGraph overview](https://docs.langchain.com/oss/python/langgraph/overview), [production interrupt patterns](https://ilirivezaj.com/ai/langchain-production), [Towards AI production practices](https://pub.towardsai.net/architecting-autonomous-ai-systems-with-langgraph-part-2-production-practices-and-advanced-7d93adfc0431).

### 2.2 Anthropic workflows (effective agents + multi-agent research)

| Pattern | Glider map | Gap |
|---------|------------|-----|
| Prompt chaining | Hoop stages | OK |
| Routing | Router stage + gateway classifier | Graph does not visualize route decision as a branch |
| Parallelization (sectioning / voting) | Actor `parallel` + swarm roles | No vote-threshold edge; merge is concat-ish |
| Orchestrator–workers (dynamic N) | Fixed roles / max_workers | **Cannot spawn variable subagents from planner output** |
| Evaluator–optimizer | Critic + feedback edge | Feedback is binary-ish; no structured critique fields on edges |
| Citation / verify pass | Critic prompt only | No first-class `verifier` stage kind with source anchors |
| External memory for plan | Memory stage + contextgraph | Plan not first-class graph artifact |

Sources: [Building effective agents](https://www.anthropic.com/engineering/building-effective-agents), [Multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system).

### 2.3 OpenAI Swarm → Agents SDK

| Prior art | Gap |
|-----------|-----|
| Agent handoffs as first-class edges | Swarm roles are parallel, not conversational handoff graph |
| Guardrails / tracing / sessions (Agents SDK) | No graph-attached guardrail or session node |
| Tool-as-handoff | Tools stay Path A / Cursor; not drawn on hoop graph |

Sources: [openai/swarm](https://github.com/openai/swarm), [Respan 2026](https://www.respan.ai/articles/is-openai-swarm-still-worth-using).

### 2.4 Temporal / Prefect (durability)

| Prior art | Gap |
|-----------|-----|
| Activity retries with backoff drawn as workflow | Retries exist in orchestrator 1:1; **not modeled on hoop edges** |
| Signals / queries for HITL | No “waiting for human” node state in Cytoscape |
| Cached successful steps on resume | Re-run hoop often redoes LLM stages |
| Child workflows / fan-out as graph | FanOut is runtime semaphore, not nested graph |

Sources: [Temporal + OpenAI Agents](https://temporal.io/blog/announcing-openai-agents-sdk-integration), [Prefect + Pydantic AI](https://www.prefect.io/blog/prefect-pydantic-integration).

### 2.5 CrewAI / Microsoft Agent Framework / AutoGen

| Prior art | Gap |
|-----------|-----|
| Role → task → process (sequential/hierarchical) | Swarm is flat fan-out; no hierarchical crew graph |
| Magentic / group-chat topologies | No chat-topology canvas |
| A2A / MCP as graph ports | MCP not hoop-owned; no connector pins on nodes |

Sources: [Microsoft Agent Framework](https://azure.microsoft.com/en-us/blog/introducing-microsoft-agent-framework/), [CrewAI](https://crewai.com/), [JetThoughts compare](https://jetthoughts.com/blog/autogen-crewai-langgraph-ai-agent-frameworks-2025/).

### 2.6 Slate / Random Labs

| Prior art | Gap |
|-----------|-----|
| Orchestrator thread + worker threads | Single hoop runner + FanOut; not OS-kernel metaphor |
| Episodes woven into orchestrator memory | MergeResults / Episode stubs — **not thread weaving** |
| DSL “program in action space” | YAML stages only |
| Worker Summary live CLI | Dashboard agent log is closest |

Sources: [Slate introduction](https://docs.randomlabs.ai/en/getting-started/introduction), [VentureBeat Slate V1](https://venturebeat.com/orchestration/y-combinator-backed-random-labs-launches-slate-v1-claiming-the-first-swarm).

---

## 3. Graph-specific feature gap list (actionable)

### P0 — blocks enterprise trust in the graph

| Gap | Notes |
|-----|-------|
| **Conditional / policy edges** | **Done 2026-07-19** — `on_fail`/`escalate`/`conditional`/`budget_exceeded` + SM guards |
| **HITL wait node** | **Done 2026-07-19** — `human_gate` stage + `waiting_human` + approve/resume API (not Temporal-durable) |
| **Failure narrative on merge** | Partial — CritiqueMerge annotates; Cytoscape labels still thin |
| **Mid-cycle stage progress** | **Done** — Progress + DecisionRoute paint |
| **Versioned graph snapshots** | **Done MVP** — `graph_version` + `GET …/snapshot` |

### P1 — parity with modern orchestrators

| Gap | Notes |
|-----|-------|
| **Typed edge payloads** | Structured critique / plan / evidence refs, not only text |
| **Guardrail / policy stage kind** | Separate from critic; maps to gateway policy |
| **Retry / timeout decoration on edges** | Temporal/Prefect-style semantics visible |
| **Trace overlay** | Link agent_log lines to node ids; OTEL later |
| **Vote / quorum edges** | Parallel voting with threshold |
| **Budget / token meter on graph** | Soft/hard caps per stage or swarm |

### P2 — aspirational / later

| Gap | Notes |
|-----|-------|
| Nested subgraphs | Release train as meta-graph of team swarms |
| Dynamic worker spawn from planner | Anthropic LeadResearcher pattern |
| Thread weaving / episode canvas | Slate-class |
| Multi-tenant graph libraries | Shared org templates with RBAC |
| SSO-bound edit permissions | Who may publish a hoop |

---

## 4. Enterprise graph concerns (non-UX)

These are not Cytoscape widgets, but they **must attach to graph runs** for enterprise MVP later:

| Concern | Graph attachment idea |
|---------|----------------------|
| **Audit** | Immutable event per node enter/exit + edge taken ([TrueFoundry](https://www.truefoundry.com/blog/ai-governance-audit-enterprise-llm-gateway), [teamazing](https://www.teamazing.com/blog/ai-agent-audit-trail-rbac-requirements/)) |
| **RBAC** | Publish / run / edit permissions per template |
| **Budgets** | Stage and swarm caps; auto-downgrade route |
| **Secrets** | No secrets in YAML prompts; vault refs on nodes |
| **Multi-tenant** | Namespace templates + logs |
| **Observability** | Correlate `hoop_id` / `turn_id` with gateway metrics |

---

## 5. Update note for loop_swarm_gap_analysis

Keep [loop_swarm_gap_analysis.md](./loop_swarm_gap_analysis.md) as the **overnight code checklist**. This file is the **external prior-art / enterprise graph gap** companion. When closing a code P0 (e.g. conditional edges), update both:

1. Checklist row in gap analysis → Done  
2. Section 3 row here → Done + date  

---

## 6. Recommended graph roadmap (short)

```
MVP graphs:   flow + feedback + parallel paint + failure labels + version field
Next:         conditional edges + HITL wait node + typed critique payload
Later:        subgraphs + dynamic spawn + episode weave canvas
```

Enterprise orchestrator packaging: [enterprise_orchestrator_mvp.md](./enterprise_orchestrator_mvp.md).
