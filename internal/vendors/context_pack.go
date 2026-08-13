package vendors

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContextPack is the session context that Glider gives to a delegated CLI.
// Thus that CLI does not start cold.
//
// The problem that it solves: a delegate subprocess received exactly two
// items, the text of the prompt and a work directory. It knew nothing about
// the larger task, about the decisions that a person had already made, or
// about which CLI asked it to work. A task such as "add a line to this file"
// operates in that condition. A task such as "continue the work that we
// started" does not.
//
// The mechanism is NOT an addition to the prompt, and this is on purpose.
// Each delegate gets its OWN private directory with its own context file.
// Glider gives that file to the CLI with a flag that the vendor already
// accepts. Therefore the CLI reads it during the usual start of a session.
// This gives three results:
//
//   - It costs nothing in the prompt. MaxPromptLen does not change, and none
//     of this content competes with the text of the task for a limited budget.
//   - It does not depend on a decision by the delegate. To read the file is
//     part of the start of the CLI. It is not a tool call that the delegate
//     can omit, and that risk is the cause that removed a design with a pull
//     through MCP.
//   - It shares no state that code can change. That is the full purpose of one
//     directory for each delegate, which the next paragraph gives.
//
// The cause for one directory for each delegate, and not the CLAUDE.md or
// AGENTS.md file of the project: that first design changed on 2026-07-31. To
// write in the file that the user owns needed all of this work. Code had to
// do five things. It had to add content to the file. It had to put the
// original bytes back after the run. It had to lock the file for the full
// life of the subprocess. It had to correct blocks that a run left after a
// failure. And it had to operate with less function when it could not get the
// lock.
//
// Each of those problems comes from one file that many processes change. One
// directory for each delegate removes all of them at the same time. It also
// removes the risk of most importance: Glider can no longer damage a file
// that the user wrote.
//
// A live test on 2026-07-31 confirmed the delivery for each vendor, and no
// person assumed it. Each test asked for a token that was ONLY in the context
// file:
//
// 	claude        --append-system-prompt-file=<file>  (operates; --help does not show it)
// 	cursor-agent  --add-dir=<dir>                     (reads AGENTS.md there)
// 	agy           --add-dir=<dir>                     (reads AGENTS.md there)
//
// Those flags are in configs/vendor_candidates.yaml as usual template args.
// Therefore the flag that a vendor needs stays data, and it is never a test
// on the name of a vendor.
type ContextPack struct {
	// Task is the delegated instruction itself, restated so the file is
	// self-contained if the CLI surfaces it out of order.
	Task string
	// FrontVendor is the CLI that the person used, for example "claude". It is
	// empty when the code could not identify the origin process.
	FrontVendor string
	// Workspace is the directory the delegate runs in.
	Workspace string
	// RecentTurns are prior human instructions from the calling session,
	// oldest first, already scaffold-stripped. May be empty: not every
	// front's wire format exposes retrievable history (continuity.go fills
	// that gap).
	RecentTurns []string
}

// DefaultContextTurns limits how many earlier instructions from a person go
// to a delegated CLI. The objective is context that orients the delegate. An
// example of such context is "we were changing the structure of auth, and we
// decided to keep the shape of the token". The objective is not to send the
// session again.
//
// But the old value of 6 was much less than that objective. It was small
// enough that the count always applied before any budget of tokens could
// apply.
//
// This is a var, and not a const. Therefore SetBackgroundLimits can keep it
// in agreement with context.background.max_entries.
//
// It is here, and not in one of the callers. BOTH entry points for a delegate
// must use the same limit. Those are the transparent path in internal/mitm
// and the gateway route in internal/api. A delegate must not get more context
// or less context because of the path that its caller used.
var DefaultContextTurns = defaultMaxEntries

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
	// This text prevents recursion, and it speaks to the delegate directly.
	//
	// A user who uses the rules for automatic delegation puts them in the context
	// file of the project. Refer to docs/instructions.md. This delegate also reads
	// that file. Therefore, with no line such as this one, the delegate could add
	// its own "/vendor" flag and send the task to a third CLI. Each step pays a
	// full start.
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
	// Use boundBackground here, and not at each caller.
	//
	// This is the one point that each source uses: the history of the front CLI, and
	// the continuity record of Glider. Therefore no later caller can go around the
	// limits, and no later caller can forget to apply them.
	//
	// Before this, only the continuity path had a limit. The path from the front CLI
	// had none, and Claude Code sends its FULL record with each call.
	if turns := boundBackground(p.RecentTurns); len(turns) > 0 {
		b.WriteString("\n## Earlier in the calling session\n\n")
		b.WriteString("Most recent last. Background only — do not re-do this work:\n\n")
		for _, t := range turns {
			if t = strings.TrimSpace(t); t != "" {
				fmt.Fprintf(&b, "- %s\n", singleLine(t))
			}
		}
	}
	return b.String()
}

// singleLine makes one line from an instruction that has more than one line.
// Therefore the list that the code writes cannot add a structure to the file by
// accident. An example of such a structure is a "## " at the start of a
// line.
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
// than a counter so two Glider processes sharing a home directory cannot
// collide on the same directory name.
func newDelegateID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "delegate"
	}
	return hex.EncodeToString(b[:])
}

// PrepareContextDir writes pack in a new private directory, for one delegate
// run. It returns the directory and the file, and it returns a function that
// removes them.
//
// contextFile is the NAME of the file that the vendor expects. cursor-agent and
// agy expect AGENTS.md, and they read a directory. claude expects a file
// directly.
//
// A caller puts those values in the args of a template. substituteTemplateArgs
// removes each arg that has no value for this run.
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

// SweepDelegateContextDirs removes each directory of a delegate that stays
// after a run. A forceful stop passes the code that removes them, and
// internal/runstate has its own detector for that condition, because it
// occurs frequently.
//
// Call this function at the start. There, the statement "each directory here
// is old" is true by construction. A live delegate can exist only inside a
// Glider that operates. And this code runs before any delegate.
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
