# Native Glider Language (NGL) — cross-CLI orchestration vision

The product vision behind `internal/ngl`: a user drives **one** CLI — `claude`, `cursor-agent`, or `agy`, whichever they already have installed — and stays there for the whole session. Behind that single front door, Glider can transparently run sub-tasks through *other* agent CLIs and fold the result back into the user's session so it reads as if the front CLI did it natively — right tool names, right diff shape, right streaming feel.

**What's actually built today** is the explicit-flag slice of this: a user typing `/agy do X` in any front gets that prompt run headless through `agy`, with permission prompts relayed back (`permission_relay_design.md`) and edits rendered through NGL's `EditViews` (`adapter_boundary.md`). What's below is the fuller vision this slice is the first piece of — an automatic router that decides *which* vendor should handle a task without the user naming one, and delegation triggered by the front CLI's own model rather than an explicit flag. Neither of those is built. This doc stays honest about which parts are live code and which are still design.

Protocol research this depends on: [agent_cli_interop.md](agent_cli_interop.md). Built execution layer: [permission_relay_design.md](permission_relay_design.md). Built interception layer: [transparent_redirector_design.md](transparent_redirector_design.md).

## NGL must not privilege any one CLI as "the" front

Any of the three vendors must be able to sit in front, and any must be able to delegate to either of the others. This ruled out an early draft that modeled the delegation trigger on Claude's own `Agent` subagent tool — a real, working mechanism for Claude and Cursor, but not one `agy`'s live toolset actually exposes (see §"Two triggers" below), which would have quietly made the design asymmetric across vendors.

## 1. NGL, named and versioned

`Turn{Vendor, Raw, Parts}`, `Part{Kind, Text, ToolCall, ToolResult, ReasoningToken}`, and `EditViews` (range-replace / hunks / unified-text / before-after + a converter registry) are **NGL** — implemented in `internal/ngl`, per-vendor parsers in `adapter_claude.go`/`adapter_cursor.go`/`adapter_agy.go`, vendor packs as data (`vendorpacks/{claude,cursor-agent,agy}.yaml`) rather than Go switch statements. See `adapter_boundary.md` for the interface contract and what a 4th vendor touches.

- Namespace convention, matching how the vendors themselves name things (`agent.v1`, `aiserver.v1`): **`glider.native.v1`**.
- `Turn` is the unit that crosses every boundary: front adapter → orchestrator → delegate adapter → orchestrator → front adapter.
- Versioned independently of Glider's binary release — NGL growing a field for a 4th vendor's shape shouldn't require every adapter to redeploy in lockstep.

## 2. Two session-concurrency models, one interface

| CLI | Persistent bidi stream? | Turn-by-turn resumption |
|---|---|---|
| `claude` | Yes — `-p --input-format stream-json --output-format stream-json` keeps one process alive indefinitely | |
| `cursor-agent` | No | `--resume [chatId]` — fresh process per turn |
| `agy` | No | `-c`/`--continue` / `--conversation <id>` — fresh process per turn |

```go
// HeadlessSession is what an orchestrator would drive. Two implementations, one contract.
type HeadlessSession interface {
    Send(ctx context.Context, turn glider.native.v1.Turn) (<-chan glider.native.v1.Turn, error)
    Close() error
}
```

Today's actual execution path (`internal/vendors.RunWithOptions`) is simpler than this — one-shot exec-and-capture per delegate call, not a held-open session — because the resume loop (`permission_relay_design.md`) only ever needs "run, then maybe run again with different args," not a live bidirectional stream. `HeadlessSession` stays here as the fuller interface a real persistent-front integration (e.g. holding Claude's bidi stream open across a whole delegated conversation) would need.

## 3. Delegation is a `Part`, not a new concept — but needs two independent triggers

### 3a. Interception-level delegation (front-agnostic) — **built**

Glider recognizes an explicit `/vendor-name <prompt>` flag in the user's own message, ahead of normal routing (`internal/mitm/delegate_handler.go`, `internal/vendors.ParseDelegateCommand`) — the same mechanical point `DecideLocal` already picks local-vs-cloud today. This is the load-bearing mechanism precisely because it never depends on the front's tool catalog: it works identically regardless of which CLI is in front, including `agy`, which has no subagent-tool concept of its own.

**Not built:** an automatic `DecideVendor(req) → same-as-front | claude | cursor | agy` classifier that picks a vendor without the user naming one — the natural extension of `DecideLocal`'s existing local/cloud decision into a third dimension. Inputs it would need: capability tags from vendor packs (which vendor's pack actually has a tool tagged for what the task needs — verified live-confirmed, not just wire-declared, since `agy`'s wire-declared catalog is confirmed broader than its live-exposed toolset), a liveness/auth probe per vendor, and ordinary routing concerns (cost, latency, explicit override).

### 3b. Model-initiated delegation via a native subagent tool — **not built**

For fronts whose model exposes a subagent-shaped tool, that tool could become a second, richer trigger for the same underlying delegation. Claude's real `Agent` tool is live-captured prior art for the shape this could take: `{description, prompt, subagent_type, run_in_background}` input, an addressable/resumable `agentId`, an async lifecycle (`task_started` → `task_progress` → `task_updated` → `task_notification`), correlation via `parent_tool_use_id`, and a result that carries budget/telemetry (`{totalTokens, totalDurationMs, toolStats:{...}}`) alongside the text. None of this is wired to cross-vendor delegation today — `agy`'s live toolset in particular doesn't appear to expose an equivalent primitive at all (it self-reports missing tools rather than reaching for wire-declared ones), so `agy` as a front would rely on 3a alone even if 3b existed.

### The shared `Delegate` shape (design, not yet the actual `internal/vendors` types)

```go
type Delegate struct {
    TargetVendor string // "claude" | "cursor" | "agy" | "" (same as front)
    Task         string
    ContextSlice []Part // never the full transcript by default
    Workspace    string // run-scoped path
    Budget       Budget // timeout, max turns, max $
    ReturnAs     string // "edit" | "read_result" | "text" | ...
    Background   bool
    TriggeredBy  string // "interception" (3a) | "model_tool" (3b)
}
```

The shipped code (`internal/vendors.RunOptions`/`RunResult`) is a narrower, already-working version of this idea, without the budget/background/triggered-by fields — those stay design until a concrete need (auto-routing, model-initiated delegation) requires them.

## 4. Rendering back: the front adapter is the only thing the user ever sees

The front CLI's adapter is meant to be the sole renderer — whatever vendor actually executed a delegated task, the front synthesizes an event in *its own* native shape from the returned NGL `Turn`, using `EditViews`' converter registry. Concretely: if `agy` executes an edit via its native `RangeReplace` shape but the user is sitting in `claude`, the front adapter asks the converter registry for `hunks` (fetching the pre-image from the workspace if needed) and emits a `claude`-shaped `Edit` diff — the user never sees `agy` or a range-replace, just a familiar diff.

Rules this implies:
- **`Turn.Raw` (the vendor's actual event) rides along for debugging but is never shown by default.**
- **Unconvertible views degrade to a visible, honest fallback** — if `hunks` genuinely can't be derived (no pre-image available), show the vendor-native form directly rather than fabricate a diff.
- **Large results go by reference, not inline** — a delegate's large output becomes a workspace-relative pointer, not an inlined blob, mirroring how `cursor-agent`'s own `webSearchToolCall` already offloads big pages to a file + pointer.

**Current reality:** `vendors.FormatEditSummary` does render a delegate's edit as a diff block appended to the reply (proven for cursor-agent and claude) — a simpler version of this idea, not yet routed through per-front-vendor native rendering (the reply always looks like a generic diff block, not literally like the front CLI's own edit-tool output).

## 5. Context across delegated sessions — open discussion, not a decision

`contextgraph` today is turn/session bookkeeping for *routing* decisions (`BindRequest`/`BindSession`, `ResolveCloudSticky`, `RecentTurns`, `Export`) — not a general knowledge base. A delegated sub-session needs roughly the same three things a sticky turn-family already needs: a way to know which parent turn a delegate call belongs to, a way to re-resolve that binding on the next turn without re-deciding from scratch, and an audit trail. Two ways this could go, neither decided:

1. **Generalize `contextgraph` in place** — new `EventKind` values for delegate lifecycle, a vendor-aware sticky resolver alongside `ResolveCloudSticky`. Cheapest, reuses working persistence/pruning/export, but bends `contextgraph`'s currently local/cloud-shaped schema toward also meaning "which external CLI."
2. **A separate, smaller delegation ledger** that only references a `contextgraph` turn id. Cleaner separation, but reimplements sticky-TTL logic that already exists.

Worth revisiting once there's enough real delegate traffic to look at, rather than deciding from the design doc alone.

## Known-solid findings worth keeping in mind for a 4th vendor

- Adding a vendor should be a new vendor pack + one `VendorAdapter`/wire-format-adapter implementation + one probe string — no core-code branching. Untested claim until it's actually done once (`adapter_boundary.md` walks through the file list this implies).
- A vendor's *wire-declared* tool catalog is not proof of its *live* toolset — `agy`'s Antigravity CLI self-reports missing tools it has wire-declared support for. Trust a `confirmed: true/false` per-tool flag set by an actual live trace, not the declared catalog.
- Across the three vendors tested, zero TLS certificate pinning was found — worth re-checking for any future vendor, not assuming it holds forever.
