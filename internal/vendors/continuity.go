package vendors

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Continuity is Glider's own running record of the human turns it has
// observed, per workspace, attributed per origin.
//
// Why it exists: ContextPack sources conversation history from the FRONT's
// own request via OriginAdapter.PriorUserInstructions, which makes context
// quality hostage to each vendor's wire format. cursor-agent genuinely
// cannot supply it — ReadRequestBody deliberately reads only the first
// Connect envelope, because reading further blocks ~30s on the client's own
// bidi keepalives and kills the request. So a cursor-agent-fronted delegate
// got no history at all.
//
// The insight this is built on: Glider isn't blind to those turns, it just
// sees them ONE AT A TIME and used to discard each one. It observes every
// intercepted request; it simply never accumulated. Continuity accumulates.
// That makes history uniform across all three fronts without touching the
// envelope-reading constraint at all.
//
// Entries are attributed to their origin (vendor + PID) rather than pooled:
// two CLIs open in the same repo are two unrelated conversations, and
// interleaving them would hand a delegate a confusing mixture of both.
type ContinuityEntry struct {
	At           time.Time
	OriginVendor string
	OriginPID    uint32
	Text         string
}

const (
	// continuityMaxEntries bounds the file. A ring, not a transcript —
	// this is orienting context, and an unbounded log of every prompt a
	// user ever typed is both a privacy liability and useless to a model.
	continuityMaxEntries = 60
	// continuityMaxTextLen truncates one entry. A pasted stack trace or
	// file dump is not orienting context and would crowd out real turns.
	continuityMaxTextLen = 400
)

// continuityLine is the on-disk shape: valid markdown a human can read,
// with enough structure to parse back reliably.
//
//	- [2026-07-31T09:15:00Z] (claude#40112) refactor the auth module
var continuityLine = regexp.MustCompile(`^- \[([^\]]+)\] \(([^#)]*)#(\d+)\) (.*)$`)

// ContinuityPath returns the record file for a workspace. Kept under
// ~/.glider rather than inside the workspace deliberately: this is Glider's
// bookkeeping, not a project artifact, and writing it into a repo the user
// owns would show up in their git status forever.
func ContinuityPath(workspace string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(workspace))))
	name := sanitizeForFilename(filepath.Base(workspace)) + "-" + hex.EncodeToString(sum[:4]) + ".md"
	return filepath.Join(home, ".glider", "continuity", name), nil
}

func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "workspace"
	}
	return out
}

var continuityLocks sync.Map // path -> *sync.Mutex

func lockForContinuity(path string) *sync.Mutex {
	actual, _ := continuityLocks.LoadOrStore(path, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// RecordContinuity appends one observed human turn. Best-effort by design:
// every error is returned but callers are expected to ignore it — failing to
// record background context must never break the request the user actually
// made.
//
// Duplicate suppression matters more than it looks: a front CLI can re-send
// the same turn across retries (cursor-agent reconnects several times per
// call under some conditions), and without this the same instruction would
// stack up and crowd the ring.
func RecordContinuity(workspace, originVendor string, originPID uint32, text string) error {
	text = singleLine(strings.TrimSpace(text))
	if workspace == "" || text == "" {
		return nil
	}
	if len(text) > continuityMaxTextLen {
		text = text[:continuityMaxTextLen] + "…"
	}

	path, err := ContinuityPath(workspace)
	if err != nil {
		return err
	}
	mu := lockForContinuity(path)
	mu.Lock()
	defer mu.Unlock()

	entries, _ := readContinuityFile(path)
	if n := len(entries); n > 0 {
		last := entries[n-1]
		if last.Text == text && last.OriginPID == originPID {
			return nil // same turn re-sent (client retry) — don't stack it
		}
	}
	entries = append(entries, ContinuityEntry{
		At: time.Now().UTC(), OriginVendor: originVendor, OriginPID: originPID, Text: text,
	})
	if len(entries) > continuityMaxEntries {
		entries = entries[len(entries)-continuityMaxEntries:]
	}
	return writeContinuityFile(path, workspace, entries)
}

// ReadContinuity returns up to max prior turns for this origin, oldest
// first, EXCLUDING the most recent one (which is the delegate's own task,
// already restated in the pack — repeating it as background reads like a
// duplicate request).
//
// Scoped to the requesting origin: entries from a different CLI or a
// different process in the same workspace are a different conversation.
// Falls back to nothing rather than to another session's turns — wrong
// context is worse than none.
func ReadContinuity(workspace, originVendor string, originPID uint32, max int) []string {
	if workspace == "" || max <= 0 {
		return nil
	}
	path, err := ContinuityPath(workspace)
	if err != nil {
		return nil
	}
	mu := lockForContinuity(path)
	mu.Lock()
	defer mu.Unlock()

	entries, err := readContinuityFile(path)
	if err != nil {
		return nil
	}

	var mine []string
	for _, e := range entries {
		if e.OriginPID == originPID || (originPID == 0 && e.OriginVendor == originVendor) {
			mine = append(mine, e.Text)
		}
	}
	if len(mine) <= 1 {
		return nil // only the current turn (or nothing) — no background to give
	}
	mine = mine[:len(mine)-1] // drop the current turn
	if len(mine) > max {
		mine = mine[len(mine)-max:]
	}
	return mine
}

func readContinuityFile(path string) ([]ContinuityEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []ContinuityEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		m := continuityLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue // header/prose lines are fine to skip
		}
		at, err := time.Parse(time.RFC3339, m[1])
		if err != nil {
			continue
		}
		pid, err := strconv.ParseUint(m[3], 10, 32)
		if err != nil {
			continue
		}
		entries = append(entries, ContinuityEntry{
			At: at, OriginVendor: m[2], OriginPID: uint32(pid), Text: m[4],
		})
	}
	return entries, sc.Err()
}

func writeContinuityFile(path, workspace string, entries []ContinuityEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Glider session continuity\n\n")
	fmt.Fprintf(&b, "Workspace: `%s`\n\n", workspace)
	b.WriteString("Human turns Glider observed here, newest last, attributed by origin CLI and PID. ")
	b.WriteString("Used to give delegated CLIs orienting context when the front's own protocol can't supply history. ")
	fmt.Fprintf(&b, "Bounded to the last %d entries. Safe to delete at any time.\n\n", continuityMaxEntries)
	for _, e := range entries {
		fmt.Fprintf(&b, "- [%s] (%s#%d) %s\n",
			e.At.UTC().Format(time.RFC3339), e.OriginVendor, e.OriginPID, e.Text)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
