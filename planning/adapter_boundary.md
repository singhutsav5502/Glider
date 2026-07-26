# The adapter boundary — what's core, what's per-CLI

> **Status (2026-07-26):** Documents the current, real state of the code (audited the same day this doc was written, not aspirational) plus one proposed fix awaiting sign-off (§4). Companion to [permission_relay_design.md](./permission_relay_design.md) (the feature this boundary was hardened while building) and [agent_cli_interop.md](./agent_cli_interop.md) (the original wire-format research this principle started from).

## 0. The rule

**Glider's core code must not know which CLI it's talking to.** Every fact that differs between claude, cursor-agent, and agy — a wire-format quirk, a denial message shape, a permission-granting mechanism, a launch flag — lives behind an interface or in data, never as a `switch vendor.Name` or `if vendor.Name == "agy"` inside shared control flow. Stated directly, twice, by direct instruction while fixing agy's resume mechanism (2026-07-26): *"all these things are per adapter... need to be exposed as interface points so the main core remains the same for all and blind to per cli specificity... refactor whatever doesn't meet this design standard"* and again just now: *"glider should not do anything SPECIFICALLY for any cli."*

This is the same principle `internal/ngl` already followed for wire-format parsing ("vendor packs, not Go switch statements," `agent_cli_interop.md` §1) — this doc is about extending it to the *execution* layer (`internal/vendors`), where it wasn't originally applied and had to be retrofitted.

## 1. The two adapter layers — they answer different questions

Glider has **two separate, unrelated "adapter" concepts**, both real, both necessary, easy to conflate:

| | **NGL adapters** (`internal/ngl/adapter_*.go`) | **VendorAdapter** (`internal/vendors/adapter.go`) |
|---|---|---|
| Question it answers | "What does this vendor's tool-call / diff look like on the wire, and how do I turn it into the canonical `Turn`/`Part`/`EditViews` shape?" | "How do I detect a denial, recover a session id, or grant a resume permission for this vendor, when running it headlessly?" |
| When it runs | Parsing bytes already captured (post-hoc, observational) | During/around an actual `exec.Command` invocation (execution-layer, has side effects) |
| Shape | One file per vendor, free functions + typed structs (`ParseCursorToolCall`, `CursorEditViews`, ...) | One `interface{...}` + one implementing type per vendor, registered in a `map[string]VendorAdapter` |
| Who calls it | Anything that wants to inspect wire data (not yet wired into live traffic — see `native_glider_orchestration.md`'s "still not built" section) | `internal/vendors`' own `RunWithOptions`/`ResolveDelegate` |

They're independent — the permission-relay feature (this doc) only needed `VendorAdapter`, and never touches the NGL adapters at all (except reusing `ngl.CursorToolRejection`/`ngl.ClaudeResultEvent` as building blocks *inside* `cursorAgentAdapter.DetectDenials`/`claudeAdapter.DetectDenials` — a real, intentional dependency: `VendorAdapter` is allowed to reuse NGL's wire-format types, since NGL's job is exactly "know the shape of vendor X's bytes").

## 2. `VendorAdapter` — the execution-layer interface point

```go
type VendorAdapter interface {
    DetectDenials(stdout, stderr []byte) []Denial
    ExtractSessionID(stdout []byte) string
    GrantResumePermission(v Vendor, denials []Denial) (revert func() error, err error)
}
```

Three implementations exist today, registered in one map (`adapter.go`'s `vendorAdapters`) — the **only** place in the whole codebase that lists all vendor names for execution purposes:

| | `cursorAgentAdapter` | `claudeAdapter` | `agyAdapter` |
|---|---|---|---|
| `DetectDenials` | Scans stream-json for a rejected `tool_call` (`ngl.CursorToolRejection`) | Scans stream-json for the terminal `result` event's `permission_denials[]` (`ngl.ClaudeResultEvent`) | Regexes agy's confirmed stderr message (no `--output-format` flag exists for this vendor at all — prose is the only signal) |
| `ExtractSessionID` | `sessionIDFromJSONLines` (shared helper — both vendors echo `session_id` on every stream-json line, a genuine cross-vendor commonality, not per-vendor logic) | same shared helper | Always `""` — no structured stdout to extract from |
| `GrantResumePermission` | No-op (`--resume [chatId]` alone is sufficient) | No-op (`--resume <id>` alone is sufficient) | Real side effect: read/modify/write `~/.gemini/antigravity-cli/settings.json`'s `permissions.allow`, return a byte-for-byte revert (`agy_grant.go`) |

`noopAdapter` is the fallback for any name not in the map — every method a safe no-op — so `adapterFor(name)` never returns nil and callers never branch on "do I have an adapter for this."

**What calls through this interface, and stays 100% vendor-blind as a result:**
- `RunWithOptions` (`vendors.go`) — calls `DetectDenials`/`ExtractSessionID` after every run. Zero vendor-name checks of its own.
- `ResolveDelegate`/`resolveAllow` (`resume.go`) — calls `GrantResumePermission` before a resume attempt, `defer`s the revert. Zero vendor-name checks.
- `DelegateHandler`/`api.Messages` (the two HTTP-facing callers) — never see a `VendorAdapter` at all; they just call `ResolveDelegate`.

## 3. What's *data*, not code — `CommandTemplate` / `vendor_candidates.yaml`

The actual launch flags each CLI needs (`--trust` for cursor-agent, `--verbose` for claude, `--add-dir={{cwd}}` for agy) are **not** in Go code anywhere — they're YAML (`configs/vendor_candidates.yaml`), loaded into each `Vendor.Templates []CommandTemplate`, editable live from the dashboard's Vendors page. `RunWithOptions` substitutes `{{prompt}}`/`{{session_id}}`/`{{cwd}}` into whatever args a template declares — it has no idea what `--add-dir` *means*, it just does string substitution. This is the same "vendor packs, not switch statements" move NGL made for tool catalogs, applied to launch commands.

**A CLI's real behavioral quirks belong in `VendorAdapter` (Go, because they require real *logic* — parsing, side effects); a CLI's launch-flag requirements belong in `CommandTemplate` (YAML, because they're just *data*).** Neither should leak into `internal/mitm` or `internal/api`.

## 4. Former exception, fixed 2026-07-26 (same day as §5's other fixes)

`internal/mitm/delegate_handler.go` and `internal/api/anthropic_messages.go` used to both call `ngl.LastUserInstruction("claude", req.Messages)` — a **hardcoded** vendor name. Fixed: both now call `vendors.ResolveOriginVendorName(r.RemoteAddr, reg)` (`internal/vendors/origin.go`) — resolves the real OS process on the other end of the connection (reusing `procinfo`, the same PID-resolution mechanism `WorkspaceStore` already needed), matches its executable name against the registry's known vendors, and passes whatever it finds (including `""` for an unidentified origin) straight through to `ngl.LastUserInstruction`.

**A real regression surfaced immediately** by this fix, caught the same day by `TestDelegateHandler_ScaffoldedFlagDoesNotTriggerDelegation` failing: `ngl.StripScaffold`'s old behavior for an unrecognized vendor was "no stripping at all," which is safe for a *named*-but-unregistered vendor (nothing to strip, nothing was ever stripped for it) but **unsafe** for `""` specifically — "unresolvable origin" is exactly the condition that caused the original live scaffold-leak bug this whole mechanism exists to prevent. Fixed in `ngl.StripScaffold`: an empty vendor name now strips *every* known pattern defensively (safe, since stripping only ever removes text matching one narrow auto-injected wrapper convention, never something a human would plausibly type). Confirmed live afterward against the real running gateway: a `/agy` flag hidden inside `<system-reminder>` scaffolding, sent via plain `curl` (an origin process matching no registered vendor, so `ResolveOriginVendorName` correctly returns `""`), is still correctly stripped and does not trigger delegation.

Testing cursor-agent/agy as genuine origin fronts (not just delegate targets) — i.e., confirming `ResolveOriginVendorName` actually resolves to `"cursor-agent"`/`"agy"` for a request really sent by those CLIs, and discovering whether either has its own scaffolding convention worth adding to `scaffoldStrippers` — needs one of those CLIs' own traffic to actually reach Glider. Neither is confirmed to support a safe, simple redirect the way claude's `ANTHROPIC_BASE_URL` does: cursor-agent's completion plane was already confirmed (`agent_cli_interop.md`) to ignore `CURSOR_API_ENDPOINT` entirely, and no equivalent override is confirmed for agy. The only confirmed path for either is OS-level transparent interception (WinDivert) — a materially higher-risk mechanism than anything else in this doc (this project's own history includes an orphaned-driver incident that blackholed all HTTPS on the test machine until manual recovery). Not attempted without a separate, explicit go-ahead given that risk profile.

## 5. Fixing the agy "grants permission but doesn't act" gap — proposed, not yet implemented

Per §2 in the prior chat turn: agy's headless resume reliably clears the permission gate (confirmed live, repeatedly) but the *model* often responds by describing the directory instead of performing the edit — a real, reproducible pattern (6 consecutive attempts, this session), distinct from and downstream of the permission mechanism itself, which works. Interactive mode (confirmed live, same session) has no such problem — it has a whole permission-review UI (diff previews, "Always Approve," a dedicated confirmation screen) the model appears tuned around; headless mode has none of that scaffolding to lean on.

**Proposed fix, matching this doc's own rule:** a fourth `VendorAdapter` method:

```go
// WrapResumePrompt lets a vendor's adapter reframe the resume prompt for
// its own model's known behavior on a resumed call — a no-op for vendors
// whose resume already reliably completes the original request
// (claude, cursor-agent). Called once, by ResolveDelegate's resolveAllow,
// immediately before the resume RunWithOptions call.
WrapResumePrompt(prompt string) string
```

`agyAdapter.WrapResumePrompt` would prepend something like: *"Permission for this action has already been granted — do not describe the directory or ask a follow-up question, perform the action directly: "* + the original prompt. `cursorAgentAdapter`/`claudeAdapter` return the prompt unchanged (their resume already works via a flag alone, confirmed live). Core code (`resolveAllow`) calls `adapterFor(pr.Vendor.Name).WrapResumePrompt(pr.Prompt)` — one more call through the same interface, zero new vendor-name branching in shared code, so it holds to §0 exactly.

**Honest limits, stated up front rather than oversold:** this is a prompt-engineering nudge, not a guarantee — the model could still choose to hedge even with explicit "don't ask" framing (this session's headless attempts included prompts with similar framing that still didn't work every time, though never with this *specific* "permission already granted" phrasing tied to the resume moment specifically — untested as of this writing). If it doesn't reliably close the gap, the honest fallback stays what `ResolveDelegate` already does correctly: surface whatever the model actually said as plain output text, not silently claim success. The *real*, complete fix for guaranteed single-shot reliability is Path B (interactive mode via ConPTY) — deferred, needs its own research spike, unrelated to this smaller mitigation.

Also flagged, smaller and separate: `agyAdapter.GrantResumePermission` only writes the global `~/.gemini/antigravity-cli/settings.json` — a workspace with its own project-specific config under `~/.gemini/config/projects/<id>/` (confirmed via agy's changelog to take precedence over the global file) would silently ignore the grant. Didn't affect any test this session (no scratch directory acquired a project-specific config during testing — checked directly), but is a real latent gap worth closing before relying on this broadly. Not proposed as part of the `WrapResumePrompt` fix above; a separate, smaller follow-up.

## 6. Adding a fourth vendor — what changes, what doesn't

To make this doc concretely useful rather than just descriptive: adding a new CLI (call it `foo`) touches exactly:
1. `configs/vendor_candidates.yaml` — a new candidate entry + `default`/`resume` `CommandTemplate`s (data).
2. `internal/vendors/adapter.go` — one new `fooAdapter` type implementing `VendorAdapter`, one new map entry.
3. Optionally, `internal/ngl/adapter_foo.go` — only if/when foo's wire format needs parsing for something beyond execution (not required for Path A permission-relay to work at all — `agyAdapter.DetectDenials` proves a `VendorAdapter` can be entirely prose-based, no NGL dependency).

**Every other file — `RunWithOptions`, `ResolveDelegate`, `DelegateHandler`, `api.Messages`, `WorkspaceStore`, the resume token store, the dashboard's template editor, `ParseDelegateCommand`'s flag syntax — needs zero changes.** That's the concrete, checkable meaning of "the core is blind to per-CLI specificity."
