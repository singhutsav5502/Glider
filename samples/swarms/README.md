# Swarm templates

Reusable fan-out recipes (`kind: swarm_template`). Copy into `~/.glider/hoops/` or save via Dashboard → Hoops & Swarm → Templates.

| File | Description |
|------|-------------|
| `security-review-swarm.yaml` | Enterprise security review (threat / authz / secrets / blast) |
| `multi-team-release-train-swarm.yaml` | Cross-team release train go/no-go (platform / app / qa / sre) |
| `data-pipeline-qa-swarm.yaml` | Warehouse / feature-store QA (schema / freshness / PII / rollback) |
| `code-review-swarm.yaml` | Multi-agent code review (plan / security / tests / docs) |
| `support-triage-swarm.yaml` | Customer support ticket triage |

See [../hoops/README.md](../hoops/README.md), [docs/site/samples.html](../../docs/site/samples.html), and [planning/enterprise_orchestrator_mvp.md](../../planning/enterprise_orchestrator_mvp.md).
