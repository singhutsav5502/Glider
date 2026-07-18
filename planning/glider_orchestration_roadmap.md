# Glider orchestration roadmap (companion)

> Live canvas: open `glider-orchestration-roadmap` beside chat (Cursor canvases), or see the copy under this folder if present.
> Status snapshot: **2026-07-18 (late evening)**. Related: [smart_routing_and_local_tools.md](./smart_routing_and_local_tools.md), [routing_session_policy.md](./routing_session_policy.md), [swarm_orchestration.md](./swarm_orchestration.md).

## Honest maturity (code truth)

| Area | ~% | Notes |
|------|----|-------|
| Routing (explicit → classifier → Starlark → ceiling) | 85 | `/cloud` hard-force; classifier MVP + role hints |
| Path A gateway | 90 | Agent+tools; **stream tool_calls bridge shipped** |
| Path B MITM Agent | 40 | Text fulfill; child/tool RunSSE → origin + would_* |
| Path A local tools | 75 | Attach + SSE tool_calls; no Glider-side runners yet |
| Analytics LOCAL/CLOUD/CANNED % | 95 | Overview tile + bar; `GET /api/metrics` distribution + optional `context_routes` |
| Task classifier MVP | 85 | small-local / must-cloud / tools; explicit wins |
| Context management | 55 | `contextgraph` hybrid MVP; `contextkit` Episode stubs |
| Swarms | 40 | `internal/swarm` FanOut+Merge+Loop+HotSwap; FanOutExecutor wired; no planner |
| Hot-swap modules | 65 | Registry + fan_out Apply on Watch/Swap; backends/MITM still restart |
| Loop engineering | 15 | `LoopRunner` / `IntervalLoop` skeleton (not Cursor-integrated) |

## Shipped this track

1. **Analytics %** — local / cloud (`origin_passthrough`+`cloud`) / canned always on Overview; CLASS/role chips; `GET /api/metrics` (+ optional contextgraph `context_routes`).
2. **Path A tools** — first-class fields + **stream tool_calls → Cursor SSE**.
3. **Task classifier MVP** — + `InferTaskRole` (plan/exec/research).
4. **Tool-followup** — Path B would_local logging; Path A allowlist bypass.
5. **Context hybrid MVP** — `internal/contextgraph` (+ `contextkit` stubs).
6. **Swarm package** — `internal/swarm` (FanOut cancel, MergeResults, IntervalLoop, HotSwap Registry, Group).
7. **FanOut foundation** — `orchestration.fan_out` + `concurrency` channel sizes (default off).

## Gaps (do not claim done)

- Path B child RunSSE tool frames (codec)
- Wire Episode store into every pipeline fulfill (FanOut records; single-path still stub)
- Default rules emitting `StrategyFanOut`
- Classifier editor in Rules UI
- Planner / Slate-like thread-weaving
- Cursor `/loop` product integration (IntervalLoop is harness-only)
- Backend/MITM live hot-swap without restart

## Swarm package API (quick)

```go
swarm.FanOut(ctx, workers, opts)           // cancel-aware
swarm.MergeResults(results)                // Episode weave stub
swarm.DefaultSwarm{Opts}.Run(ctx, workers) // Swarm interface
swarm.IntervalLoop{}                       // LoopRunner
swarm.NewRegistry().BindProvider(p)        // Hot module Apply on Swap
swarm.WithContext(ctx)                     // errgroup-style Group
swarm.OptionsFromConfig(cfg.Orchestration) // backpressure sizes
```

## Restart notes

```powershell
$env:PATH = "$env:LOCALAPPDATA\go-sdk\go\bin;$env:PATH"
$env:GOROOT = "$env:LOCALAPPDATA\go-sdk\go"
cd D:\___repos\Glider
go build -o glider.exe ./cmd/glider
# stop old process, then:
.\glider.exe --config configs\glider.yaml
```

Verify: dashboard Overview **LOCAL / CLOUD / CANNED** + bar; `curl http://localhost:8081/api/metrics` shows `distribution.local_pct|cloud_pct|canned_pct` (+ optional `context_routes`).
