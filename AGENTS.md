# Glider — agent instructions

Read by **cursor-agent** and by **agy** (which merges `AGENTS.md` and
`GEMINI.md`). Claude Code imports this file from `CLAUDE.md`.

Glider is a proxy between an agent CLI and its backend. It routes some
requests to a local model, and hands tasks from one CLI to another.
`docs/site/` is the documentation; `planning/` holds the design rationale.

## Delegating to other CLIs

Glider is running on this machine, so I can hand a task to another installed
agent CLI by ending my message with a trailing flag. The flag must be the
LAST thing in the message: a *leading* `/claude` is intercepted by my own CLI
as a slash command and never reaches Glider at all.

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
| Anything touching `internal/mitm` interception or the WinDivert redirector while Glider is running | Do it myself — a wrong edit there takes the network down, not just the build |
| Anything I already know how to do | Do it myself — delegation costs ~20s before any work starts |
| Anything under a couple of minutes of my own work | Do it myself |
| Anything I cannot state completely in one message | Narrow it until I can, or do it myself |

Worked example — a second opinion, with what I already tried:

    The transparent redirector drops connections on port 443 when
    AllowProcessNames is set. I tried widening the pattern and disabling
    the process filter; the first changed nothing and the second made it
    work, so the filter is where it goes wrong. Scope:
    internal/mitm/redirector_windows.go. Find the cause. /cursor-agent

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

## Conventions this repo does not enforce for you

Only what a tool cannot check. `go build ./...` and `go test ./...` are not
listed here: they either pass or they do not, and that is not something a
document needs to say.

- **Which CLIs exist is data**, in `configs/vendor_candidates.yaml`. A fourth
  CLI is an entry there plus an adapter — never a branch on a vendor name in
  shared Go code.
- **Comments and `docs/site/` prose** are Simplified Technical English as far
  as technical accuracy allows: short sentences, active voice, present tense,
  no idioms. Technical names stay as they are.
- **The delegation template in `docs/instructions.md` is deliberately NOT
  STE** — a CLI reads it, not a person. It is generated into
  `docs/site/instructions.html`, so edit the markdown and never the HTML.
- **`docs/site/` has a second home I cannot see from here.** The public copy is
  at https://utsv.work/Glider/, served from a snapshot in the website repo
  `singhutsav5502/thoughts` (`public/Glider/`). This repo owns the
  pages; that repo publishes them. So an edit here does NOT reach the public
  site until somebody runs `npm run sync:glider` there and pushes. I edit the
  pages here, never the snapshot, and I say the publish step is still pending.
