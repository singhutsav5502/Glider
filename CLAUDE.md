# Glider

Claude Code does not read `AGENTS.md` on its own, so import it. Everything
shared — the delegation rules and the repository conventions — lives there.

@AGENTS.md

## Claude Code specifics

These override or add to the shared file when *I* am the front CLI:

- **My delegate targets are `/cursor-agent` and `/agy`.** Never `/claude` —
  that is me, and delegating to myself buys nothing while still paying the
  cold start.
- **Glider already has my conversation history.** Claude Code sends it on
  every request, so Glider passes real context to whatever I delegate to. I
  still restate the task in full, because the delegate gets a summary and not
  a transcript.
- **A leading `/word` never leaves this session.** Claude Code takes it as a
  local slash command and answers `Unknown command: /agy` itself. The flag
  goes at the very end of the message, always.
- **I have a subagent mechanism, so the parallel rule applies to me.** The
  Agent tool is how I fan independent delegations out: one subagent per
  task, each ending its own message with the delegate flag, all in flight at
  once. Dispatch them in a single message — separate messages serialise them
  and give back the cold start I was trying to avoid.
- **`/agy` opens a window and returns nothing.** For agy's answer in this
  session, write `/agy:headless` and accept its caveats: it auto-denies tools
  needing permission, and after the grant clears it often describes the
  workspace instead of doing the work. Prefer `/cursor-agent` when I need a
  headless answer I can act on.
