# Sample Loop Engineering hoops + swarm templates

Runnable YAML for local smoke tests and showcase demos. See [docs/site/samples.html](../../docs/site/samples.html).

Enterprise strategy: [planning/enterprise_orchestrator_mvp.md](../../planning/enterprise_orchestrator_mvp.md).  
Graph gaps: [planning/graph_feature_gaps.md](../../planning/graph_feature_gaps.md).  
Tools / ScopeRel / context: [planning/tools_mcp.md](../../planning/tools_mcp.md).

## Prerequisites

```powershell
ollama serve
.\glider.exe --config configs\glider.yaml
# Dashboard http://127.0.0.1:8081
```

## Enterprise showcase hoops (substantial)

| File | Use case |
|------|----------|
| `enterprise-incident-command.yaml` | SEV command channel: context seed + 3 parallel workstreams + artifact_write + safety critic + HITL |
| `compliance-evidence-pack.yaml` | SOC2 evidence pack + context seed + parallel pack writers + auditor critic + HITL |
| `escalation-policy.yaml` | On-call escalation policy (severity matrix + paging ladder) + context seed + HITL |
| `clone-repo-security-audit.yaml` | Clone Unbrokify + verify + `kind: context` + parallel audit (no re-clone) + HITL + git_clone / GitHub MCP |
| `parallel-swarm-mode.yaml` | Tiny demo: `kind: context` seed + `parallel_mode: swarm` (nested Runner) |
| `incident-triage.yaml` | Lighter on-call runbook (context seed + parallel actors + critic + HITL) |
| `research-synthesize.yaml` | Research → context seed → synthesize → critique (`web_search` / `web_fetch` / `artifact_write`) |
| `feeds-edge-mvp.yaml` | Tiny demo: `graph_edges kind=feeds` seeds producer summary into consumer `FEEDS:` prompt block |
| `release-changelog.yaml` | Release checklist / changelog + context seed + artifact_write + HITL |

**Clone audit tip:** rebuild/restart Glider after pulling tool-loop fixes. Blind pre-pass uses workspace `.` only (never the hoop goal). `git_clone` and `fs_*` share run ScopeRel — bare `audit-target` → `runs/<hoop-id>/work/audit-target` (for this sample: `runs/clone-repo-security-audit/work/audit-target`). After `kind: context` (`context_seed`), parallel workers should `context_query key=clone_path` (or `goal` / `plan` / `file-tree`) and must not re-clone (undeclared `git_clone` rejected). Prefer `artifact_write kind=out` for reports under `runs/<hoop-id>/out/`. MCP `*` expands to a catalog probe, never `CallTool("*")`. Tool loop budget: sequential stages MaxSteps=20, parallel workers=28 (`toolLoopMaxSteps*` in `internal/loop/cycle.go`). Critic defaults to no tools (must emit `SCORE:` / `REASON:`). Local Complete timeout: `thresholds.request_timeout` (default `10m`).

**Parallel swarm tip:** `parallel-swarm-mode.yaml` needs `orchestration.swarm.enabled: true` (default in `glider.yaml`). Prefer local models; no clone required. Showcase hoops with parallel actors comment `# parallel_mode: swarm` as an optional nested-runner switch.

## Smoke hoops

`hello-critic.yaml`, `explain-snippet.yaml`, `rename-suggest.yaml`, `review-lite.yaml`, `summarize-notes.yaml`

## Swarm templates (`samples/swarms/`)

| File | Roles |
|------|-------|
| `security-review-swarm.yaml` | threat / authz / secrets / blast |
| `repo-audit-swarm.yaml` | research / exec / plan — quality + security + secrets (git_clone + ScopeRel + artifacts) |
| `multi-team-release-train-swarm.yaml` | platform / app / qa / sre |
| `data-pipeline-qa-swarm.yaml` | schema / freshness / pii / rollback |
| `code-review-swarm.yaml` | plan / security / tests / docs |
| `support-triage-swarm.yaml` | plan / exec / research |
| `swarm-dual-review.yaml` (under hoops/) | plan / exec dual review |

All swarm templates declare `tools:` (`artifact_write`, `context_query`, `fs_*`, plus clone/grep/web where relevant) and prompt for `runs/<turn-id>/work|out/` via ScopeRel.

## Load / run

```powershell
# Seed ALL sample hoops + swarm templates (idempotent; does not Start)
powershell -File scripts\seed-samples.ps1
# Optional: start every hoop after load
powershell -File scripts\seed-samples.ps1 -Start

# Create one hoop via API (mirrors YAML)
go run ./scripts/loadhoop -file samples/hoops/enterprise-incident-command.yaml -start

# Install hoop directory only
go run ./scripts/loadhoop -dir samples/hoops

# Single sample: load + start
powershell -File scripts\run-sample-hoop.ps1 -Name enterprise-incident-command
```

## Live agent logs (per instance)

1. Dashboard → Hoops: click a hoop card (binds log stream to that hoop id).
2. Start the hoop — log timeline resets for that instance.
3. Graph editor / Hoops "Agent log" panels show **only** that hoop's lines.
4. Run swarm — stream switches to that swarm `turn_id`.
5. API: `GET /api/agent-logs?scope=hoop&id=<id>` or `scope=swarm&id=<turn_id>`
6. WebSocket `/ws` events `type=agent_log` include `scope` + `instance_id`.
