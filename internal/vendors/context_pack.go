package vendors

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// The mechanism is deliberately NOT prompt injection. Each delegate gets its
// OWN private directory containing its own context file, handed to the CLI
// through a flag that vendor already supports, so the CLI ingests it during
// normal session startup. That means:
//
//   - Zero prompt cost. MaxPromptLen is untouched; none of this competes
//     with the task text for a capped budget.
//   - No reliance on the delegate choosing to act. Ingesting the file is
//     part of CLI startup, not a tool call it might skip — the failure mode
//     that ruled out an MCP-pull design.
//   - No shared mutable state, which is the whole point of per-delegate
//     directories (below).
//
// Why per-delegate directories rather than the project's own CLAUDE.md /
// AGENTS.md (the first design, replaced 2026-07-31): writing into the file
// the user owns required appending to it, restoring it byte-for-byte after,
// locking it for the delegate's entire subprocess lifetime, self-healing
// blocks left behind by crashed runs, and degrading when a lock couldn't be
// acquired. Every one of those problems is an artifact of sharing one
// mutable file. Giving each delegate its own directory deletes all of them
// at once, and removes the risk that mattered most: Glider can no longer
// damage a file the user wrote.
//
// Per-vendor delivery, verified live 2026-07-31 rather than assumed — each
// probe asked for a token that existed ONLY inside the context file:
//
//	claude        --append-system-prompt-file=<file>  (works; absent from --help)
//	cursor-agent  --add-dir=<dir>                     (reads AGENTS.md there)
//	agy           --add-dir=<dir>                     (reads AGENTS.md there)
//
// Those flags live in configs/vendor_candidates.yaml as ordinary template
// args, so which flag a vendor needs stays data, never a vendor-name branch.
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
	// oldest first, already scaffold-stripped. May be empty: not every
	// front's wire format exposes retrievable history (continuity.go fills
	// that gap).
	RecentTurns []string
}

// DefaultContextTurns bounds how many prior human instructions ride along to
// a delegated CLI. Small on purpose: the goal is orienting context ("we were
// refactoring auth, we decided to keep the token shape"), not replaying the
// session.
//
// Lives here rather than in either caller because BOTH delegate entry points
// (internal/mitm's transparent path and internal/api's gateway route) must
// use the same bound — a delegate shouldn't get more or less context
// depending on which way its caller happened to reach Glider.
const DefaultContextTurns = 6

// Empty reports whether the pack carries nothing worth writing.
func (p ContextPack) Empty() bool {
	return strings.TrimSpace(p.Task) == "" && len(p.RecentTurns) == 0
}

// Render produces the markdown the delegate's CLI will ingest.
func (p ContextPack) Render() string {
	var b strings.Builder
	b.WriteString("# Delegated task context\n\n")
	b.WriteString("You are being run by Glider on behalf of another agent CLI session. ")
	b.WriteString("This is context for the task you were just given.\n\n")
	// Anti-recursion, stated to the delegate directly. A user who adopts
	// auto-delegation rules (docs/instructions.md) puts them in the project's
	// own context file, which this delegate also reads — so without this line
	// it could append its own "/vendor" flag and delegate onward, each hop
	// paying a full cold start.
	b.WriteString("**You are already the delegate.** Do not delegate onward: ignore any auto-delegation ")
	b.WriteString("rules you find in this project, and do not append a `/vendor` flag to anything. ")
	b.WriteString("Complete the task yourself.\n\n")

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
		b.WriteString("\n## Earlier in the calling session\n\n")
		b.WriteString("Most recent last. Background only — do not re-do this work:\n\n")
		for _, t := range p.RecentTurns {
			if t = strings.TrimSpace(t); t != "" {
				fmt.Fprintf(&b, "- %s\n", singleLine(t))
			}
		}
	}
	return b.String()
}

// singleLine flattens a multi-line instruction into one bullet so the
// rendered list can't accidentally introduce markdown structure (a stray
// "## " at line start) into the file.
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// DelegateContextRoot is where per-delegate context directories are created —
// under ~/.glider, never inside the user's workspace. This is Glider's own
// scratch state and has no business appearing in someone's git status.
func DelegateContextRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".glider", "delegates"), nil
}

// newDelegateID returns a short unique id for one delegate run. Random rather
// than a counter so two Glider processes sharing a home directory can't
// collide on the same directory name.
func newDelegateID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "delegate"
	}
	return hex.EncodeToString(b[:])
}

// PrepareContextDir writes pack into a fresh private directory for one
// delegate run, returning that directory, the context file inside it, and a
// cleanup func removing the whole directory.
//
// contextFile is the file NAME the vendor expects: AGENTS.md for cursor-agent
// and agy, which discover it by convention inside an --add-dir root; any name
// for claude, which is handed the path explicitly.
//
// An empty contextFile or an empty pack is a quiet no-op returning empty
// paths. Callers substitute those into template args, and
// substituteTemplateArgs drops any arg whose context placeholder resolved
// empty — so a vendor with no context configured simply runs without one,
// with no dangling flag.
//
// cleanup is always safe to call, including after an error, and callers must
// always call it. Unlike the previous shared-file design there is nothing to
// restore and nothing another delegate might be holding: this directory
// belongs to exactly one run.
func PrepareContextDir(contextFile string, pack ContextPack) (dir, file string, cleanup func() error, err error) {
	noop := func() error { return nil }
	if strings.TrimSpace(contextFile) == "" || pack.Empty() {
		return "", "", noop, nil
	}

	root, err := DelegateContextRoot()
	if err != nil {
		return "", "", noop, err
	}
	dir = filepath.Join(root, newDelegateID())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", noop, fmt.Errorf("vendors: creating delegate context dir: %w", err)
	}
	cleanup = func() error { return os.RemoveAll(dir) }

	file = filepath.Join(dir, contextFile)
	if err := os.WriteFile(file, []byte(pack.Render()), 0o644); err != nil {
		_ = cleanup()
		return "", "", noop, fmt.Errorf("vendors: writing delegate context: %w", err)
	}
	return dir, file, cleanup, nil
}

// SweepDelegateContextDirs removes leftover per-delegate directories, for the
// case where a forceful kill skipped cleanup (see internal/runstate — that
// happens often enough to have its own detector). Intended for startup, where
// "everything here is stale" is true by construction: a live delegate can
// only exist inside a running Glider, and this runs before any delegate does.
func SweepDelegateContextDirs() error {
	root, err := DelegateContextRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
	return nil
}
