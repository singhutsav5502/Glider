# Routing and context

How Glider decides **local vs cloud/origin** for a request, how that decision
stays coherent across the follow-on RPCs one user turn generates, and the
context layer that makes both possible.

> Code: `internal/router/`, `internal/mitm/agent_fulfill_hub.go`,
> `internal/mitm/intercept.go`, `internal/contextgraph/`, `internal/contextkit/`.
> Config: `routing.*`, `transform.*`, `context.*` in `configs/glider.yaml`.

Consolidated 2026-07-31 from three separate docs (`smart_routing_and_local_tools.md`,
`routing_session_policy.md`, `context_management.md`) — they described one
subject from three angles and cross-referenced each other on nearly every point.

---

## 1. The problem routing solves

Routing on **context token count** alone is wrong in both directions:

- **Short ≠ simple.** A 2k-token "redesign this package" belongs on cloud.
- **Long ≠ complex.** A 12k-token paste plus "rename `foo` → `bar`" is local-safe.

So Glider routes on **task shape** (what's being asked, which tools it needs),
with the token ceiling demoted to a safety net rather than the primary signal.

## 2. Priority order

Highest wins. Applies to both paths; Path B (MITM) notes differ where marked.

| # | Layer | Effect |
|---|---|---|
| 1 | Explicit `/cloud` `/heavy` | Hard-force origin. Opens a cloud turn family. Never canned. |
| 2 | Explicit `/local` `/fast` | Hard-force local. Opens a local turn family. |
| 3 | Turn-family sticky | Correlated reply-summary / same-UUID RunSSE only (§3). |
| 4 | `composer_wrapup_origin` (prio 95) | Wrap-up chrome → origin, so post-grace summaries never leak local. |
| 5 | Tool-step re-decide | Child tool loops re-evaluate instead of inheriting parent forever (§4). |
| 6 | Classifier — tools present (~85) | → cloud when `tools_force_cloud` and not fully allowlisted. |
| 7 | Classifier — must-cloud (~80) | Architecture / migration / multi-file (`role: plan`). |
| 8 | Complexity → cloud (~75) | Opt-in, `routing.complexity.enabled`. |
| 9 | Classifier — small-local (~70) | Rename / explain / format / Q&A (`role: exec\|research`). |
| 10 | Starlark scripts | Custom rules. |
| 11 | Token ceiling (~10) | `context_size` safety net, not the primary signal. |
| 12 | Default | Cloud when unsure. Path B opaque → origin. |

Explicit flags are **TipTap-safe**: a mid-text or buried slash token still
matches (`HasCloudOverride` / `HasLocalOverride`), so a flag pasted inside a
larger message isn't silently ignored.

### Complexity scoring (opt-in)

| `routing.complexity_from` | Source |
|---|---|
| `heuristic` | Glider's own score: tool count, file-path mentions, prompt length, agent/plan/max-mode strings |
| `cursor` | Only `Metadata.CursorComplexity`, when present |
| `both` | Cursor if available, else heuristic |

Cursor's own envelopes do **not** currently expose complexity / `model_tier` /
`max_mode` in anything Glider parses (confirmed against real MITM dumps). The
vendor protos mention `max_mode` on `ModelDetails` / `RequestedModel`; if that
ever surfaces, call `router.TryAttachCursorComplexity(req, score, true)` in
`bidi_extract`/`decode` and set `complexity_from: both`. Starlark sees
`request.complexity_score` and `complexity_source`.

## 3. Turn-family sticky (not session-wide)

One user send generates several RPCs with **different request UUIDs** — the
answer stream, then chrome like "summarize the reply" and title generation.
Routing each independently means a cloud turn's own summary can get yanked
local mid-thought. Binding the whole *conversation* to cloud is the opposite
error: the next real question can never go local again.

So a decision binds a **turn family**: the deciding `BidiAppend`, its
correlated root `RunSSE`, and immediate chrome follow-ons within
`routing.turn_family_ttl` (default 90s).

| Inherits the family? | Example |
|---|---|
| Yes | `summarize the reply…`, title generation, `final summary`, `completed_subtitle` |
| Yes | Composer system chrome (`user_visible_*` / `high_level_summary` packs) — **fail-closed even if TTL expired** |
| Yes | Mid-turn tool-result packs (`call-…` ids) or short crumbs (`Hi!`) while StickyCloud is live |
| Yes | Any non-TipTap BidiAppend while StickyCloud is live — only an allowlisted *fresh* `tiptap_text` may re-decide |
| **No** | Next real user message (≥16 chars, `extractSource=tiptap_text`, no chrome/child markers) → **re-decides**, may go local |

Load-bearing rules:

- **Classifier decisions open a family too** (`decide_cloud` / `decide_local`),
  not just explicit flags — otherwise summaries after a heavy classifier-routed
  cloud turn get interrupted.
- **StickyLocal cannot downgrade a live StickyCloud** family.
- **Parent RunSSE holds the family open** (`BeginParentRun`/`EndParentRun` →
  `RunSSEOpen`/`RunSSEClose`), then renews TTL for wrap-up chrome — a wall-clock
  TTL alone expires mid-stream on a long answer.
- Sticky consults the **context graph** (`LiveCloudFamily` / `ResolveCloudSticky`),
  not only the TTL map — this is what fixes final-summary leaks when TipTap
  mis-extracts or the TTL lapses mid-stream.

### Must never fulfill locally

- Explicit `/cloud` / `/heavy` on this turn
- Turn-family cloud follow-on while the family is live
- Task/subagent children spawned mid-turn under StickyCloud (`bidi_sticky_cloud_child`)
- Child/tool-loop RunSSE on Path B (origin until the tool codec ships)
- Non-extractable BidiAppend
- `CompleteLocal` failure with canned off → origin fail-soft

## 4. Tool-step re-decide

```yaml
routing:
  turn_family_ttl: 90s
  tool_followup:
    enabled: true
    inherit_parent_default: true   # start from the parent's decision
    reevaluate: true               # allow local offload of safe tools
    local_tool_allowlist: ["read_file", "grep", "Glob", "list_dir", "codebase_search"]
    cloud_tool_denylist: ["Shell", "Write", "Delete", "ApplyPatch", "edit_file"]
```

| Setting | Meaning |
|---|---|
| `inherit_parent_default` | Start from the parent turn's cloud\|local |
| `reevaluate` | Let allowlisted tools prefer local even under a cloud parent |
| `local_tool_allowlist` | **All** tools in the step must match (exact or `path.Match`) |
| `cloud_tool_denylist` | **Any** match → cloud/origin |

**Path A** (gateway) honors this fully: an allowlisted-only tool set skips
`tools→cloud` and can run local. **Path B** decides, logs, and emits
`tool_followup_would_local` — but still relays to origin, because the child
RunSSE tool codec isn't finished. That gap is deliberate and visible in metrics
rather than silently mis-routed.

## 5. What locals actually receive

Local models must never be fed a full Composer mega-dump — they start riffing
on prior meta-chat instead of answering.

```text
Path B (MITM Agent RPC)
  BidiAppend context_envelope
    → ExtractBidiCompletionRequest   // 1× latest user TipTap turn, no tools
    → optional InjectEpisodeContext  // compressed prior episodes
    → sticky / DecideLocal
    → ArmLocal(offer.Request) | ArmOrigin
    → RunSSE Wait → CompleteLocal    // slim messages only

Path A (gateway :8080)
  POST /v1/chat/completions|responses   // full history + tools
    → route on the full body
    → if local: InjectEpisodeContext, then BoundLocalContext(latest_turn)
    → if cloud: full body to BYOK, never bounded
```

| `transform` knob | Default | Role |
|---|---|---|
| `local_context` | `latest_turn` | Bound Path A / StreamChat locals |
| `local_system_max_chars` | 4000 | Cap the leading system prompt |
| `local_episode_count` | 3 | Prior episodes injected as a preamble |
| `local_episode_max_chars` | 1500 | Cap that preamble |

`thresholds.max_local_context_tokens` is a **routing ceiling**, not a truncator;
`transform.trim_context` remains an optional middle-drop.

## 6. Local tooling (Path A)

Shipped: `CompletionRequest.Tools`/`ToolChoice`, `Message.ToolCalls`/`ToolCallID`,
Anthropic normalization (`tool_use` → `assistant.tool_calls`, `tool_result` →
`role=tool`), tool attachment for Ollama/vLLM/OpenAI, stream `tool_calls`
parsing (`ParseOpenAIStreamPayload`) and re-emission (`WriteChatSSE`/`WriteChatJSON`).

**Ollama limitation:** many local models can't tool-call at all. The backend
wraps that as `backend.ToolsUnsupportedError`, `FallbackChain` continues to BYOK
cloud, and the default `tools_force_cloud: true` keeps tool-heavy turns off
local unless every tool is allowlisted. The client still *executes* tools —
Glider only preserves definitions and call/result shapes.

## 7. Context graph

Sticky routing without a memory layer keeps failing the same way: chrome and
tool children arrive as new UUIDs, TipTap extract sees a short crumb, the
classifier says "small → local," and a cloud turn's wrap-up gets interrupted.
The graph turns *"guess from the latest prompt string"* into *"query turn family X."*

**Design: event log as source of truth + an ephemeral in-memory turn index** for
live sticky/analytics. Deliberately *not* a property-graph DB and *not* OpenTelemetry
— OTel is excellent for latency/SLO dashboards but is not LLM memory and still
needs a custom processor for sticky; a full graph DB is unjustified before
multi-worker swarms are product-default. Both remain viable complements later.

**Nodes:** `Turn`, `RequestUUID`, `RunSSE`, `BidiAppend`, `ToolCall`, `ModelRoute`
(+ `Message`/`Artifact` reserved).
**Edges:** `parent_of`, `caused`, `summarizes`, `tool_of`, `sticky_inherits`.

**Events** (append-only, all shipped): `TurnOpened`/`StickyBound`, `RouteDecided`,
`OriginPassthrough`, `FulfilledLocal`, `SummaryRequested`, `SubagentSpawned`,
`ToolStarted`/`ToolFinished`, `RunSSEOpen`/`RunSSEClose`, `BidiSeen`, `Error`,
`EpisodeMerged`, `LoopStarted`/`LoopTick`/`LoopStopped` — each with a timestamp,
an `actor` (`cloud`/`local`/`mitm`/`orch`), and optional `connect_session`.

**Storage:** hot in-memory index keyed by turn-family / request-uuid /
connect-session; warm JSONL at `~/.glider/context/events-YYYY-MM-DD.jsonl`,
replayed on startup (`context.warm_load_days`, default 2) and GC'd via
`context.retain_days` (default 14).

### Session memory

`contextkit.Store` is built in `cmd/glider` and shared by `PipelineCompleter`
(episode on local-fulfill success), `FanOutExecutor` (merged worker returns),
`loop.Manager` (per-tick checkpoint), and the dashboard.

```text
SessionState
  session_id
  active_overrides      // last /local|/cloud|/fast|/heavy
  last_decision         // RoutingDecision snapshot
  episodes[]            // ring buffer, N≈32
  spend_tokens / spend_cost
  loop_checkpoint?      // { goal, last_episode_id, eval_status, wake_reason, next_delay_s }
```

Keyed by Glider history session id (per process run); turn-family id rides on
`Episode.TurnID` when known.

## 8. APIs

| Endpoint | Purpose |
|---|---|
| `GET /api/context/recent?limit=&events=` | Recent turns + events + store stats |
| `GET /api/context/turns?limit=` | Recent turn projections |
| `GET /api/context/turns/{id}` | Full event list for a turn / request / session |
| `GET /api/context/episodes?session=&limit=` | Episode ring (omit session → all) |
| `GET /api/context/export?turn=&session=&events=` | Debug dump (graph + episodes) |
| `POST /api/context/prune?retain_days=14` | JSONL GC + memory ring pressure |
| `GET /api/metrics` | `distribution.{local_pct,cloud_pct,canned_pct}`, `tokens_saved_est` |
| `GET /api/mitm/debug/recent` | Same distribution + action counters + `context_turns` |

## 9. Analytics

Request-log actions (`Mode=mitm` for Path B):

| Action | Counts as | When |
|---|---|---|
| `origin_passthrough` | cloud | `/cloud`, turn-family cloud follow-on, or DecideLocal cloud |
| `local` | local | Successful `CompleteLocal` |
| `canned` | local | Opt-in canned RunSSE after CompleteLocal failure |
| `delegate` | — | Cross-CLI delegate dispatch (see `permission_relay_design.md`) |
| `error` | — | CompleteLocal failure before canned/origin |

Selected counters: `bidi_cloud_override`/`bidi_local_override` (explicit flags),
`bidi_sticky_cloud`/`bidi_sticky_local` (family follow-on),
`bidi_sticky_cloud_summary` (system summary chrome), `bidi_composer_wrapup`,
`bidi_sticky_cloud_child` (subagent held to origin),
`bidi_decide_cloud_family`/`bidi_decide_local_family`,
`tool_followup_would_local`/`tool_followup_origin`, `runsse_skip_tool_loop`.

The dashboard LOCAL/CLOUD tile shows `% local` / `% cloud`, appending
`· canned K%` when any canned responses occurred. **Cloud % includes
`origin_passthrough`**, so Path B origin turns are never hidden from the split.

## 10. Verifying behavior after a change

Restart with `agent_rpc_fulfill: true`, `composer_wrapup_origin` enabled, and
`routing.tool_followup.enabled: true`, then:

1. Heavy prompt with no flag → `bidi_decide_passthrough` + `bidi_decide_cloud_family`.
2. Its reply-summary / title → `bidi_sticky_cloud`, and **no** `runsse_local`.
3. `/cloud <question>`, then wrap-up chrome → `bidi_composer_wrapup` or
   `bidi_sticky_cloud_summary`; still no `runsse_local`.
4. Child tool step with an allowlisted name → `tool_followup_would_local`, still origin.
5. Fresh user message (`rename foo to bar`, no flag) → re-decides, often local.
6. `/cloud … through a subagent` → parent `bidi_cloud_override`, child
   `bidi_sticky_cloud_child`, no `runsse_local`.
7. `GET /api/context/turns/{cloudRoot}` lists the child request ids and stats.
8. Dashboard **Rules** shows `composer_wrapup_origin` at priority 95.

## 11. Known gaps

- **Path B child/tool-loop RunSSE stays origin** — decided and logged, not fulfilled locally, until the tool codec lands.
- **Episodes don't survive a process restart** — in-memory ring only; the graph JSONL is the warm event log.
- **Dashboard Rules UI can't edit the classifier block** — config file only.
- **No episode chip in the Overview UI** — the API is ready, the widget isn't.
- **Swarm/fan-out is stub-level** — `FanOutExecutor` + `contextkit` exist, no planner, no default fan-out rules.
- **Blackboard / property-graph storage deferred** until fan-out is product-default.
