# Cross-CLI permission relay — design (not yet implemented)

> **Status (2026-07-26):** Design only, written for explicit sign-off before any code — per standing practice on this project (see `native_glider_orchestration.md`'s own history of "vet all decisions through me"). Nothing in this doc is built yet.

## 0. Why this doc exists, and the scope correction that produced it

The original ask ("show a delegated CLI's permission pause interactively in whatever CLI the user is running") assumed a **live, blocking pause** exists in each vendor's headless CLI that Glider could intercept and answer. A same-day research spike (three parallel live investigations, one per vendor, run without the skip-permission flags that all prior research had used) found that assumption is **false for all three vendors**:

| Vendor | Headless (`-p`) permission behavior, confirmed live |
|---|---|
| **cursor-agent** | Never blocks. Resolves synchronously inline in the stream-json stdout — `"tool_call":{"shellToolCall":{"result":{"rejected":{"command":"...","reason":""}}}}` — and the model just continues. `interaction_query` (the web-search/fetch approval event found earlier this session) also self-resolves in the same process, not a real external channel. |
| **claude** (Claude Code) | Never blocks either. A denied tool gets an immediate `tool_result{is_error:true, tool_result_meta.non_execution_kind:"user-rejected"}`; the terminal `result` event carries a structured `permission_denials:[{tool_name, tool_use_id, tool_input}]` array. (One open confound: tested from inside a sandboxed Claude Code subagent, so this needs one real-world retest before being fully trusted — see §6.) |
| **agy** | Always fails closed, unconditionally — the CLI's own stderr says so: *"headless mode cannot prompt... re-run with --dangerously-skip-permissions to auto-approve all tools."* Not an inferred edge case; documented policy. |

So there is no stdin channel, no blocking state, nothing to "unblock" in any vendor's headless mode. **The feature this doc actually designs is two complementary mechanisms**, both landing in the same place (a permission question shown in the origin CLI, answered by the human, relayed back):

- **Path A — headless resume loop** (default, practical, works today with plain process exec + stdout capture): run headless, detect the denial, ask the human out-of-band, then *resume* the delegate's session with expanded permissions and the answer as the next input. Not live — the delegate run has already ended (or partially ended) by the time the question is asked — but can be chained fast enough by Glider to feel responsive.
- **Path B — real interactive mode, opt-in** (true live relay): launch the delegate in its actual interactive/TUI invocation (no `-p`), attached to a pty Glider owns, so a genuine mid-execution pause can be read off the pty and answered by writing back into it in real time. Heavier (a long-lived process + pty plumbing per delegated session) but is the only path that produces an actually-live pause.

A third requirement folded in during this same design pass: **none of the launch commands should be hardcoded.** The user wants to define, per vendor, in the dashboard: (1) the default headless launch command template, (2) additional named command-template variants triggered by a specific flag (e.g. a flag that requests Path B instead of Path A for that call), with the resurface-to-user mechanism working the same way regardless of which command template actually launched the process.

## 1. Configurable per-vendor launch commands

Today (`internal/vendors/vendors.go`), a `Vendor` carries one hardcoded shape: `PrintFlag string`, and `Run` always execs `v.Path, v.PrintFlag, prompt`. `configs/vendor_candidates.yaml` is discovery-only (probe args), not execution config.

**Proposed replacement:** each `Vendor` carries a set of named **command templates**, editable from the dashboard, not just a single print flag:

```go
type CommandTemplate struct {
    Name string   `json:"name"`  // "default", "interactive", or any user-defined label
    Args []string `json:"args"`  // e.g. ["-p", "--trust", "--output-format", "stream-json", "{{prompt}}"]
    Mode string   `json:"mode"`  // "headless" | "interactive" — selects Path A vs Path B handling
}

type Vendor struct {
    Name      string
    Binary    string
    Path      string
    Version   string
    Enabled   bool
    Templates []CommandTemplate // first one named "default" is used when no flag override applies
}
```

`{{prompt}}` (and later, `{{session_id}}` for resume) are substituted at exec time — this is template *data*, not new Go code, matching the existing "vendor packs, not switch statements" discipline.

**Flag convention, extending the existing `/vendor-name` scheme**: `vendors.ParseDelegateCommand` already finds `/agy `, `/cursor-agent `, etc. anywhere in the extracted user text. This gets one addition — an optional **second token** naming which command template to use, e.g. `/agy:interactive fix the auth bug` selects the `Vendor.Templates` entry named `"interactive"` instead of `"default"`. No flag suffix → `"default"` template, preserving all current behavior unchanged. This is additive parsing, not a redesign of the existing matcher.

**Dashboard**: the existing Vendors tab (`internal/dashboard/vendors_api.go`, `static/index.html`/`app.js`) gets a template editor per vendor — add/edit/delete named templates, each with its arg list and headless/interactive mode toggle. Discovery still auto-populates a sane `"default"` headless template per vendor (today's `-p`-based behavior) so nothing regresses for a user who never opens this editor; it just becomes overridable instead of fixed.

## 2. Path A — headless resume loop

### 2.1 Detecting a denial

Per-vendor detector, each operating on already-captured output (no new process-lifecycle changes needed for this path — `vendors.Run` already blocks until exit and captures stdout/stderr):

- **cursor-agent**: parse the `stream-json` stdout (already have `ParseCursorAgentTurn`/`ParseCursorToolCall` from this session's NGL work) for any `tool_call` event whose `result` field is `{"rejected":{"command":..., "reason":...}}` instead of `{"success":{...}}`. New: `CursorToolResult` (already built) needs a sibling `CursorToolRejection` case — currently `CursorToolResult` only distinguishes success-present vs. not-yet-present; add explicit rejection detection rather than treating it as "no result yet."
- **claude**: parse the terminal `result` stream-json line's `permission_denials` array (new field, not modeled in `adapter_claude.go` yet — needs a `ClaudeResultEvent` type, analogous to `CursorResultEvent` already built this session).
- **agy**: no structured event — parse stderr text for the documented denial message shape (`... a tool required the "..." permission that headless mode cannot prompt for, so it was auto-denied ...`) via a regex extracting the permission name. Fragile by nature (prose-parsing), flagged as such; agy simply doesn't offer anything better in headless mode.

### 2.2 Asking the human

No vendor's wire format has a native "permission request" content-block type (confirmed directly for Claude this research pass; no better option found for the other two either). So the question is surfaced the only way any of them actually support: **as plain assistant text**, injected into whatever response Glider is already sending back to the origin CLI for that delegated turn — reusing `DelegateHandler`'s existing `writeAnthropicJSON`/`writeAnthropicSSE` reply path, just with a formatted denial summary instead of (or prepended to) the delegate's own output, e.g.:

```
[agy] needs permission to run: rm old_auth.py
Reply with "/agy:allow" to approve and continue, or "/agy:deny" to skip this step.
```

### 2.3 Resuming

This is genuinely two separate, uncorrelated HTTP requests through `DelegateHandler.TryHandle` — the ask, and (later, a separate turn) the human's answer — so **some session state has to persist between them**. Proposed: a small in-memory (later maybe disk-backed) map keyed by a short-lived token embedded in the question text itself (e.g. `/agy:allow <token>` — the token round-trips through the human's next message the same way `/agy` itself does, no new transport needed), holding: `{vendor, delegateSessionID, deniedPermission, originalPrompt}`.

On the answer arriving:
- **claude**: re-invoke with `--resume <session-id> --allowedTools <the specific tool>` (or a scoped `--permission-mode` bump) and the continuation prompt.
- **cursor-agent**: `--resume [chatId]` confirmed (closed the earlier gap — see below), combinable with `-p --output-format stream-json --trust`.
- **agy**: **corrected 2026-07-26, superseding this doc's original proposal.** A `permissions.allow`/`config.json` write was the first idea but was rightly rejected: it's a persistent, global side effect on the user's agy install (affects every future `agy` invocation, not just this delegated session) and it's a special-cased mutation that no other vendor's resume needs — breaking the uniform "resume is just a different `CommandTemplate`" model the other two vendors fit cleanly. Corrected design: agy's `resume` template is simply `["-p", "--dangerously-skip-permissions", "--continue", "{{prompt}}"]` — scoped to that one resume invocation, triggered only by the human's explicit approval, expressed as ordinary template args like every other vendor. Coarser than Claude's `--allowedTools <specific-tool>` (bypasses all permission checks for that continuation, not just the one denied action — agy has no scoped equivalent, confirmed by the research pass), but zero persistent side effects and zero agy-specific code path.

**Gap closed (2026-07-26, same day):** cursor-agent's resume flag was the one open item above; a follow-up research pass confirmed `--resume [chatId]` (also a standalone `resume`/`ls` subcommand for interactive session picking, not needed here) — combinable with `-p --output-format stream-json`. All three vendors now have a confirmed resume mechanism expressible purely as `CommandTemplate` args.

## 3. Path B — real interactive mode (opt-in, live relay)

Only reachable via a `CommandTemplate` with `mode: "interactive"` (§1) — e.g. `/agy:interactive`. Instead of `vendors.Run`'s blocking exec-and-capture, this needs a **long-lived, pty-attached process**:

- Launch `v.Path` with the interactive template's args (no `-p`) attached to a pseudo-terminal Glider allocates (needed because — per this pass's research — interactivity is understood to live only behind a real TTY for these CLIs' TUI code paths, not a plain pipe; Windows equivalent is a ConPTY, not the Unix `script`/pty approach the research forks tried and found unavailable in Git Bash).
- A read goroutine watches the pty's output stream for the CLI's own interactive-prompt pattern (vendor-specific string/ANSI match — needs a fresh, dedicated capture per vendor of what its real TUI permission prompt actually looks like on the wire; **not captured yet by any research pass so far**, since all prior research deliberately avoided the interactive TUI).
- When a prompt is detected, the same "ask via plain assistant text in the origin CLI" mechanism from Path A fires; the human's answer, once it arrives, gets written directly into the pty (e.g. `y\n`) rather than triggering a resume/re-invoke.
- The pty-backed process stays alive across the whole delegated session (not one-shot per prompt like Path A), which changes `DelegateHandler`'s request/response model — a single incoming `/v1/messages` request can no longer map to one outbound exec-and-wait; it needs to either hold the HTTP response open (SSE, already partially supported by `writeAnthropicSSE`) across an indefinite pty session, or decouple entirely (return immediately, deliver the eventual answer/question via a side channel the origin CLI polls or receives on a later turn).

This path is materially larger than Path A and has a real unresearched core (what each vendor's actual interactive permission prompt looks like on a pty, and how Windows ConPTY integration works in this codebase, which currently has no pty/ConPTY code at all). Proposed: **Path A ships first** (fully spec'd above, all three vendors' detection mechanisms are close to buildable today), Path B gets its own follow-up research spike (one vendor's real TUI prompt captured via ConPTY) before design, not built blind.

## 4. What changes, concretely (once signed off)

- `internal/vendors/vendors.go`: `Vendor.Templates []CommandTemplate` replaces `PrintFlag`; `Run` takes a template name; `ParseDelegateCommand` gains the optional `:templatename` suffix parse.
- `internal/vendors/vendor_candidates.yaml` schema extended with a default `templates:` block per candidate (still data, not code).
- `internal/ngl/adapter_cursor.go`: add rejection-case handling alongside `CursorToolResult`.
- `internal/ngl/adapter_claude.go`: add `ClaudeResultEvent` (mirroring `CursorResultEvent`) with `PermissionDenials []ClaudeDenial`.
- `internal/mitm/delegate_handler.go`: denial detection dispatch per vendor, the ask/resume token map, resume re-invocation per vendor.
- `internal/dashboard/vendors_api.go` + `static/index.html`/`app.js`: template CRUD UI.
- Path B: net-new, deferred pending its own research spike (§3).

## 5. Explicitly open, needs your call

1. ~~Pilot vendor for Path A~~ — superseded: building all three in parallel per direct instruction (2026-07-26), not sequencing a single pilot.
2. ~~agy's `permissions.allow` config.json write~~ — rejected and replaced (2026-07-26): see §2.3's corrected agy resume design (a scoped `--dangerously-skip-permissions` resume template, no config mutation).
3. **Path B timing** — build now (needs its own research spike first) or defer until Path A is proven out for at least one vendor.
4. **Token/session-state persistence** — in-memory (simplest, lost on Glider restart mid-conversation) vs. disk-backed (survives restart, more moving parts) for the ask↔answer correlation in Path A. **Still open, and now the main blocker** — Path A's detection layer (all three vendors) and configurable command templates are implemented and tested; what's left before the loop is actually end-to-end is exactly this correlation/resume wiring in `DelegateHandler`.

## 6. Open research gaps carried over from the spike

- Claude's "never blocks" finding was tested from inside a sandboxed Claude Code subagent — needs one retest outside that environment before being fully trusted.
- ~~cursor-agent's resume/continue flag for Path A was not found/tested.~~ Closed 2026-07-26: `--resume [chatId]` confirmed.
- No vendor's real interactive-TUI permission-prompt wire shape has been captured yet (blocks Path B design specifics, not Path A).

## 7. Implementation status (2026-07-26)

**Path A is fully wired end-to-end for all three vendors.** Built and tested (`internal/ngl`, `internal/vendors`): `ngl.CursorToolRejection`/`CursorRejection`, `ngl.ClaudeResultEvent`/`ClaudeDenial`, `vendors.CommandTemplate`/`Vendor.ResolveTemplate`, `vendors.RunResult`/`RunOptions`/`RunWithOptions` (template-args substitution, `{{prompt}}`/`{{session_id}}`, with a guard against resuming with a missing session id), `vendors.DetectDenials`/`FormatDenialSummary` (all three vendors), `ParseDelegateCommand`'s `:template` suffix parsing (with a false-positive-prefix regression test), and `configs/vendor_candidates.yaml`'s real `default`/`resume` templates per vendor (cursor-agent's required `--trust`, claude's required `--verbose`, agy's corrected scoped-`--dangerously-skip-permissions` resume — see §2.3).

The ask↔answer↔resume loop (§5 item 4) is now implemented: `vendors.RegisterPendingResume`/`TakePendingResume` (in-memory, one-shot, TTL-bounded token store) plus `vendors.ResolveDelegate` — the single shared control-flow function both `DelegateHandler` and `api.Messages` now call, handling `/vendor:allow <token>` and `/vendor:deny <token>` as control-flow markers reusing the existing flag parser, alongside the normal run path. A denial gets surfaced with concrete reply instructions embedded (`FormatDenialSummary`); an "allow" answer re-invokes the vendor's `resume` template with the stored session id (or, for agy, none needed); a "deny" answer just drops the pending state. Proven end-to-end with a real subprocess (a shell script standing in for agy's confirmed denial behavior), not just unit-level mocks.

Not yet done: dashboard template-editor UI (§4 — the backend model is ready, no UI yet), Path B entirely (§3).

## 8. Live-testing results (2026-07-26)

Ran the actual code against real, installed CLIs (a temporary `cmd/livesmoke` harness, deleted after use), not just fixtures. Findings:

**Confirmed working, live:**
- Claude's `default` template (`--output-format stream-json --verbose`) — clean run, and the real terminal `result` event's field name is exactly `permission_denials` (`"permission_denials":[]` on a clean run), matching `ClaudeResultEvent` exactly with zero adjustment needed.
- agy's `default` template correctly triggers and detects a real denial — the exact confirmed stderr message, `DetectDenials`, `RegisterPendingResume`, and `FormatDenialSummary` all proven working end-to-end against the real `agy.exe`.
- The full ask→answer round trip (`ResolveDelegate` with `"allow"`/`"deny"` template names, token store) works correctly within one process — confirmed by design once the two-separate-process artifact in the first test attempt was corrected.
- cursor-agent discovery works fine despite being a `.ps1`/`.cmd` wrapper, not a bare `.exe` — an earlier concern about `exec.LookPath` not finding it was unfounded.

**Real bugs found and fixed by this testing, not by inspection:**
- **agy workspace binding (fixed).** `agy -p` without an explicit directory silently operates in a generic internal fallback (`~/.gemini/antigravity-cli/scratch`), completely ignoring the process's actual working directory — confirmed by watching it "resume" into an unrelated project and by a `--continue` call reporting a file "not found" in that fallback path instead of the real scratch dir. Fix: added a new `{{cwd}}` template placeholder (substituted via `os.Getwd()` in `RunWithOptions`) and `--add-dir={{cwd}}` to agy's `default` template — confirmed live afterward that the model correctly saw the real directory's real contents. One easy-to-miss detail: the flag must be `--add-dir=<path>` (equals-sign form); `--add-dir <path>` as two separate argv entries silently failed to parse as a flag at all in live testing.

**Real bug found, then fixed (2026-07-26, same day) — agy's `resume` mechanism.** `--continue`/`--conversation<id>` were proven unreliable (4+ reproductions: both resume an unrelated "most recent" conversation, not the denied one, even immediately after the denial). `--dangerously-skip-permissions` was proven unreliable too (4+ reproductions across different flag positions/prompt phrasings: the model responds by explaining the flag instead of performing the task). agy's own changelog resolved the question: 1.1.3 "the CLI now soft-denies such tools and prints a stderr notice naming the allow-rule needed," 1.1.6 "headless (`-p`) runs... now honor persisted `settings.json` policies, including permissions" — confirming `settings.json`'s `permissions.allow` is agy's **officially designed** headless-permission mechanism, not a workaround. This directly reopened the settings.json-write decision from §2.3 (originally rejected as "too invasive and agy-specific"); reinstated with explicit sign-off once the evidence was in, and required as its own follow-up: **all vendor-specific execution-time behavior — not just wire-format parsing — must go through a proper interface point, never a `vendor.Name ==` branch in shared code.** See §9.

Confirmed live afterward, end-to-end, through the actual product code path (not a manual test): the grant suppresses agy's hard "command" denial entirely (no error on the resumed call), and the revert correctly restores the real `settings.json` to its exact original byte content afterward (diffed to confirm). One residual, accepted behavior: the model sometimes still asks a clarifying question on the resumed call instead of acting immediately (observed non-deterministically, both with and without the grant) — this is ordinary agent caution on a one-shot destructive request, not a Glider bug, and it's already handled correctly: a non-denial, non-error response is just normal output text, surfaced to the human as-is, answerable with a further plain reply.

## 10. Workspace-directory scoping (2026-07-26)

The real end-to-end test (§8) found a second, more serious gap than agy's resume: **every delegate call ran in Glider's own server process directory**, not the origin CLI's project directory — confirmed live when a resume call read and listed real files from Glider's own repo instead of a test scratch directory. Root cause: no wire protocol Glider intercepts (Anthropic Messages API included) carries a filesystem path at all, so `{{cwd}}` had nothing correct to resolve to.

First fix attempt — resolving the origin process's own cwd automatically via its PEB (the standard technique Process Explorer uses: `NtQueryInformationProcess` → PEB address → `ReadProcessMemory` through `ProcessParameters.CurrentDirectory`) — was implemented (`internal/procinfo`) and then proven broken by a real test against a real subprocess with a known cwd: **Windows Defender's default real-time cross-process memory-read protection silently truncates the read** (`ERROR_PARTIAL_COPY`), confirmed via `Get-MpComputerStatus` showing real-time protection active — the default posture on essentially every real Windows install, not a fixable offset bug (the PID/parent-PID fields in the very same struct read back correctly, proving the struct layout was right).

**Final design** (explicit direction): PID *identification* (no memory read — just `GetExtendedTcpTable` + `QueryFullProcessImageNameW`, both confirmed reliable) is used as a stable *key* into a small directory registry (`internal/vendors/workspace.go`'s `WorkspaceStore`), not as a means to read the directory automatically. When a request's origin PID has no known directory (no per-PID entry, no configured default), Glider asks once via plain reply text (`"/workspace <path>"`, reusing the existing flag-parsing convention) rather than guessing — and remembers it for that PID's lifetime. A dashboard-configured default (`internal/dashboard`'s new "Default workspace" field, persisted in `Registry.DefaultWorkspace`, seeded into the live store at startup) covers the common single-project case without ever asking.

Confirmed live, full round trip through the real running product (`glider` binary + real `curl` + real `agy`): (1) an unrecognized origin PID gets asked rather than silently defaulting to Glider's own directory; (2) `/workspace <path>` from the *same* OS process (verified using one persistent `curl --next` invocation so the PID stays constant, as it would for a real long-running interactive CLI session) is remembered; (3) a follow-up delegate call from that same process correctly operates on the set directory — the model's own reply named the exact scratch directory. Also confirmed the negative case: a *different* process (a fresh `curl` invocation, different PID) correctly does NOT inherit another process's workspace — proving real per-process scoping, not a global leak.

As part of this fix, `internal/mitm/redirector_windows.go`'s own duplicate TCP-owner-PID/process-name lookup was retired in favor of `internal/procinfo`'s shared implementation — the exact "refactor whatever doesn't meet the interface-point standard" instruction that triggered §9 below applied here too, since the redirector had needed this same lookup for its process-based traffic filtering all along.

## 9. VendorAdapter refactor (2026-07-26)

While fixing agy's resume, a design review caught that `DetectDenials` and `extractSessionID` both branched on `vendorName` directly inside otherwise-shared code — the exact anti-pattern `internal/ngl`'s "vendor packs, not Go switch statements" principle (`agent_cli_interop.md` §1) already existed to prevent for wire-format parsing, just not yet extended to the execution layer. Refactored: `internal/vendors/adapter.go` defines `VendorAdapter` (`DetectDenials`, `ExtractSessionID`, `GrantResumePermission`), with `cursorAgentAdapter`/`claudeAdapter` (in `denial.go`) and `agyAdapter` (`denial.go` + `agy_grant.go`) as the three implementations, looked up via `adapterFor(vendorName)` — the one place that knows the full set of vendor names. `RunWithOptions`, `DetectDenials` (package func), and `extractSessionID` are now thin wrappers with zero vendor-name branching of their own. `GrantResumePermission` is a no-op for claude/cursor-agent (their resume `CommandTemplate` args alone suffice) and agy's real implementation (`agy_grant.go`) — a scoped `settings.json` `permissions.allow` read/modify/write with a byte-for-byte snapshot revert, tested in isolation (`agy_grant_test.go`, 6 tests, real-home-directory isolated via `USERPROFILE`/`HOME` env override) and confirmed against the real file live.
