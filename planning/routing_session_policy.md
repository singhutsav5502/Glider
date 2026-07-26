# Path B routing turn-family & tool-followup policy

> Short, actionable. Code: `internal/mitm/agent_fulfill_hub.go`, `internal/mitm/intercept.go`,
> `internal/router/tool_followup.go`, `internal/config/config.go` (`routing.turn_family_ttl`, `routing.tool_followup`).
> Related: [smart_routing_and_local_tools.md](./smart_routing_and_local_tools.md),
> [swarm_orchestration.md](./Depreceated/swarm_orchestration.md),
> [context_management.md](./context_management.md),
> [cursor_agent_protocol_interception.md](./cursor_agent_protocol_interception.md).

## Goal

1. Fix the Path B leak where **reply-summary / title** RPCs after a **cloud** turn get local-fulfilled (interrupted), **without** locking the whole Composer chat to cloud.
2. After a heavy parent turn on cloud, **child tool loops** must **re-decide** (allowlist/denylist) — not stay stuck on parent cloud forever.
3. Conversation-wide sticky is wrong — after cloud (explicit or classifier), the **next user message** must re-decide via classifier / thresholds / a new flag.

## Worked example (this methodology)

| Step | What happens | Route |
|------|----------------|-------|
| 1 | User: `architect the module boundaries` (**no** `/cloud` or `/local`) | Classifier / must-cloud → **cloud**; opens turn-family (`decide_cloud`, TTL) |
| 2 | Root `RunSSE` for that request UUID | Origin / ArmOrigin (same-UUID sticky) |
| 3 | Chrome: `summarize the reply…` / title gen (different UUID, follow-on shape) | **Turn-family sticky cloud** — no interrupt |
| 4 | Cloud emits tools → child `RunSSE` (`X-Parent-Agent-Tool-Call-Id`) | **Tool-step re-decide**: allowlist `read_file`/`grep` → `tool_followup_would_local` (Path B still **origin** until tool codec); denylist `Shell`/`Write` → origin |
| 5 | Next user: `rename foo to bar` (no flag, not follow-on) | **Re-decide** — may go local; not sticky cloud |

Path A (gateway): allowlisted-only `tools[]` skip `tools→cloud` and can route local when other rules agree.

## Decision layers (priority)

1. **Explicit** `/cloud` `/heavy` or `/local` `/fast` on **current user turn** text (TipTap-safe). Hard-force; `/cloud` never canned.
2. **`composer_wrapup_origin`** (priority 95, enabled by default) — first-class routing rule + MITM `ArmOrigin` belt. Matches when StickyCloud / graph cloud family is live for a **non-fresh TipTap** follow-on, **or** body/hint is wrap-up chrome (`user_visible_high_level_summary`, `high_level_summary`, title/summary packs, empty TipTap, printable_hint titles like `Friday stock market close`). Beats Small Context Local so expired-grace wrap-ups never `runsse_local`.
3. **Turn-family binding** from the *decision* (explicit **or** `DecideLocal` cloud|local): bind request UUID + short TTL for **correlated reply-summary / same-UUID RunSSE only**.
4. **Tool-step re-decide** (NEW): child tool loops are **not** blindly stuck on parent cloud. Each step uses:
   - `parent_route` (cloud|local from live turn family)
   - tool name / risk class (allowlist / denylist)
   - payload size / estimated tokens (reserved signal; size gates can be added later)
   - config allowlist / denylist
5. Classifier / Starlark / thresholds as today
6. Default **cloud** for Path B opaque when unsure

## Explicit flags (always win)

| Flag | Effect |
|------|--------|
| `/cloud`, `/heavy` | Hard-force **origin** for this turn. Opens a **cloud turn family**. Never canned. |
| `/local`, `/fast` | Hard-force **local** for this turn. Opens a **local turn family**. |
| TipTap-safe | Mid-text / buried slash tokens match (`HasCloudOverride` / `HasLocalOverride`). |

## Turn-family sticky (not session-wide)

A decision binds only that **turn family**:

1. The `BidiAppend` extract that produced the decision (explicit or classifier)
2. Correlated root `RunSSE` for that request UUID
3. **Immediate chrome follow-ons** only — reply-summary / title / similar prompts (`IsTurnFollowOn`) within `routing.turn_family_ttl` (default 90s)

| Inherits family? | Example |
|------------------|---------|
| Yes | `summarize the reply…`, `generate a title…`, `final summary`, `one-sentence` / `executive summary`, `completed_subtitle` / wrap-up |
| Yes | Composer system chrome: `user_visible_*` / `high_level_summary` packs (`IsSystemSummaryChrome` → metric `bidi_sticky_cloud_summary`) — **fail-closed even if StickyCloud TTL expired** (never `runsse_local`) |
| Yes | Mid-turn tool-result packs with `call-…` ids or short crumbs (`Hi!`) while StickyCloud live |
| Yes (deny-local default) | **Any** non-TipTap Bidi while StickyCloud live (printable_hint titles, empty extract, system-only envelopes) — only allowlisted fresh `tiptap_text` may re-decide (`IsAllowlistedFreshTipTapUser`; body TipTap history alone does not count) |
| No | Next TipTap user msg: `rename foo`, `fix the bug` (≥16 chars, `extractSource=tiptap_text`, no chrome/child) |

**StickyCloud deny-local rule (P0):** while a cloud family is live (TTL map **or** context graph), default is **origin**. Dump markers that must stick: title crumbs like `Delhi weather today` / `Refresh planning docs to latest` plus `user_visible_high_level_summary`.

- **Classifier / token decisions DO open a turn family** (`decide_cloud` / `decide_local`) so summaries after a heavy cloud turn are not interrupted.
- **StickyLocal cannot downgrade a live StickyCloud** family (mid-turn mis-routes used to kill `/cloud` sticky).
- **Parent RunSSE** calls `BeginParentRun` / `EndParentRun` (`RunSSEOpen`/`RunSSEClose` on the context graph) so family stays live for the full parent stream, then renews TTL for chrome wrap-up.
- Sticky/summary/subagent inheritance **consults the context graph** (`LiveCloudFamily` / `ResolveCloudSticky`), not only the wall-clock TTL map — fixes final-summary → local leaks when TipTap mis-extracts or TTL expires mid-stream.
- Next TipTap user message **without** a flag → **re-decide** (may go local after a cloud turn).
- Empty / expired family → classifier (fail-soft; never break origin), except **system chrome** which always ArmOrigin. Context graph: [context_management.md](./context_management.md).

## Tool-step re-decide (`routing.tool_followup`)

```yaml
routing:
  turn_family_ttl: 90s
  tool_followup:
    enabled: true
    inherit_parent_default: true   # start from parent decision
    reevaluate: true               # allow local offload of safe tools
    local_tool_allowlist: ["read_file", "grep", "Glob", "list_dir", "codebase_search"]
    cloud_tool_denylist: ["Shell", "Write", "Delete", "ApplyPatch", "edit_file"]
```

| Setting | Meaning |
|---------|---------|
| `inherit_parent_default` | Start from parent turn cloud\|local |
| `reevaluate` | Allow allowlisted tools to prefer local even if parent was cloud |
| `local_tool_allowlist` | All tools in the step must match (exact or `path.Match`) |
| `cloud_tool_denylist` | Any match → cloud/origin |

### Path B behavior today

- Parent heavy → cloud: `ArmOrigin` / passthrough for root RunSSE
- Sticky only for same-turn summaries — **not** whole chat
- Child tool RunSSE: if reevaluate says local → **decide + log + metric** `tool_followup_would_local`, **still origin** until Path B tool codec is ready
- Gateway Path A: allowlisted tools can skip tools→cloud and route locally when applicable
- `/cloud` hard force still wins; never canned on cloud

## Must never be local (within a cloud turn family)

- Explicit `/cloud` / `/heavy` on this turn
- Turn-family **cloud** follow-on (summary / title) while family TTL live
- **Task / subagent children** spawned mid-turn (prompt like `Say hi via subagent`, or `call-…` in pack / parent headers) while StickyCloud is live — they look like new root RunSSE pairs but must stay origin (`bidi_sticky_cloud_child`)
- Child / tool-loop RunSSE **fulfill** on Path B (origin until codec; may still *prefer* local in metrics)
- Non-extractable BidiAppend → origin
- CompleteLocal failure when canned is **off** → origin fail-soft

## TipTap extract (fulfill messages)

Path B extract uses **latest user turn only** (`LatestUserTurnText`): prefers the newest slash-command segment; otherwise the last non-assistant node. Do **not** join full Composer history into `CompletionRequest` (avoids local models riffing on prior “Hi — I’m Auto…” / meta-chat).

### How context reaches locals

```
Path B: BidiAppend context_envelope
      → ExtractBidiCompletionRequest  (1× user TipTap turn; no tools)
      → sticky / DecideLocal
      → ArmLocal(offer.Request) | ArmOrigin
      → RunSSE Wait → CompleteLocal(offer.Request)   // slim messages only

Path A: POST /v1/chat/completions|responses  (full Cursor history + tools)
      → Route on full body
      → if local: BoundLocalContext(latest_turn) then execute
      → if cloud: full body to BYOK (no BoundLocalContext)
```

`transform.local_context: latest_turn` (default in `configs/glider.yaml`) bounds Path A / StreamChat locals to leading system (≤ `local_system_max_chars`) + latest user turn (+ tool-loop tail). Path B extract is already single-turn — BoundLocalContext is a no-op. StickyCloud deny-local is unchanged (origin before CompleteLocal).

## Priority order (Path B BidiAppend)

1. **Explicit** `/cloud` \| `/heavy`
2. **Explicit** `/local` \| `/fast`
3. **Turn-family sticky** (StickyCloud / graph live → origin for non-fresh TipTap)
4. **`composer_wrapup_origin`** (wrap-up chrome / last-cloud crumb → origin; also DecideLocal priority 95)
5. **Classifier** / Starlark / token rules (`DecideLocal`) → opens turn family
6. Unknown / error → **origin**

## Analytics definitions

Overview request-log actions (`Mode=mitm` for Path B):

| Action | Route | When |
|--------|-------|------|
| `origin_passthrough` | `cloud` | `/cloud`, turn-family cloud follow-on, or DecideLocal cloud |
| `local` | `local` | Successful local fulfill (`CompleteLocal`) |
| `canned` | `local` | Opt-in canned RunSSE after CompleteLocal failure |
| `error` | — | CompleteLocal failure before canned/origin |

IncAction (selected):

| Metric | Meaning |
|--------|---------|
| `bidi_cloud_override` / `bidi_local_override` | Explicit flags |
| `bidi_sticky_cloud` / `bidi_sticky_local` | Turn-family follow-on |
| `bidi_sticky_cloud_summary` | Composer `user_visible_high_level_summary` / system summary chrome |
| `bidi_composer_wrapup` | Rule `composer_wrapup_origin` / wrap-up ArmOrigin (incl. post-grace) |
| `bidi_sticky_cloud_child` | Task/subagent child stayed origin under StickyCloud |
| `bidi_decide_cloud_family` / `bidi_decide_local_family` | DecideLocal opened family |
| `tool_followup_would_local` | Child step would be local (Path B still origin) |
| `tool_followup_origin` | Child step stays origin by policy |
| `runsse_skip_tool_loop` | Child RunSSE not fulfilled locally |

Dashboard **LOCAL / CLOUD** tile: `% local` / `% cloud`; if any `canned`, append `· canned K%`.
**Cloud % includes `origin_passthrough`** so Path B Agent origin turns are never hidden.

APIs:
- `GET /api/metrics` → `distribution.{local_pct,cloud_pct,canned_pct}` + `tokens_saved_est`
- `GET /api/mitm/debug/recent` → same `distribution` plus action counters
- Session aggregates → `distribution` from action_counts

## Operator retest

1. Rebuild / restart with `agent_rpc_fulfill: true`, `composer_wrapup_origin` enabled, and `routing.tool_followup.enabled: true`.
2. Heavy prompt **without** `/cloud` → expect `bidi_decide_passthrough` + `bidi_decide_cloud_family`.
3. Reply-summary / title **without** flag → `bidi_sticky_cloud`; **no** `runsse_local` for that follow-on.
4. `/cloud hello hows the stock market today` → origin; later `Friday stock market close` / `user_visible_high_level_summary` → `bidi_composer_wrapup` or `bidi_sticky_cloud_summary`; **no** `runsse_local`.
5. Child tool with allowlisted name in body/header → `tool_followup_would_local` but origin (handled=false).
6. Next user message `rename foo` (fresh TipTap) → re-decide (often local); **not** sticky wrap-up.
7. `/cloud …` → hard origin; never canned.
8. `/cloud … through a subagent` → parent `bidi_cloud_override`; child `Say hi via subagent` → `bidi_sticky_cloud_child` + **no** `runsse_local`.
9. Dashboard **Rules** shows `composer_wrapup_origin` (priority 95, cloud).
