# Glider as enterprise-scale orchestrator MVP

> Research snapshot **2026-07-19**. Cross-check: [swarm_orchestration.md](./swarm_orchestration.md), [loop_engineering.md](./loop_engineering.md), [graph_feature_gaps.md](./graph_feature_gaps.md), [loop_swarm_gap_analysis.md](./loop_swarm_gap_analysis.md).  
> Code authority: `internal/loop`, `internal/swarm`, `internal/orchestrator`, Cytoscape dashboard graphs.

Glider already sits in a useful niche: **local-first model gateway + hoop loops + bounded swarm fan-out**, with Path A/B Cursor interception. This note positions that stack as an **enterprise orchestrator MVP** — what to sell first, what to defer, and how industry practice (2024–2026) maps onto Glider.

---

## 1. Industry signal (2024–2026)

| Pattern | What production teams actually ship | Sources |
|---------|-------------------------------------|---------|
| **Handoffs / routines** | Lightweight agent + tool + handoff; Swarm → Agents SDK (guardrails, tracing, sessions) | [openai/swarm](https://github.com/openai/swarm), [Respan: Swarm in 2026](https://www.respan.ai/articles/is-openai-swarm-still-worth-using), [OpenAI Agents + Temporal](https://temporal.io/blog/announcing-openai-agents-sdk-integration) |
| **Graph / state machine** | Explicit graphs, checkpointing, `interrupt()` HITL, LangSmith traces | [LangGraph overview](https://docs.langchain.com/oss/python/langgraph/overview), [Ilir Ivezaj production notes](https://ilirivezaj.com/ai/langchain-production), [Towards AI Part 2](https://pub.towardsai.net/architecting-autonomous-ai-systems-with-langgraph-part-2-production-practices-and-advanced-7d93adfc0431) |
| **Role crews** | Fast role→task mapping; enterprise AMP / governance as upsell | [CrewAI](https://crewai.com/), [JetThoughts framework compare](https://jetthoughts.com/blog/autogen-crewai-langgraph-ai-agent-frameworks-2025/) |
| **MSFT unify** | AutoGen + Semantic Kernel → Agent Framework; A2A, MCP, Foundry observability | [Azure Blog: Agent Framework](https://azure.microsoft.com/en-us/blog/introducing-microsoft-agent-framework/), [landscape 2025](https://medium.com/@hieutrantrung.it/the-ai-agent-framework-landscape-in-2025-what-changed-and-what-matters-3cd9b07ef2c3) |
| **Durable OS for agents** | Workflows deterministic; LLM/tools as activities; signals = HITL; crash-safe replay | [Temporal durable agent tutorial](https://learn.temporal.io/tutorials/ai/durable-ai-agent/), [Durable Agentic Harness](https://temporal.io/code-exchange/durable-agentic-harness-crash-safe-autonomous-ai-agents-with-human-in-the-loop-approval) |
| **Data/agent orchestration** | Retries, cache, resume-from-failure; Prefect↔Pydantic AI; Prefect acquires Dagster Labs | [Prefect + Pydantic AI](https://www.prefect.io/blog/prefect-pydantic-integration), [Prefect acquires Dagster](https://www.prefect.io/prefect-acquires-dagster) |
| **Orchestrator–worker research** | Lead plans → parallel subagents → citation/critic pass; OODA research loops | [Anthropic multi-agent research](https://www.anthropic.com/engineering/multi-agent-research-system), [Building effective agents](https://www.anthropic.com/engineering/building-effective-agents) |
| **Swarm-native coding** | Thread weaving, episodes, bounded workers (aspirational bar for Glider) | [Slate docs](https://docs.randomlabs.ai/en/getting-started/introduction), [VentureBeat Slate V1](https://venturebeat.com/orchestration/y-combinator-backed-random-labs-launches-slate-v1-claiming-the-first-swarm) |
| **Enterprise control plane** | RBAC, virtual keys, budgets, immutable audit, policy gates, HITL, multi-tenant | [TrueFoundry governance](https://www.truefoundry.com/blog/ai-governance-audit-enterprise-llm-gateway), [teamazing RBAC/audit](https://www.teamazing.com/blog/ai-agent-audit-trail-rbac-requirements/), [Fleece governance 2026](https://fleeceai.app/blog/ai-agent-governance-audit-policy-compliance-2026) |

**Takeaway:** Frameworks won on **graphs + durability + HITL + observability**. Gateways won on **policy, budget, audit, RBAC**. Glider already looks like a **gateway-shaped orchestrator** with hoop graphs — the MVP should double down on that wedge, not chase Slate thread-weaving or LangGraph Cloud parity in v1.

---

## 2. Glider wedge (honest)

| Strength today | Enterprise translation |
|----------------|------------------------|
| Dual-mode gateway + Path A/B | Controlled model path for IDEs / agents without rewriting every client |
| Hoop loop (plan → act → critique → learn) | Evaluator–optimizer / maker≠checker workflows enterprises already trust |
| Bounded FanOut + CritiqueMerge | Parallel sectioning / voting without unbounded spend |
| Cytoscape `graph_edges` (flow + feedback) | Visible, editable topology operators can audit |
| Local-first + BYOK | Data residency / air-gapped-friendly pilots |
| Rate / budget / breaker (orchestrator 1:1) | Seed of FinOps controls |

| Weakness vs enterprise bar | Why it matters |
|----------------------------|----------------|
| No multi-tenant RBAC / SSO | Security review blockers |
| Audit not SIEM-grade / hash-chained | SOC2 / EU AI Act logging asks |
| HITL is critic-score escalate, not durable pause/resume | Long approvals need Temporal-class signals |
| No policy-as-code on tools / models | Cannot prove “who may call what” |
| Episode weave / Slate-class memory thin | Long-horizon multi-team work loses coherence |
| Graph conditional / dynamic spawn limited | Anthropic-style dynamic subagent count |

---

## 3. Usage areas (where Glider MVP fits)

Prioritize **closed-loop knowledge work** with clear eval criteria and human gates — Anthropic’s own advice: start simple, add agents when measurable ([Building effective agents](https://www.anthropic.com/engineering/building-effective-agents)).

| Area | Hoop vs swarm | Why Glider |
|------|---------------|------------|
| **Incident command / on-call** | Hoop (parallel actors + critic + feedback) | Runbook draft with safety critic; L1 human gate |
| **Security / change review** | Swarm (multi-role fan-in) | Sectioning: threat, authz, secrets, blast radius |
| **Compliance evidence packs** | Hoop (plan → collect → pack → auditor critic) | Maker≠checker; SCORE gate for audit readiness |
| **Release train coordination** | Swarm (platform / app / QA / docs / SRE) | Role fan-out; merge narrative for go/no-go |
| **Support / triage factories** | Swarm or hoop | Existing support-triage sample; sticky routing context |
| **Research → brief → cite** | Hoop | Anthropic orchestrator–worker lite without full subagent spawn |
| **Data pipeline QA** (optional) | Swarm | Schema / freshness / PII / rollback roles |
| **Escalation policy drafting** (optional) | Hoop | Critic enforces severity matrix completeness |

**Do not position MVP as:** unbounded autonomous coding hive, cross-org A2A mesh, or Temporal replacement.

---

## 4. Strategies

### 4.1 Product strategy — “governed local orchestrator”

1. **Own the loop graph** — hoop YAML + Cytoscape as the audit-visible workflow (prompt chaining + evaluator–optimizer).
2. **Own the gateway** — every LLM call already passes Glider; attach identity, budget, and audit here (TrueFoundry / DVARA-shaped control plane).
3. **Bound parallelism** — keep FanOut max + semaphore; sell predictability over swarm hype.
4. **Maker ≠ checker by default** — critic + `eval.min_score` + `human_gate` as the enterprise default template.
5. **Local-first pilots** — prove value on Ollama/vLLM; cloud BYOK as escape hatch with sticky policy.

### 4.2 Go-to-market usage packaging

| Package | Includes | Buyer |
|---------|----------|-------|
| **Pilot MVP** | Showcase hoops/swarms, agent logs, dashboard graphs, local route | Platform eng / SRE |
| **Team MVP** | Templates library, Starlark fan-out rules, per-hoop budgets, HITL queue | Eng manager |
| **Org later** | SSO, RBAC, multi-tenant, SIEM export, policy-as-code, durable HITL | CISO / platform |

### 4.3 Technical strategy (MVP vs later)

| Capability | MVP (ship / harden) | Later |
|------------|---------------------|-------|
| Hoop plan–act–critique + feedback edges | Harden samples + docs | Dynamic stage spawn |
| Swarm role fan-out + CritiqueMerge | Templates for security / release | Episode weave (Slate-like) |
| Per-instance agent logs | Keep | OpenTelemetry export |
| Orchestrator rate/budget/breaker | Surface in dashboard | Soft→hard budget + chargeback |
| Autonomy L1 + human_gate | Document runbooks | Durable interrupt/resume (Temporal pattern) |
| `contextgraph` events | Wire every fulfill | Immutable audit + SIEM |
| Skills string | OK | SKILL.md + connectors (MCP owned by hoop) |
| Graph UX undo/modals | Sibling work | Conditional edges / subgraphs |
| Multi-tenant / SSO / RBAC | Stub design only | Full control plane |
| Worktrees / isolation | Document | Agent-side isolation product |
| A2A / MCP mesh | Path A via Cursor only | First-class Glider MCP proxy |

---

## 5. MVP definition (enterprise orchestrator)

**In scope for MVP:**

- Loadable hoop + swarm YAML (`samples/`, `loadhoop`) with `graph_edges`.
- Parallel actors / FanOut with role tags and merge + critique.
- Critic SCORE gates, stop conditions (max iter, contains, latency, on_fail_n), L1 human gate.
- Dashboard graph edit + live progress + per-instance agent logs.
- Gateway budgets/rate limits as operator knobs.
- Documentation that maps usage areas → sample files.

**Explicitly out of MVP:**

- SSO / OIDC, org RBAC, multi-tenant isolation.
- Cryptographic audit chain / SIEM connectors.
- Durable multi-day HITL (Temporal-class).
- Slate thread weaving / DSL action space.
- Unbounded dynamic subagent spawning.
- Policy-as-code (OPA/Cedar) on tools.

**Success metrics for a pilot:**

- Operator can load a hoop/swarm from `samples/` and see graph + agent log.
- Critic gate fails unsafe drafts (human_gate fires).
- Fan-out stays ≤ configured max workers; spend bounded.
- One SEV/compliance/release scenario demoed end-to-end on local models.

---

## 6. Competitive positioning (one paragraph)

Use **LangGraph/Temporal/Prefect** when the customer needs durable, multi-day workflows and deep graph programming. Use **CrewAI / Agent Framework** when they want role crews inside Azure or rapid content pipelines. Use **Glider** when they need a **self-hosted gateway that already routes IDE traffic**, plus **visible hoop/swarm graphs** with maker–checker loops and bounded fan-out — especially for **incident, security review, compliance pack, and release-train** knowledge work on local or BYOK models.

---

## 7. Related samples

| Scenario | File |
|----------|------|
| Enterprise incident command | `samples/hoops/enterprise-incident-command.yaml` |
| Compliance evidence pack | `samples/hoops/compliance-evidence-pack.yaml` |
| Escalation policy (optional) | `samples/hoops/escalation-policy.yaml` |
| Security review swarm | `samples/swarms/security-review-swarm.yaml` |
| Multi-team release train | `samples/swarms/multi-team-release-train-swarm.yaml` |
| Data pipeline QA (optional) | `samples/swarms/data-pipeline-qa-swarm.yaml` |

See [docs/site/samples.html](../docs/site/samples.html).
