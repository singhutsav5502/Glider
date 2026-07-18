# Context management for Glider orchestrator

> Status **2026-07-18**. Code: `internal/contextgraph` (**hybrid MVP shipped**),
> sticky consumers in `internal/mitm/agent_fulfill_hub.go` + `intercept.go`,
> orchestrator emits `RouteDecided` / fulfill / error via `PipelineCompleter.Graph`.
> Related: [routing_session_policy.md](./routing_session_policy.md),
> [swarm_orchestration.md](./swarm_orchestration.md).
> **Archival note:** longer “context + swarm architecture” prose was merged here + into swarm_orchestration (2026-07-18).

## Why this exists

Path B sticky routing without a memory layer keeps failing in the same way:

1. `/cloud` opens a turn family.
2. Mid-turn children / tool-result packs / **final summary / title** arrive as **new request UUIDs**.
3. TipTap extract often sees a short crumb (`Hi!`) instead of the chrome prompt.
4. Classifier says **Small Context Local** → `runsse_local` — wrong for a cloud turn wrap-up.
5. Wall-clock `turn_family_ttl` can expire while the **parent RunSSE is still streaming**.

A context layer turns “guess from the latest prompt string” into “query turn family X”.

---

## Option 1 (preferred): Context graph + event log — **SHIPPED MVP**

### Nodes

| Node | Meaning |
|------|---------|
| `Turn` | One user send + its chrome/tool children (turn family) |
| `RequestUUID` | Cursor corr id (BidiAppend / RunSSE) |
| `RunSSE` | Streaming answer channel |
| `BidiAppend` | Prompt / context pack append |
| `ToolCall` | `call-…` id mid-turn |
| `ModelRoute` | `cloud` \| `local` decision |
| `Message` / `Artifact` | Optional later (swarm episodes) |

### Edges

`parent_of`, `caused`, `summarizes`, `tool_of`, `sticky_inherits`

### Event log (append-only source of truth)

| Event | When | Status |
|-------|------|--------|
| `TurnOpened` / `StickyBound` | Explicit `/cloud`\|`/local` or DecideLocal opens family | [x] |
| `RouteDecided` | Route chip recorded (MITM + orchestrator) | [x] |
| `OriginPassthrough` | Path B stayed on Cursor origin | [x] |
| `FulfilledLocal` | RunSSE local/canned write | [x] |
| `SummaryRequested` | Chrome summary/title follow-on detected | [x] |
| `SubagentSpawned` | Task/subagent child under StickyCloud | [x] |
| `ToolStarted` / `ToolFinished` | Child tool-loop re-decide (still origin) | [x] |
| `RunSSEOpen` / `RunSSEClose` | Parent RunSSE in-flight / ended (+ grace) | [x] |
| `BidiSeen` | Every BidiAppend extract | [x] |
| `Error` | DecideLocal / CompleteLocal / encode failures | [x] |
| `EpisodeMerged` | Local fulfill / fan-out merge / loop tick | [x] |
| `LoopStarted` / `LoopTick` / `LoopStopped` | Glider-owned loops | [x] |

Timestamps + `actor` (`cloud`\|`local`\|`mitm`\|`orch`) + optional `connect_session`.

### Query

| Endpoint | Purpose |
|----------|---------|
| `GET /api/context/recent?limit=20&events=50` | Recent turns + recent events + **store stats** |
| `GET /api/context/turns?limit=20` | Recent turn projections (+ stats) |
| `GET /api/context/turns/{id}` | Full event list for turn / request UUID / session key (+ turn stats) |
| `GET /api/mitm/debug/recent` | Includes `context_turns` (last 10) |

### Storage

- Hot: in-memory turn index (`internal/contextgraph.Store`) keyed by **turn_family**, **request_uuid**, **connect_session**
- Warm: JSONL under `~/.glider/context/events-YYYY-MM-DD.jsonl`
- Cold later: sqlite / property graph DB (deferred)

---

## Option 2: Linear session transcript

Ordered spans per Composer session (`[user] → [assistant] → [summary]`), no graph.

| Pros | Cons |
|------|------|
| Fastest to ship | Weak tool/swarm queries (“all call-… under this /cloud”) |
| Easy to dump | Sticky still needs ad-hoc heuristics |
| Good for replay UI | Hard to merge fan-out workers |

**Use when:** debug-only history; not as the sticky source of truth.

---

## Option 3: OpenTelemetry-style traces

Spans + attributes (`turn.id`, `route`, `rpc=BidiAppend`).

| Pros | Cons |
|------|------|
| Ops-native, great concurrency | Not “LLM memory”; poor episode compaction |
| Existing collectors (Jaeger/OTLP) | Sticky still needs a custom processor |
| Excellent latency graphs | Heavier dep surface for Glider MVP |

**Use when:** production SLOs / dashboards; complement (not replace) the event log.

---

## Option 4: Mailbox / blackboard (Slate-like)

Shared state keys + watchers (`scratch[corr_id].episodes[]`).

| Pros | Cons |
|------|------|
| Natural for swarms | Needs careful locking / TTL GC |
| Workers don’t peer-message | Easy to overgrow into a second agent runtime |
| Matches Slate “thread weaving” | Overkill before FanOut is default-on |

**Use when:** multi-worker swarms land; store episodes **on top of** the event log.

---

## Recommendation — hybrid MVP (**shipped**)

**Event log = source of truth** + **ephemeral in-memory turn graph** for live MITM sticky/analytics.

- [x] Do **not** build a full property-graph DB yet.
- [x] Do **not** replace routing with OTel alone.
- [x] Wire sticky/summary/subagent decisions to consult the turn index (`ShouldStickyCloudOrigin` + `contextgraph.LiveCloudFamily` / `ResolveCloudSticky`) — **not only the TTL map**.
- [ ] Defer blackboard keys until `FanOutExecutor` is productized.

This unblocks:

1. **Correct `/cloud` wrap-up** — final summary inherits cloud for parent TTL + parent-run grace **even when TipTap mis-extracts** and when the TTL map was wiped while `RunSSEOpen` is live on the graph.
2. **Swarm/loop** — fan-out nodes emit `ToolStarted` / merge events onto the same `TurnID`; loop checkpoints point at last episode event id.
3. **Analytics** — “all events for turn family X” without scraping mitm-debug dumps.

### Sticky / summary fixes (shipped)

- [x] Broader `IsTurnFollowOn` (final/executive/one-sentence/completed_subtitle/wrap-up).
- [x] `IsSystemSummaryChrome` for `user_visible_high_level_summary` / high-level summary packs (`bidi_sticky_cloud_summary`).
- [x] Tool-result packs: `call-…` scan up to 4 MiB; short crumbs under StickyCloud stay origin.
- [x] `StickyLocal` cannot downgrade a live `StickyCloud` family.
- [x] `BeginParentRun` / `EndParentRun` → `RunSSEOpen` / `RunSSEClose` keep family live while parent RunSSE streams, then renew TTL for chrome wrap-up.
- [x] Graph correlation after TTL map clear: live open-run still sticky for final summary.
- [x] Concurrent append tests + JSONL warm store.

### Retest checklist (`/cloud` + final summary)

1. `/cloud …` → `BidiSeen` + `TurnOpened` + `RouteDecided(cloud)` + `OriginPassthrough`.
2. Parent `RunSSE` → `RunSSEOpen`; stream ends → `RunSSEClose` (+ grace).
3. Chrome `final summary` / `completed_subtitle` (new UUID, short TipTap crumb) → `SummaryRequested` + `StickyBound` + **no** `runsse_local`.
4. `GET /api/context/turns/{cloudRoot}` shows child request ids + stats.
5. `GET /api/context/recent` shows `stats.cloud_turns` / `by_kind`.

---

## Swarm / loop path

```text
TurnOpened(cloud)
  ├─ RunSSEOpen
  ├─ StickyBound(child ToolCall) … FanOut workers append ToolStarted / EpisodeMerged
  ├─ SummaryRequested (final summary) → OriginPassthrough
  └─ RunSSEClose → grace TTL
LoopCheckpoint → last EpisodeMerged event id on this Turn
```

Shared graph means workers never invent a parallel sticky story; the orchestrator merges by `TurnID`.

---

## Session memory (wired MVP)

`contextkit.Store` is constructed in `cmd/glider` and shared by:

| Consumer | Behavior |
|----------|----------|
| `PipelineCompleter` | On local fulfill success → `RecordEpisode` + graph `EpisodeMerged` |
| `FanOutExecutor` | Merged worker return → episode + optional `EpisodeMerged` |
| `loop.Manager` | Each tick → episode + `SetLoop` checkpoint + `EpisodeMerged` |
| Dashboard | `GET /api/context/episodes`, export, prune |

Target `SessionState` shape (live):

```text
SessionState
  session_id
  active_overrides          // last /local|/cloud|/fast|/heavy
  last_decision             // RoutingDecision snapshot
  episodes[]                // ring buffer, N≈32
  spend_tokens / spend_cost
  loop_checkpoint?          // { goal, last_episode_id, eval_status, wake_reason, next_delay_s }
```

Key by Glider history session id (process run). Turn family id is stored on `Episode.TurnID` when known.

### Local context → Ollama (bounded)

```text
Path B (MITM Agent text)
  ExtractBidiCompletionRequest
    → one TipTap latest-turn user message (no tools, no full envelope)
    → optional InjectEpisodeContext (compressed prior episodes)
    → DecideLocal / CompleteLocal
    → BoundLocalContext is a no-op on single-user shape

Path A (gateway :8080)
  full body used for routing
  when Target=local:
    InjectEpisodeContext (≤ local_episode_count)
    BoundLocalContext(latest_turn)  // system cap + latest user (+ tool-loop)
    Execute → Ollama
  cloud / sticky origin: never BoundLocalContext
```

Config (`transform`):

| Knob | Default | Role |
|------|---------|------|
| `local_context` | `latest_turn` | Bound Path A / StreamChat locals |
| `local_system_max_chars` | 4000 | Cap leading system |
| `local_episode_count` | 3 (intro yaml) | Inject prior episodes as system preamble |
| `local_episode_max_chars` | 1500 | Cap preamble |

**Do not** feed TipTap mega-dumps to locals — Path B extract + Path A `latest_turn` prevent that.

### Query / retention / export

| Endpoint | Purpose |
|----------|---------|
| `GET /api/context/recent?limit=20&events=50` | Recent turns + events + store stats |
| `GET /api/context/turns?limit=20` | Recent turn projections |
| `GET /api/context/turns/{id}` | Full event list for turn / request / session |
| `GET /api/context/episodes?session=&limit=` | Episode ring (omit session → all) |
| `GET /api/context/export?turn=&session=&events=` | Debug dump (graph + episodes) |
| `POST /api/context/prune?retain_days=14` | Disk JSONL GC + memory ring pressure + empty sessions |

Warm store: `~/.glider/context/events-YYYY-MM-DD.jsonl` — **replayed on startup** (`context.warm_load_days`, default 2). Disk GC via `context.retain_days` (default 14) at start + prune API.

### Pure local + context

See `configs/glider.local.yaml`. Locals get bounded context + episodes; Ollama health gate; clear errors when `mitm.origin_on_local_error: false`. Gateway-only: `mitm.enabled: false` — loops and `/v1` need no Cursor subscription (`cus-` optional for IDE Override).

### Remaining gaps

- Episode persistence across process restarts (in-memory ring only; graph JSONL is the warm event log)
- Overview UI chip for episodes (API ready)
- Path B tool-loop still origin (not episode/local fulfill)
- Blackboard / property-graph DB deferred until FanOut is product-default
