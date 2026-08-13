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

	"github.com/glider-ai/glider/internal/procutil"
)

// Continuity is the record that Glider keeps of the turns of a person that it
// observes. It keeps one record for each workspace, and it attributes each
// entry to its origin.
//
// The cause for this record: ContextPack takes the history of the
// conversation from the request of the FRONT CLI, with
// OriginAdapter.PriorUserInstructions. Therefore the quality of the context
// depends on the wire format of each vendor.
//
// cursor-agent truly cannot give that history. ReadRequestBody reads only the
// first Connect envelope, and this is on purpose. A read of more blocks for
// approximately 30 seconds, on the keepalive envelopes of the client. That
// stops the request. Therefore a delegate with cursor-agent at the front got
// no history.
//
// This is the idea that the record uses: Glider is not blind to those turns.
// It sees them ONE AT A TIME, and it discarded each one. It observes each
// request that it intercepts, and it only kept nothing. Continuity keeps
// them. Thus the history is the same for all three fronts, and this needs no
// change to the limit on the read of the envelope.
//
// The code attributes each entry to its origin, which is a vendor and a PID.
// It does not put them together. Two CLIs in the same repository are two
// conversations with no relation, and to mix them would give a delegate a
// mixture of both.
type ContinuityEntry struct {
	At           time.Time
	OriginVendor string
	OriginPID    uint32
	// Kind is KindTurn or KindDelegate. Empty means a human turn.
	Kind string
	Text string
}

const (
	// continuityMaxEntries limits the file. This is a ring, and it is not a full
	// record. The objective is context that orients a delegate. A log with no
	// limit holds each prompt that a user ever typed. That is a risk to privacy,
	// and it is of no use to a model.
	continuityMaxEntries = 60
	// continuityMaxTextLen limits the length of one entry, in characters.
	//
	// 400 was much too small: approximately 100 tokens, which cut a usual
	// instruction of some sentences in half. 4000 is approximately 1000 tokens.
	// Therefore a long instruction, or a stack trace of a usual size, stays
	// complete. And one very large paste still cannot use the full budget for the
	// background.
	continuityMaxTextLen = 4000
)

// The kinds of an entry. An entry on the disk with no kind is a turn of a
// person. Therefore each old file that a person wrote before this code
// recorded the results of a delegate still reads correctly.
const (
	// KindTurn is an instruction Glider observed a person type.
	KindTurn = ""
	// KindDelegate is the OUTCOME of a delegate run: which vendor ran, and
	// what it changed. Recorded so a later delegate in the same workspace
	// knows what an earlier one already did. Without it, delegate #2
	// re-derives, or undoes, delegate #1's work.
	KindDelegate = "delegate"
)

// continuityLine is the shape on the disk. It is correct markdown that a
// person can read, and it has sufficient structure for the code to read it
// again with confidence.
//
//	- [2026-07-31T09:15:00Z] (claude#40112) refactor the auth module
//	- [2026-07-31T09:16:40Z] (claude#40112) {delegate} agy: renamed Serve
//
// The {kind} group is optional. Therefore each file that a person wrote before
// this code recorded the results of a delegate still reads correctly, as plain
// turns of a person.
var continuityLine = regexp.MustCompile(`^- \[([^\]]+)\] \(([^#)]*)#(\d+)\)(?: \{([a-z]+)\})? (.*)$`)

// ContinuityPath gives the record file for a workspace.
//
// The file stays under ~/.glider, and not inside the workspace. This is on
// purpose. This is the bookkeeping of Glider, and it is not a part of the
// project. To write it in a repository that the user owns would show it in the
// git status of that user permanently.
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

// RecordContinuity adds one turn of a person that Glider observed.
//
// A failure here is acceptable, by design. The function returns each error,
// but a caller must ignore it. A failure to record the background context must
// never stop the request that the user truly made.
//
// The removal of a repeated turn is more important than it appears. A front
// CLI can send the same turn again after a retry, and cursor-agent connects
// again some times for one call in some conditions. Without this removal, the
// same instruction would collect and fill the ring.
func RecordContinuity(workspace, originVendor string, originPID uint32, text string) error {
	return recordContinuity(workspace, originVendor, originPID, KindTurn, text)
}

// RecordDelegateOutcome records what a delegate run actually did, so a later
// delegate in the same workspace is not blind to it.
//
// This is the half that was missing: Glider captured EditViews, rendered them
// for the human, and then discarded them. A second delegate therefore had no
// way to know a first had already renamed the function it was about to
// rename. The code records this under the identity of the FRONT CLI, which is
// the session that sent the work. It does not use the identity of the
// delegate. That front session is the conversation that the result belongs
// to. And the PID of the delegate is inside the kill-on-close job, which
// recordContinuity refuses on purpose.
func RecordDelegateOutcome(workspace, originVendor string, originPID uint32, delegateVendor, summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil
	}
	return recordContinuity(workspace, originVendor, originPID, KindDelegate,
		delegateVendor+": "+summary)
}

func recordContinuity(workspace, originVendor string, originPID uint32, kind, text string) error {
	text = singleLine(strings.TrimSpace(text))
	if workspace == "" || text == "" {
		return nil
	}

	// Never record an origin Glider could not identify. A live test confirmed
	// this on 2026-07-31. The code recorded a plain `curl` against the gateway
	// as "(#56632)". That is a true process, but it is not a CLI session.
	// Therefore no code can give its turn to any conversation, and that turn
	// only adds noise to a file that must orient a delegate.
	if strings.TrimSpace(originVendor) == "" {
		return nil
	}

	// Never record Glider's OWN delegate subprocesses. A live test confirmed a
	// second condition on the same day. Glider intercepted the request of a
	// cursor-agent process that it started itself. It then recorded that request
	// as if a person typed the task. Therefore Glider recorded its own output as
	// the record of the session. That record then goes to the context of the
	// next delegate. Reuses the job-object membership check that already
	// identifies delegate subprocesses elsewhere (it covers child processes too,
	// e.g. cursor-agent.cmd's real node.exe, which is the process that actually
	// owns the connection).
	if procutil.IsInDelegateSubprocessJob(originPID) {
		return nil
	}
	text = truncateMiddleOut(text, continuityMaxTextLen)

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
		if last.Text == text && last.OriginPID == originPID && last.Kind == kind {
			return nil // same turn re-sent (client retry) — don't stack it
		}
	}
	entries = append(entries, ContinuityEntry{
		At: time.Now().UTC(), OriginVendor: originVendor, OriginPID: originPID,
		Kind: kind, Text: text,
	})
	if len(entries) > continuityMaxEntries {
		entries = entries[len(entries)-continuityMaxEntries:]
	}
	return writeContinuityFile(path, workspace, entries)
}

// ReadContinuity returns a maximum of max earlier turns for this origin, and
// the oldest is first. It EXCLUDES the newest turn. That newest turn is the
// task of the delegate, and the pack already gives it. To give it again as
// background looks like a second request.
//
// Scoped to the requesting origin: entries from a different CLI or a
// different process in the same workspace are a different conversation. Falls
// back to nothing rather than to another session's turns — wrong context is
// worse than none.
func ReadContinuity(workspace, originVendor string, originPID uint32, max int) []string {
	return ReadContinuityFor(workspace, originVendor, originPID, "", max)
}

// ReadContinuityFor is ReadContinuity with the delegate's own task supplied,
// so entries can be ranked against it instead of taken by recency alone. A
// blank task degrades to pure recency, which is what ReadContinuity does.
func ReadContinuityFor(workspace, originVendor string, originPID uint32, task string, max int) []string {
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

	mine := entriesForOrigin(entries, originVendor, originPID)
	if len(mine) <= 1 {
		return nil // only the current turn (or nothing) — no background to give
	}
	// Drop the current turn, but only if it IS one. A delegate outcome is
	// never the caller's own instruction, so trimming it would silently throw
	// away the most useful entry in the file.
	if mine[len(mine)-1].Kind == KindTurn {
		mine = mine[:len(mine)-1]
	}
	return SelectContinuity(mine, task, max, BackgroundTokenBudget())
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
			At: at, OriginVendor: m[2], OriginPID: uint32(pid), Kind: m[4], Text: m[5],
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
	b.WriteString("Human turns Glider observed here, plus the outcome of each delegate run, newest last, ")
	b.WriteString("attributed by origin CLI and PID. A `{delegate}` entry is what a delegated CLI did, not what a person typed. ")
	b.WriteString("Used to give delegated CLIs orienting context when the front's own protocol can't supply history. ")
	fmt.Fprintf(&b, "Bounded to the last %d entries. Safe to delete at any time.\n\n", continuityMaxEntries)
	for _, e := range entries {
		kind := ""
		if e.Kind != KindTurn {
			kind = " {" + e.Kind + "}"
		}
		fmt.Fprintf(&b, "- [%s] (%s#%d)%s %s\n",
			e.At.UTC().Format(time.RFC3339), e.OriginVendor, e.OriginPID, kind, e.Text)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
