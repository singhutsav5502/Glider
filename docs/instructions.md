# Auto-delegation instructions

**What this is:** a template you copy into your project's agent context file so
your CLI delegates certain tasks to a different CLI *on its own*, without you
typing a flag every time.

**Why it works:** Glider's delegate flag is just trailing text in a message
(`<prompt> /vendor`). Nothing has to understand a new protocol — if your CLI's
own model appends that flag, Glider routes it. So "automatic delegation" needs
no new Glider feature, only instructions your CLI already reads at startup.

---

## 1. Where to put it

Each CLI reads a different file. Put the rules in the one your CLI actually
reads, at your repository root:

| Your CLI | File it reads |
|---|---|
| Claude Code | `CLAUDE.md` — it does **not** read `AGENTS.md` (it supports an explicit `@AGENTS.md` import) |
| cursor-agent | `AGENTS.md` |
| agy (Antigravity) | `AGENTS.md` and `GEMINI.md` (both, merged; `GEMINI.md` wins conflicts) |

If you want one file for everything, write `AGENTS.md` and add a single line to
`CLAUDE.md`:

```markdown
@AGENTS.md
```

> **Note:** Glider never writes into this file. When it delegates, it passes
> context through a private per-run directory instead (see §5), so your own
> rules here are the only thing in it.

## 2. The template

Copy this in, then edit the rules — the specific routing is **your** call, not
something this doc can decide for you (see §4).

```markdown
## Delegating to other CLIs

Glider is running, so I can hand a task to another installed agent CLI by
ending my message with a trailing flag. The flag must be the LAST thing in
the message: a *leading* `/claude` is intercepted by my own CLI as a slash
command and never reaches Glider at all.

    <the full task description> /cursor-agent

| Flag | Mode | Answer returns to me? |
|---|---|---|
| `/claude` | headless | yes |
| `/cursor-agent` | headless | yes |
| `/agy:headless` | headless | yes |
| `/claude:interactive` | interactive | no |
| `/cursor-agent:interactive` | interactive | no |
| `/agy` | interactive | **no** — this is agy's default |

I never delegate to the CLI I am running as. I never type `:resume`; Glider
drives those itself when the user approves a denied tool.

### Which mode

| If | Then |
|---|---|
| I need the answer to continue my own work | headless |
| A person should make a judgment call part-way through | interactive |
| The change is destructive or wide-reaching | interactive |
| The work is the user's to drive and mine only to set up | interactive |

An interactive delegate hands the task to the user in a separate window. I
never see what happens in it, so I must not open one and then wait for
findings — there is no channel for them to arrive on. I say I opened it, say
nothing returns here, and carry on with my own work.

### When to delegate, and what to do instead

| Delegate when | Because |
|---|---|
| I have failed twice at the same problem | A different model family fails differently; a third try of mine correlates with the first two |
| A bulk mechanical edit — a rename, a codemod, a repetitive signature change | It is long, not hard |
| A judgment call is needed mid-task | Open `/agy` and let the user drive |

| Never delegate | Do this instead |
|---|---|
| Anything destructive: deleting files, `git push`, migrations, releases, dependency upgrades | Do it myself, or ask the user first |
| Anything I already know how to do | Do it myself — delegation costs ~20s before any work starts |
| Anything under a couple of minutes of my own work | Do it myself |
| Anything I cannot state completely in one message | Narrow it until I can, or do it myself |

Worked example — a second opinion, with what I already tried:

    The session cookie is dropped on the OAuth redirect. I tried widening
    the cookie domain and switching SameSite to Lax; both still drop it,
    and the Set-Cookie header is present in the 302. Scope:
    internal/auth/. Find the cause. /cursor-agent

### Doing several at once

Glider runs delegates concurrently — each gets its own context, so nothing
queues. If I have a subagent mechanism I dispatch one per task and collect
the replies; if not, I delegate the highest-value task and do the rest
myself. I split only where the parts are genuinely independent, because one
delegate cannot see another's edits. Interactive delegates do not
parallelise: a person drives one window at a time.

### What the delegate already knows

Glider writes it a briefing: that it IS the delegate, which CLI sent the
task, the workspace, the task restated, and the relevant earlier turns of
this session including what any previous delegate changed.

It does NOT get this conversation verbatim, my reasoning, or my tool output.
So I never write "as discussed above" or "that file we were just looking
at" — it saw none of that. It also cannot ask me a question, and answers
once.

### Writing the message

1. The goal, in one sentence.
2. The constraint — what must not change, what must still pass.
3. The file or directory scope, as paths from the workspace root.
4. What I already tried and how it failed, for a second opinion.
5. What "done" looks like, so it stops in the right place.

I say I am delegating, and why, before I send it.


### If Glider asks which directory

Glider cannot read my working directory out of my own CLI, so the first
handoff in a session may come back asking for it instead of running. That
is not an error. I reply once with the project path and the trailing
`/workspace` flag:

    . /workspace
    C:/projects/myapp /workspace

Then I resend the task. Every later handoff in this session runs there.

Glider asks every session, even when a default is configured — it cannot see
which project a new CLI is in, so it will not assume. If a default is set,
the question arrives with that path already in it and the user only has to
send it back.

### When the reply comes back

A reply through transparent/MITM mode is prefixed `Delegated to <vendor>:`;
through the gateway route it is not, so I do not depend on the prefix to
recognise one. Before I use any of it:

| What came back | What I do |
|---|---|
| It meets the constraint I set | Use it, and say it came from a delegate |
| It missed the constraint, or answered a different question | Fix my message and send it ONCE more, or do the work myself |
| It failed, or returned nothing usable | Do NOT resend the same task to the same CLI — it fails the same way. Try a different CLI, or do it myself |
| It contradicts something I already know | Verify before accepting. It saw a briefing, not the conversation |

I never present a delegate's claim as my own verified result, and I never
report work as done on a delegate's word alone.

### If I am the delegate

Glider says so directly in the context it hands me. If that context tells me
I am already the delegate, I ignore every rule above, append no `/vendor`
flag, and complete the task myself.
```

### Adjusting it for the CLI you drive

The block above is written to be pasted as-is. These are the parts worth
changing depending on which CLI is the *front* — the one you type into:

| Front CLI | Put it in | Delegate targets | Worth adding |
|---|---|---|---|
| Claude Code | `CLAUDE.md` | `/cursor-agent`, `/agy` | *"Fan parallel delegations out through the Agent tool, dispatched in one message."* Claude Code also sends its full conversation history on every call, so Glider has real context to pass on (§5). |
| cursor-agent | `AGENTS.md` | `/claude`, `/agy` | *"Restate any detail from earlier in this conversation that the task depends on."* Glider can only read the first envelope of a cursor-agent request, so it falls back to its own record of the session (§5). |
| agy | `AGENTS.md` **and** `GEMINI.md` | `/claude`, `/cursor-agent` | *"Prefer `/claude` when the task needs a narrow, auditable set of tool permissions."* See the permission row in §4. |

**Name the subagent mechanism if your front CLI has one.** The template says
"if I have a subagent mechanism, I use it", which is deliberately
conditional — a model cannot reliably tell whether it has one, so the rule
degrades to serial rather than inventing a tool. Claude Code's is the Agent
tool, confirmed. Whether cursor-agent and agy expose an equivalent has not
been verified here, so their rows do not claim one; check your own CLI and
name it if it does.

Delegating to the CLI you are already in is pointless — it buys neither a
different model family nor a separate quota, and still pays the cold start.
Leave your own vendor out of the list you paste in.

## 3. Every template, and which ones you can name

A flag with no suffix runs that vendor's `default` template. `:<name>` runs a
named one. This is the complete set as shipped in
`configs/vendor_candidates.yaml` — each is editable per-installation from the
dashboard's Vendors page, so treat this as the starting point, not a contract.

| Flag you type | Vendor | Template | Mode | What happens |
|---|---|---|---|---|
| `/claude` | claude | `default` | headless | Runs `-p --output-format stream-json`, answer comes back into your session. |
| `/claude:interactive` | claude | `interactive` | interactive | Opens a new console window running a normal Claude Code session, primed with your task. Nothing returns here. |
| `/cursor-agent` | cursor-agent | `default` | headless | Runs `-p --output-format stream-json --trust`. Answer comes back here. |
| `/cursor-agent:interactive` | cursor-agent | `interactive` | interactive | New window, normal cursor-agent session, primed with your task. **No `--trust`** — you answer the workspace-trust prompt yourself. |
| `/agy` | agy | `default` | **interactive** | Opens a new window with agy's own permission UI, primed with your task. This is agy's default, unlike the others. |
| `/agy:interactive` | agy | `interactive` | interactive | Identical to `/agy`. Both exist so the name is available explicitly. |
| `/agy:headless` | agy | `headless` | headless | Text comes back here. Read the caveats in §4 before choosing this. |

**The `resume` templates are not yours to type.** Each vendor also ships a
`resume` template, but Glider drives them itself as the second half of the
permission relay — you approve a denied tool, Glider re-runs the task through
`resume`. Typing them by hand does not work as you would expect:

| Flag | What actually happens |
|---|---|
| `/claude:resume`, `/cursor-agent:resume` | **Errors out.** Both templates contain `{{session_id}}`, and `RunWithOptions` refuses a template that needs a session id when none was supplied. |
| `/agy:resume` | Runs, but its args are byte-identical to `/agy:headless`. The only real difference on a Glider-driven resume is the scoped allow-rule that `agyAdapter.GrantResumePermission` writes into agy's `settings.json` immediately before the call and removes immediately after. Typed by hand you get none of that — so use `/agy:headless` and mean it. |

### Choosing headless or interactive

This is the decision worth encoding in your rules, because the two modes
return fundamentally different things:

| | headless | interactive |
|---|---|---|
| Where the answer goes | back into your session as text | into a separate window, and **nowhere else** |
| Can your CLI act on the result | yes | no — it never sees it |
| Who answers permission prompts | Glider relays them to you | the vendor's own UI, directly |
| Good for | anything whose output you need to read, diff, or feed onward | anything where a person should be steering |

Encode it as one rule: **if the answer has to come back, it must be headless.**
An interactive delegate is a handoff to a human, not a subroutine — a model
that delegates a bug investigation to `/agy` and then waits for the findings
will wait forever, because there is no channel for them to arrive on.

That has a specific consequence worth spelling out in your own rules. `/agy` is
interactive *by default*. So a rule that says "send hard bugs to agy" silently
becomes "open a window and drop the thread" unless it says `/agy:headless`. If
you want agy's answer in-session, name the template.

## 4. Writing your own rules

Be concrete about **triggers**, not vibes. `"delegate refactors to cursor-agent"`
works; `"delegate when it seems better"` does not — the model has no way to
evaluate that.

On *which* CLI is better at what: that's genuinely your judgment, and it changes
as these tools ship. This doc deliberately won't assert "cursor-agent is better
at refactoring" as fact. What's worth routing on, with reasons that hold up:

| Basis | What it actually buys you |
|---|---|
| **Different model family** | A genuinely independent attempt. The strongest reason to delegate — two tries from the same model correlate; two from different vendors don't. |
| **Quota separation** | Delegated calls spend against that CLI's own subscription. Useful when you're near a limit on your main one. |
| **Permission granularity** | claude can be approved for one specific tool (`--allowedTools`). cursor-agent has no per-tool flag — its only lever grants everything, so Glider won't reach for it. Prefer claude when you want narrow, auditable approval. |
| **Human-in-the-loop** | `/agy` opens a real window the user drives. The right answer whenever the task needs a judgment call, rather than guessing. |

> **`/agy` is interactive by default**, unlike `/claude` and `/cursor-agent`,
> which run headless. This is deliberate: agy's headless mode auto-denies any
> tool needing permission (so even reading a file costs an extra approve/resume
> round trip), and after that grant clears its model often *describes* the
> workspace instead of doing the work. Its interactive mode has neither problem
> — it opens a window with agy's own permission UI, primed with your task. Use
> `/agy:headless` only if you specifically want the headless behavior and its
> caveats. Note a window is not a text reply: nothing comes back into your
> session from it.

The template pairs every "never delegate" with what to do instead, in a
table. That shape is deliberate and worth keeping if you rewrite it: a list
of warnings with no matching actions is a measured anti-pattern — the model
starts checking its work against every warning instead of acting on any of
them. These are the reasons behind the four rows:

- **Latency is real.** A delegated call pays a full cold start — process
  launch, auth, model listing, then inference. A trivial `cursor-agent` task
  takes ~20s measured with Glider entirely out of the path. Delegation must buy
  more than it costs.
- **The delegate can't ask questions.** It runs headless and returns once. An
  ambiguous task comes back with an assumption baked in, silently. This is why
  the template spends five numbered points on what the message must carry.
- **Destructive actions need you.** Whatever your rules say, keep deletions,
  pushes, and migrations off the automatic path.

## 5. What the delegate actually receives

Worth knowing, because it determines how much you need to spell out:

- **The task text** — everything before the flag.
- **A working directory** — the workspace of the session that dispatched it.
  Glider cannot read that out of your CLI: no wire protocol it intercepts
  carries a filesystem path. So it asks you once, and the first handoff of a
  session can come back with that question instead of a result. Answer with
  the path and a trailing `/workspace` flag — `. /workspace` from your
  project — and every later handoff in that session runs there. A default set
  on the dashboard's Vendors page stops it ever asking.
- **A context file** in a private directory created for that one run
  (`~/.glider/delegates/<id>/`), handed to the CLI through a flag it already
  supports — `--append-system-prompt-file` for claude, `--add-dir` for
  cursor-agent and agy. It holds the task restated, which CLI delegated it,
  the workspace, and the relevant recent history of your session — bounded
  by `context.background.token_budget`, 20k tokens by default.
  The directory is deleted when the run ends.

**Your own files are never touched.** An earlier design appended to your
project's `CLAUDE.md`/`AGENTS.md` and restored them afterward; a per-run
directory removes that risk entirely.

The same block is delivered whichever way you run Glider — transparent
interception or gateway mode.

**Where the history comes from.** Preferably the front CLI's own request:
Claude Code sends full conversation history on every call, so Glider reads it
straight out. cursor-agent's protocol can't supply it — Glider is only able to
read the first envelope of its request, and reading further would hang the
call. So Glider also keeps its own running record of the turns it observes,
per workspace, attributed per origin CLI and process. When the front can't
supply history, that record fills in — which means context quality doesn't
depend on which CLI you happen to be driving.

That record lives at `~/.glider/continuity/<workspace>.md`, outside your repo.
It's plain markdown, bounded to the last 60 entries, and **safe to delete at
any time**. Two CLIs open in the same repo stay separate: a delegate only ever
sees the history of the session that dispatched it, never the other one's. A
CLI that you restart does keep its own earlier history — a new PID is not a
new conversation.

**It also records what each delegate did**, not only what you typed. A
`{delegate}` entry names the CLI that ran and what came back, so a later
delegate does not redo or undo an earlier one's work.

**Which entries a delegate gets is ranked, not just recent.** Entries are
scored against that delegate's own task, so a session that moved between
topics hands over the ones about *this* task. The two newest entries are
always included whatever they score, and the whole block is bounded by a token
budget rather than a fixed count.

**A long record is compacted, not simply truncated.** Once it outgrows what a
delegate could be handed, the older half is replaced by one summary and a
verbatim tail is kept — the approach Claude Code, Codex CLI and OpenCode all
converged on. Who writes that summary is `context.summary.chain` in your
config, and it defaults to `origin` first: an installed agent CLI you already
pay for, run headlessly, rather than your own API credits. It never reuses
credentials Glider saw in intercepted traffic. With no summarizer available it
still compacts, deterministically, keeping the opening turns and every
delegate outcome. All of this happens in the background, so it never delays a
request.

It does **not** receive your full conversation verbatim, your CLI's internal
reasoning, or anything from a previous delegate call. Each delegate starts
cold with a briefing — so the task description still has to stand on its own.

## 6. Loops, and why they don't happen

If your delegate rules live in `AGENTS.md`, and a delegated CLI reads
`AGENTS.md`, it would read those same rules and could delegate onward — each
hop paying another cold start.

Glider prevents this: the context file it hands the delegate says explicitly
that it *is* the delegate, that it should ignore any auto-delegation rules it
finds in the project, and that it must complete the task itself. You don't
need to handle this in your
rules, but if you write your own briefing conventions, keep that line.

## 7. Verifying it works

1. Start Glider and confirm the dashboard is up (`http://127.0.0.1:8081`).
2. Give your CLI a task matching one of your triggers.
3. It should say it's delegating, and the reply comes back — prefixed
   `Delegated to <vendor>:` in transparent mode, unprefixed via the gateway.
4. The dashboard's Overview request log shows a row with action `delegate`.

If nothing delegates, check in this order:

| Symptom | Likely cause |
|---|---|
| CLI never emits the flag | Rules are in a file that CLI doesn't read (§1) |
| `Unknown command: /agy` | The flag landed at the *start* of the message. It must be trailing — Claude Code intercepts a leading `/word` locally and never sends it. |
| Flag sent, nothing happens | That vendor isn't registered or is disabled — check the dashboard's Vendors page |
| "I do not know which directory…" | Expected once per session. Reply `. /workspace` from your project, then resend. A default on the Vendors page pre-fills the answer; it does not remove the question, so a second CLI opened in a different project never silently inherits the first project's directory. |
