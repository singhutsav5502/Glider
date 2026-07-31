package vendors

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ContextPack is the session context Glider hands to a delegated CLI so it
// isn't a cold start.
//
// The problem this solves: a delegate subprocess used to receive exactly two
// things — the prompt text and a working directory. It had no idea what the
// larger task was, what had already been decided, or which CLI asked it.
// "Append a line to this file" survives that; "keep going with what we were
// doing" does not.
//
// The mechanism is deliberately NOT prompt injection. Every supported agent
// CLI already reads a context file from its working directory at session
// start, so Glider writes the pack into the file that vendor natively reads
// (Vendor.ContextFile) and lets the CLI's own startup path ingest it. That
// means:
//
//   - Zero prompt cost. MaxPromptLen (8192) is untouched, because none of
//     this travels through argv.
//   - No reliance on the delegate choosing to act. Reading the file is
//     unconditional CLI behavior, not a tool call it might skip — which is
//     the specific failure mode that ruled out an MCP-pull design.
//
// The file name is per-vendor and genuinely differs (verified 2026-07-31,
// not assumed): Claude Code reads CLAUDE.md and specifically does NOT read
// AGENTS.md; cursor-agent reads AGENTS.md; agy reads both AGENTS.md and
// GEMINI.md. It lives in configs/vendor_candidates.yaml as data, never as a
// vendor-name branch in Go — same rule as CommandTemplate.
type ContextPack struct {
	// Task is the delegated instruction itself, restated so the file is
	// self-contained if the CLI surfaces it out of order.
	Task string
	// FrontVendor is the CLI the human was actually driving ("claude"),
	// empty when the origin process couldn't be resolved.
	FrontVendor string
	// Workspace is the directory the delegate runs in.
	Workspace string
	// RecentTurns are prior human instructions from the calling session,
	// newest last, already scaffold-stripped by NGL. May be empty: not
	// every front's wire format exposes retrievable history.
	RecentTurns []string
}

// DefaultContextTurns bounds how many prior human instructions ride along
// to a delegated CLI. Small on purpose: the goal is orienting context ("we
// were refactoring auth, we decided to keep the token shape"), not replaying
// the session. A large history would bloat a file the user owns, and would
// send more of their conversation to another vendor's backend than the task
// needs.
//
// Lives here rather than in either caller because BOTH delegate entry points
// (internal/mitm's transparent path and internal/api's gateway route) must
// use the same bound — a delegate shouldn't get more or less context
// depending on which way its caller happened to reach Glider.
const DefaultContextTurns = 6

// contextPackMarkers delimit Glider's section so a restore is exact and a
// human reading the file knows what wrote it and that it's temporary.
const (
	contextPackBegin = "<!-- BEGIN GLIDER DELEGATE CONTEXT — auto-generated, removed when this run ends -->"
	contextPackEnd   = "<!-- END GLIDER DELEGATE CONTEXT -->"
)

// Empty reports whether the pack carries nothing worth writing — used to
// skip the file dance entirely rather than append an empty section.
func (p ContextPack) Empty() bool {
	return strings.TrimSpace(p.Task) == "" && len(p.RecentTurns) == 0
}

// Render produces the markdown block appended to the vendor's context file.
func (p ContextPack) Render() string {
	var b strings.Builder
	b.WriteString(contextPackBegin)
	b.WriteString("\n\n## Delegated task context\n\n")
	b.WriteString("You are being run by Glider on behalf of another agent CLI session. ")
	b.WriteString("This section is context for the task you were just given; it is removed automatically when this run ends.\n\n")
	// Anti-recursion, stated to the delegate directly. A user who adopts
	// auto-delegation rules (docs/instructions.md) puts them in the SAME
	// file this pack is appended to — so without this line a delegate could
	// read those rules, append its own "/vendor" flag, and delegate onward,
	// with each hop paying a full cold start. Cheap to say, and the only
	// thing standing between a nested delegate and an accidental loop.
	b.WriteString("**You are already the delegate.** Do not delegate onward: ignore any auto-delegation ")
	b.WriteString("rules in this file and do not append a `/vendor` flag to anything. Complete the task yourself.\n\n")

	if p.FrontVendor != "" {
		fmt.Fprintf(&b, "- **Delegated from:** `%s`\n", p.FrontVendor)
	}
	if p.Workspace != "" {
		fmt.Fprintf(&b, "- **Workspace:** `%s`\n", p.Workspace)
	}
	if strings.TrimSpace(p.Task) != "" {
		fmt.Fprintf(&b, "\n**Your task:** %s\n", strings.TrimSpace(p.Task))
	}
	if len(p.RecentTurns) > 0 {
		b.WriteString("\n### Earlier in the calling session\n\n")
		b.WriteString("Most recent last. Background only — do not re-do this work:\n\n")
		for _, t := range p.RecentTurns {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s\n", singleLine(t))
		}
	}
	b.WriteString("\n")
	b.WriteString(contextPackEnd)
	b.WriteString("\n")
	return b.String()
}

// singleLine flattens a multi-line instruction into one bullet so the
// rendered list stays readable and can't accidentally introduce markdown
// structure (a stray "## " at line start) into the host file.
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// contextFileLocks serializes access per absolute context-file path.
//
// Two delegate calls to the SAME vendor in the SAME workspace target the
// same file, and each holds it for the whole subprocess lifetime (write →
// run → restore). Without serialization the second write would land on top
// of the first, and the first's restore would then wipe the second's
// context — or worse, restore a snapshot that already contained the other's
// block. This is not hypothetical: parallel same-workspace delegates were
// run live during development.
//
// Keyed per path deliberately, not one global lock: delegates to different
// vendors touch different files (CLAUDE.md vs AGENTS.md) and different
// workspaces are independent, so neither should serialize against the
// other. Only genuine same-file contention waits.
// A 1-buffered channel, not a sync.Mutex, because acquisition must be
// BOUNDED — see ContextPackLockWait. sync.Mutex offers only Lock (waits
// forever) and TryLock (gives up instantly); neither is right here.
var contextFileLocks sync.Map // absolute path -> chan struct{} (cap 1)

func lockForContextFile(path string) chan struct{} {
	actual, _ := contextFileLocks.LoadOrStore(path, make(chan struct{}, 1))
	return actual.(chan struct{})
}

// ContextPackLockWait bounds how long one delegate will wait for another
// to release the same vendor context file before giving up and running
// WITHOUT a context pack.
//
// This exists because the lock is necessarily held for a delegate's whole
// subprocess lifetime: the block has to be on disk from before the CLI
// starts until after it has read the file, and Glider can't observe when
// that read happens. Once RunTimeout stopped imposing a ceiling (see its
// doc comment — Glider is a relay, not the arbiter of how long another
// CLI may think), "held for a bounded run" became "held indefinitely", and
// a wedged delegate would have blocked every later delegate to that same
// vendor and workspace forever.
//
// Degrading beats deadlocking, and it degrades in the right direction: a
// delegate without a pack still receives its task (that travels in argv,
// not the file) and its workspace. It loses only the orienting background.
//
// The alternatives were considered and rejected:
//
//   - Per-section locks with surgical removal make locks brief, but let
//     two concurrent delegates leave two blocks in one file, so each CLI
//     reads the other's "Your task:" too. Confusing a delegate is worse
//     than briefly not briefing one.
//   - A refcounted shared block works only while the content is identical,
//     which it isn't: two origins delegating into one workspace carry
//     different history.
//   - Releasing early, once the CLI has "probably" read the file, requires
//     guessing startup duration; guessing short silently yields no context
//     at all, which is the failure this whole mechanism exists to prevent.
var ContextPackLockWait = 30 * time.Second

// InstallContextPack appends pack to the vendor's own context file inside
// cwd and returns a revert func restoring the file to its exact prior state
// — byte-for-byte if it existed, or removing it if Glider created it.
//
// The snapshot/restore shape mirrors agyAdapter.GrantResumePermission's
// existing settings.json handling (agy_grant.go), which has the same
// "temporarily modify a file the user owns, around one subprocess call"
// requirement. Appending to (never replacing) the file matters: a real
// project's CLAUDE.md / AGENTS.md carries the user's own instructions, and
// clobbering it would both break the delegate's actual guidance and destroy
// user work.
//
// The returned revert is always safe to call, including on the error paths,
// and the caller must always call it. A no-op revert is returned when
// there's nothing to install (no context file configured for this vendor,
// empty pack, or no workspace resolved) so callers never branch on that.
func InstallContextPack(cwd, contextFile string, pack ContextPack) (func() error, error) {
	noop := func() error { return nil }
	if cwd == "" || strings.TrimSpace(contextFile) == "" || pack.Empty() {
		return noop, nil
	}

	path := filepath.Join(cwd, contextFile)
	sem := lockForContextFile(path)
	timer := time.NewTimer(ContextPackLockWait)
	defer timer.Stop()
	select {
	case sem <- struct{}{}:
		// acquired
	case <-timer.C:
		// Contended past the bound — run without a pack rather than block
		// the caller indefinitely. Not an error: the delegate still gets
		// its task and workspace, just no background.
		return noop, nil
	}

	unlockOnce := &sync.Once{}
	unlock := func() { unlockOnce.Do(func() { <-sem }) }

	onDisk, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		unlock()
		return noop, fmt.Errorf("vendors: reading %s: %w", path, err)
	}

	// Strip any Glider block already present before appending ours, and
	// treat THAT as the state to restore. Self-healing: a block left behind
	// by a run whose revert never executed (glider.exe force-killed — see
	// internal/runstate for how often that really happens) gets removed by
	// the next delegate to the same file, instead of accumulating forever
	// in a file the user owns. Restoring `original` rather than `onDisk` is
	// the load-bearing detail; restoring what was on disk would faithfully
	// put the stale block back.
	original := stripContextPack(onDisk)

	var next []byte
	if existed {
		body := string(original)
		if strings.TrimSpace(body) != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if strings.TrimSpace(body) != "" {
			body += "\n"
		}
		next = []byte(body + pack.Render())
	} else {
		next = []byte(pack.Render())
	}

	if err := os.WriteFile(path, next, 0o644); err != nil {
		unlock()
		return noop, fmt.Errorf("vendors: writing %s: %w", path, err)
	}

	return func() error {
		defer unlock()
		if !existed {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("vendors: removing %s: %w", path, err)
			}
			return nil
		}
		if err := os.WriteFile(path, original, 0o644); err != nil {
			return fmt.Errorf("vendors: restoring %s: %w", path, err)
		}
		return nil
	}, nil
}

// stripContextPack removes a Glider-written block (and the blank line that
// preceded it) from content, returning it unchanged when no block is
// present. Tolerates a truncated file with a BEGIN marker but no END —
// exactly what a crash mid-write would leave — by cutting to end of file
// rather than giving up and preserving the corruption.
func stripContextPack(content []byte) []byte {
	s := string(content)
	start := strings.Index(s, contextPackBegin)
	if start < 0 {
		return content
	}
	rest := s[start:]
	endIdx := strings.Index(rest, contextPackEnd)
	var tail string
	if endIdx < 0 {
		tail = "" // truncated block — drop through end of file
	} else {
		tail = rest[endIdx+len(contextPackEnd):]
		tail = strings.TrimPrefix(tail, "\n")
	}
	head := strings.TrimRight(s[:start], "\n \t")
	if head == "" && strings.TrimSpace(tail) == "" {
		return nil
	}
	if head != "" {
		head += "\n"
	}
	return []byte(head + tail)
}
