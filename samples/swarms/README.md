# Swarm templates

Reusable fan-out recipes (`kind: swarm_template` or `kind: swarm`). Seed all samples with `powershell -File scripts\seed-samples.ps1` (writes into `~/.glider/hoops/`), or copy manually / save via Dashboard → Hoops & Swarm → Templates.

| File | Description |
|------|-------------|
| `repo-audit-swarm.yaml` | Quality + security + secrets audit of Unbrokify (`git_clone` + ScopeRel + `artifact_write` + `context_query` + GitHub MCP) |
| `security-review-swarm.yaml` | Enterprise security review (threat / authz / secrets / blast) |
| `multi-team-release-train-swarm.yaml` | Cross-team release train go/no-go (platform / app / qa / sre) |
| `data-pipeline-qa-swarm.yaml` | Warehouse / feature-store QA (schema / freshness / PII / rollback) |
| `code-review-swarm.yaml` | Multi-agent code review (plan / security / tests / docs) |
| `support-triage-swarm.yaml` | Customer support ticket triage |
| `multi-wave-weave.yaml` | Multi-wave FanOut + weave policies + context_query across waves |

**Paths + tools:** with an active swarm run, bare paths resolve via ScopeRel to `runs/<turn-id>/work/…` (same as hoop `git_clone` / `fs_*`). Write finals with `artifact_write kind=out` → `runs/<turn-id>/out/`. Prefer `context_query` / `[context_digest]` for shared goal/plan; clone-based templates must not re-clone after the first worker lands `audit-target`. Nested hoop swarm: `samples/hoops/parallel-swarm-mode.yaml`.

See [../hoops/README.md](../hoops/README.md), [docs/site/samples.html](../../docs/site/samples.html), and [planning/tools_mcp.md](../../planning/tools_mcp.md).
