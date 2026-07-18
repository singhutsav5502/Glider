# Sample Loop Engineering hoops + swarm templates

Runnable YAML for local smoke tests and showcase demos. See [docs/site/samples.html](../../docs/site/samples.html).

Enterprise strategy: [planning/enterprise_orchestrator_mvp.md](../../planning/enterprise_orchestrator_mvp.md).  
Graph gaps: [planning/graph_feature_gaps.md](../../planning/graph_feature_gaps.md).

## Prerequisites

```powershell
ollama serve
.\glider.exe --config configs\glider.yaml
# Dashboard http://127.0.0.1:8081
```

## Enterprise showcase hoops (substantial)

| File | Use case |
|------|----------|
| `enterprise-incident-command.yaml` | SEV command channel: 3 parallel workstreams + safety critic + feedback |
| `compliance-evidence-pack.yaml` | SOC2 change-control evidence pack + auditor critic |
| `escalation-policy.yaml` | On-call escalation policy (severity matrix + paging ladder) |
| `incident-triage.yaml` | Lighter on-call runbook (parallel actors + critic) |
| `research-synthesize.yaml` | Research → synthesize → critique |
| `release-changelog.yaml` | Release checklist / changelog |

## Smoke hoops

`hello-critic.yaml`, `explain-snippet.yaml`, `rename-suggest.yaml`, `review-lite.yaml`, `summarize-notes.yaml`

## Swarm templates (`samples/swarms/`)

| File | Roles |
|------|-------|
| `security-review-swarm.yaml` | threat / authz / secrets / blast |
| `multi-team-release-train-swarm.yaml` | platform / app / qa / sre |
| `data-pipeline-qa-swarm.yaml` | schema / freshness / pii / rollback |
| `code-review-swarm.yaml` | plan / security / tests / docs |
| `support-triage-swarm.yaml` | plan / exec / research |

## Load / run

```powershell
# Create via API (mirrors YAML)
go run ./scripts/loadhoop -file samples/hoops/enterprise-incident-command.yaml -start

# Install directory
go run ./scripts/loadhoop -dir samples/hoops

# Swarm template into ~/.glider/hoops
Copy-Item samples\swarms\*.yaml $env:USERPROFILE\.glider\hoops\
# or POST /api/swarm/templates from Dashboard

powershell -File scripts\run-sample-hoop.ps1 -Name enterprise-incident-command
```

## Live agent logs (per instance)

1. Dashboard → Hoops: click a hoop card (binds log stream to that hoop id).
2. Start the hoop — log timeline resets for that instance.
3. Graph editor / Hoops "Agent log" panels show **only** that hoop's lines.
4. Run swarm — stream switches to that swarm `turn_id`.
5. API: `GET /api/agent-logs?scope=hoop&id=<id>` or `scope=swarm&id=<turn_id>`
6. WebSocket `/ws` events `type=agent_log` include `scope` + `instance_id`.
