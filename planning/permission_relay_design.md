# Cross-CLI permission relay

How Glider lets you delegate a task from your current CLI to another installed CLI, and get that CLI's permission prompts relayed back to you instead of the delegated run silently failing or auto-denying. Code: `internal/vendors/`, `internal/ngl/`, `internal/mitm/delegate_handler.go`.

## Why this isn't just "pipe stdin/stdout through"

None of the three supported vendors (`claude`, `cursor-agent`, `agy`) actually **block** for a permission decision in headless (`-p`) mode — there's no live pause to intercept:

| Vendor | Headless permission behavior |
|---|---|
| **cursor-agent** | Never blocks. A denied tool resolves synchronously inline in the stream-json output (`"tool_call":{"shellToolCall":{"result":{"rejected":{...}}}}`) and the model just continues. |
| **claude** (Claude Code) | Never blocks. A denied tool gets an immediate `tool_result{is_error:true}`; the terminal `result` event carries a structured `permission_denials:[{tool_name, tool_use_id, tool_input}]` array. |
| **agy** (Antigravity) | Fails closed, unconditionally. Its own stderr says so: *"headless mode cannot prompt... re-run with --dangerously-skip-permissions to auto-approve all tools."* |

So the actual feature is a **resume loop**, not a live pty relay: run headless, detect the denial from already-captured output, ask the human out-of-band (as a normal reply in whatever CLI they're sitting in), then resume the delegate's session with expanded permissions once they answer. Not truly live — the delegated run has already ended by the time the question is asked — but fast enough to feel responsive.

A real interactive/pty-attached mode (**Path B**) that could catch a genuine mid-execution pause was scoped but never built — it needs Windows ConPTY plumbing this codebase doesn't have yet, plus a capture of what each vendor's real TUI prompt looks like on the wire. What's below is all Path A.

**A separate, much simpler mechanism — interactive hand-off — shipped 2026-07-28 and should not be confused with Path B.** A `:interactive` template (`mode: interactive`) hands the whole task to the vendor's own native interactive session in a brand-new, visible OS console window (`vendors.LaunchInteractive`, `internal/vendors/resume.go`'s `resolveInteractive`) instead of running headless — no pty plumbing, no output capture, no resume loop, no correlation back into the chat that requested it. Glider's only role is opening the window already primed with the task (each vendor CLI's own bare positional `[prompt]` argument for claude/cursor-agent, `--prompt-interactive` for agy — confirmed live via each CLI's own `--help`) and confirming it opened; the human takes it from there entirely on their own. Reached the same way this doc anticipated below (`fix the auth bug /agy:interactive`) — that example predates the implementation by roughly two days and turned out to name the feature correctly on the first guess.

## 1. Configurable per-vendor launch commands

A `Vendor` carries named **command templates** rather than one hardcoded print flag — editable from the dashboard's **Vendors** tab, discovery seeds a sane `default` per vendor from `configs/vendor_candidates.yaml`:

```go
type CommandTemplate struct {
    Name string   // "default", "resume", or any user-defined label
    Args []string // e.g. ["-p", "--trust", "--output-format", "stream-json", "{{prompt}}"]
    Mode string   // "headless" | "interactive" — interactive is unimplemented (Path B)
}
```

`{{prompt}}`, `{{session_id}}`, and `{{cwd}}` are substituted at exec time — template *data*, not vendor-specific Go code. A message can select a non-default template with a `:name` suffix on the delegate flag (`fix the auth bug /agy:interactive`), parsed by `vendors.ParseDelegateCommand`.

**The flag goes at the end of the message, not the start.** Confirmed live: Claude Code's own client reads an unrecognized leading `/word` as a local slash command and never sends the message over the network at all ("Unknown command: /agy") — so a flag typed as the human's first characters never reaches Glider from that front, full stop, no matter how the parser searches. Requiring the flag last, not first, sidesteps that failure mode for every front rather than special-casing Claude Code. Everything before the trailing flag, trimmed, is the prompt — kept intact rather than discarded, which an earlier "search anywhere" version of the parser used to do to whatever came before the first match.

## 2. Detecting a denial

Per-vendor, operating on already-captured stdout/stderr (`internal/vendors/denial.go`):

- **cursor-agent**: parse `stream-json` for a `tool_call` event whose result is `{"rejected":{...}}` instead of `{"success":{...}}` (`ngl.CursorToolRejection`).
- **claude**: parse the terminal `result` event's `permission_denials` array (`ngl.ClaudeResultEvent`/`ClaudeDenial`).
- **agy**: no structured event — regex the documented stderr denial message. Fragile by nature (prose-parsing); agy doesn't offer anything better in headless mode.

## 3. Asking the human, and resuming

No vendor's wire format has a native "permission request" content block, so the question is surfaced as plain assistant text on the same reply path `DelegateHandler` already uses:

```
[agy] needs permission to run: rm old_auth.py
Reply with "<token> /agy:allow" to approve and continue, or "<token> /agy:deny" to skip.
```

The token comes before the flag, same trailing-flag rule as §1 — the reply is itself a message a human might type into any front, so it can't start with `/` either.

The ask and the answer are two separate, uncorrelated HTTP requests, so state persists between them in an in-memory, one-shot, TTL-bounded token store (`vendors.RegisterPendingResume`/`TakePendingResume`) keyed by a token embedded in the question text. `vendors.ResolveDelegate` is the single function both `DelegateHandler` and the gateway's `api.Messages` route call to handle the normal run path plus `<token> /vendor:allow` / `<token> /vendor:deny`.

On allow, each vendor grants permission its own way, entirely behind `VendorAdapter.GrantResumePermission` (§5) — the caller doesn't know or care which mechanism a given vendor uses:

- **claude**: resume template re-invokes with `--resume <session-id>` and the continuation prompt (its own `--allowedTools` scoping is available but not currently used per-denial).
- **cursor-agent**: resume template uses `--resume [chatId] -p --output-format stream-json --trust`.
- **agy**: **the interesting case.** agy's `settings.json` (`permissions.allow`) and per-project `~/.gemini/config/projects/<id>.json` (`permissionGrants.permissionGrants.allow`, doubly-nested, takes precedence over global) are agy's own officially-designed headless-permission mechanism — confirmed via its changelog (1.1.6: "headless (`-p`) runs now honor persisted `settings.json` policies, including permissions"). Two alternatives were tried and rejected first: `--dangerously-skip-permissions --continue` as a plain resume-template flag looked cleaner (uniform with the other two vendors) but proved unreliable live — the model responds by *explaining* the flag instead of acting on it, repeatably. `--continue`/`--conversation <id>` alone also proved unreliable — both resume an unrelated "most recent" conversation, not the denied one. The shipped fix (`agy_grant.go`) writes a scoped rule into both the global and (if the delegate's workspace directory matches a known project) per-project file, resumes, then reverts both files to their exact original byte content afterward — tested with a byte-diff, not just "did the call succeed."

## 4. Workspace-directory scoping

Every delegate call needs to run in the *origin CLI's* project directory, not Glider's own server directory — no wire protocol Glider intercepts (Anthropic Messages API included) carries a filesystem path, so this can't be read off the request.

The first approach tried was reading the origin process's cwd directly via its PEB (`NtQueryInformationProcess` → PEB → `ReadProcessMemory` into `ProcessParameters.CurrentDirectory` — the technique Process Explorer uses). It failed against a real test: Windows Defender's default real-time cross-process memory-read protection silently truncates the read (`ERROR_PARTIAL_COPY`) — not a fixable offset bug, since the PID/parent-PID fields in the same struct read back fine.

**Shipped design instead:** PID *identification* only (`internal/procinfo`: `GetExtendedTcpTable` + `QueryFullProcessImageNameW`, both reliable) is used as a stable key into a small directory registry (`vendors.WorkspaceStore`), not as a way to read the directory automatically. An origin PID with no known directory gets asked once (`<path> /workspace`, same trailing-flag convention as §1/§3) and remembered for that PID's lifetime; a dashboard-configured default workspace (`Registry.DefaultWorkspace`) covers the common single-project case without ever asking.

## 5. VendorAdapter — the execution-layer boundary

`DetectDenials`, `ExtractSessionID`, and `GrantResumePermission` used to branch on `vendorName` directly inside shared code — the same anti-pattern NGL's wire-format adapters exist to prevent, just not yet extended to execution. Fixed by `internal/vendors/adapter.go`'s `VendorAdapter` interface:

```go
type VendorAdapter interface {
    DetectDenials(stdout []byte) []Denial
    ExtractSessionID(stdout []byte) string
    GrantResumePermission(v Vendor, cwd string, denials []Denial) (revert func() error, err error)
    WrapResumePrompt(prompt string) string
    ExtractEditViews(stdout []byte) (ngl.EditViews, bool)
}
```

`cursorAgentAdapter`/`claudeAdapter`/`agyAdapter` (`denial.go`, `agy_grant.go`) are the three implementations, looked up once via `adapterFor(vendorName)` — the only place in the package that knows the full vendor list. `GrantResumePermission` is a no-op for claude/cursor-agent (their resume template args alone suffice); agy's is the real settings-file grant from §3. `ExtractEditViews` feeds NGL's diff-rendering (see `adapter_boundary.md`) so a delegate's edit shows up as a proper diff in the reply, regardless of which vendor produced it.

See `adapter_boundary.md` for the full two-layer adapter picture (this interface vs. NGL's wire-format adapters) and what adding a 4th vendor actually touches.

## Known gaps

- **`WrapResumePrompt` doesn't reliably fix agy's "describes instead of acts" behavior.** Even after a successful grant, agy's model sometimes responds to a resumed call with a clarifying question or a description of what it would do, instead of doing it — observed non-deterministically, with and without the grant. Treated as ordinary agent caution on a one-shot destructive request rather than a bug to suppress, but it means the resume loop isn't always one round trip.
- **Path B (real interactive/pty relay) is unbuilt.** Everything above is the resume-loop design; a genuinely live mid-execution pause would need Windows ConPTY plumbing and a captured TUI-prompt wire format per vendor, neither of which exist in this codebase yet.
- **Concurrent workspace writes** between a delegate and the front session against the same directory have no locking — not yet a problem in practice, but unaddressed.
- **Cost/billing**: one delegated call can spend against a different subscription/API key than the one the user is directly paying attention to. No consent gate exists yet beyond the explicit `/vendor` flag itself.
