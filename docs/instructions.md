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
> context through a private per-run directory instead (see §4), so your own
> rules here are the only thing in it.

## 2. The template

Copy this in, then edit the rules — the specific routing is **your** call, not
something this doc can decide for you (see §3).

```markdown
## Delegating to other CLIs

Glider is running, so I can hand a task to another installed agent CLI by
ending my message with a trailing flag. The flag must be the LAST thing in
the message.

    <the full task description> /cursor-agent
    <the full task description> /agy
    <the full task description> /claude

Rules for when to do this automatically:

- **Second opinion on a hard bug** — when I've failed at the same problem
  twice, delegate a fresh look to `/cursor-agent` rather than trying a
  third variation myself. A different model family fails differently.
- **Long mechanical edits** — bulk renames or repetitive refactors across
  many files go to `/cursor-agent`.
- **Anything needing a human decision mid-task** — use `/agy`, which
  opens a real agy window the user drives directly, instead of guessing
  on their behalf. Nothing comes back into this session from it.

Never delegate automatically:

- Anything destructive (deleting files, `git push`, migrations, releases).
- Anything where I already have the answer — delegation costs ~20s of
  cold start minimum, so it must buy something.
- Tasks under a couple of minutes of my own work.

When I delegate, I must:
1. Put the ENTIRE task in the message — the delegate cannot see our
   conversation beyond the context Glider passes it, and cannot ask me
   follow-up questions.
2. Say out loud that I'm delegating and why, before I do it.
3. Review what comes back rather than pasting it through unchecked.
```

## 3. Writing your own rules

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

Anti-triggers worth stating explicitly in your rules, because a model won't
infer them:

- **Latency is real.** A delegated call pays a full cold start — process
  launch, auth, model listing, then inference. A trivial `cursor-agent` task
  takes ~20s measured with Glider entirely out of the path. Delegation must buy
  more than it costs.
- **The delegate can't ask questions.** It runs headless and returns once. An
  ambiguous task comes back with an assumption baked in, silently.
- **Destructive actions need you.** Whatever your rules say, keep deletions,
  pushes, and migrations off the automatic path.

## 4. What the delegate actually receives

Worth knowing, because it determines how much you need to spell out:

- **The task text** — everything before the flag.
- **A working directory** — the resolved workspace.
- **A context file** in a private directory created for that one run
  (`~/.glider/delegates/<id>/`), handed to the CLI through a flag it already
  supports — `--append-system-prompt-file` for claude, `--add-dir` for
  cursor-agent and agy. It holds the task restated, which CLI delegated it,
  the workspace, and up to 6 recent human instructions from your session.
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
It's plain markdown, bounded to the last 60 turns, and **safe to delete at any
time**. Two CLIs open in the same repo stay separate: a delegate only ever
sees the history of the session that dispatched it, never the other one's.

It does **not** receive your full conversation verbatim, your CLI's internal
reasoning, or anything from a previous delegate call. Each delegate is a cold
start with a briefing — so the task description still has to stand on its own.

## 5. Loops, and why they don't happen

If your delegate rules live in `AGENTS.md`, and a delegated CLI reads
`AGENTS.md`, it would read those same rules and could delegate onward — each
hop paying another cold start.

Glider prevents this: the context file it hands the delegate says explicitly
that it *is* the delegate, that it should ignore any auto-delegation rules it
finds in the project, and that it must complete the task itself. You don't
need to handle this in your
rules, but if you write your own briefing conventions, keep that line.

## 6. Verifying it works

1. Start Glider and confirm the dashboard is up (`http://127.0.0.1:8081`).
2. Give your CLI a task matching one of your triggers.
3. It should say it's delegating, and the reply comes back prefixed
   `Delegated to <vendor>:`.
4. The dashboard's Overview request log shows a row with action `delegate`.

If nothing delegates, check in this order:

| Symptom | Likely cause |
|---|---|
| CLI never emits the flag | Rules are in a file that CLI doesn't read (§1) |
| `Unknown command: /agy` | The flag landed at the *start* of the message. It must be trailing — Claude Code intercepts a leading `/word` locally and never sends it. |
| Flag sent, nothing happens | That vendor isn't registered or is disabled — check the dashboard's Vendors page |
| "I don't know which directory…" | No workspace set. Reply `. /workspace` once, or set a default on the Vendors page. |
