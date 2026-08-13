package vendors_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/vendors"
)

// withHome points ~/.glider at a temp dir so these tests never touch the
// developer's real continuity records.
func withHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(k, dir)
	}
}

func entry(ts string, pid uint32, kind, text string) vendors.ContinuityEntry {
	at, _ := time.Parse(time.RFC3339, ts)
	return vendors.ContinuityEntry{At: at, OriginVendor: "claude", OriginPID: pid, Kind: kind, Text: text}
}

// A selection by time alone gives a delegate the subject that the session
// touched last. That subject is not the subject of the task of the delegate. To
// rank the entries against the task is the correction.
func TestSelectContinuity_RanksAgainstTheTaskNotJustRecency(t *testing.T) {
	entries := []vendors.ContinuityEntry{
		entry("2026-08-01T10:00:00Z", 10, "", "fix the oauth redirect dropping the session cookie"),
		entry("2026-08-01T10:05:00Z", 10, "", "the cookie domain change did not help either"),
		entry("2026-08-01T11:00:00Z", 10, "", "make the button padding larger on mobile"),
		entry("2026-08-01T11:05:00Z", 10, "", "use a lighter grey for the card border"),
		entry("2026-08-01T11:10:00Z", 10, "", "increase the heading font size"),
		entry("2026-08-01T11:15:00Z", 10, "", "align the footer links"),
	}

	got := vendors.SelectContinuity(entries, "the oauth session cookie is still dropped, find the cause", 4, 700)

	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "oauth redirect") {
		t.Errorf("the entry about oauth was not selected for an oauth task.\ngot: %s", joined)
	}
	if !strings.Contains(joined, "cookie domain") {
		t.Errorf("the second oauth entry was not selected.\ngot: %s", joined)
	}
}

// The protected tail is unconditional: however good the ranking, what the
// user just said is never background noise.
func TestSelectContinuity_AlwaysKeepsTheRecentTail(t *testing.T) {
	entries := []vendors.ContinuityEntry{
		entry("2026-08-01T10:00:00Z", 10, "", "alpha beta gamma"),
		entry("2026-08-01T10:01:00Z", 10, "", "delta epsilon zeta"),
		entry("2026-08-01T10:02:00Z", 10, "", "wholly unrelated penultimate turn"),
		entry("2026-08-01T10:03:00Z", 10, "", "wholly unrelated final turn"),
	}
	got := vendors.SelectContinuity(entries, "alpha beta gamma", 4, 700)
	joined := strings.Join(got, " | ")
	for _, want := range []string{"penultimate", "final"} {
		if !strings.Contains(joined, want) {
			t.Errorf("protected tail entry %q was dropped.\ngot: %s", want, joined)
		}
	}
}

// A token budget, not a fixed count: a few huge entries must not blow past it.
func TestSelectContinuity_RespectsTheTokenBudget(t *testing.T) {
	big := strings.Repeat("payload ", 200) // ~1600 chars, ~400 tokens each
	entries := []vendors.ContinuityEntry{
		entry("2026-08-01T10:00:00Z", 10, "", "payload one "+big),
		entry("2026-08-01T10:01:00Z", 10, "", "payload two "+big),
		entry("2026-08-01T10:02:00Z", 10, "", "payload three "+big),
		entry("2026-08-01T10:03:00Z", 10, "", "tail one"),
		entry("2026-08-01T10:04:00Z", 10, "", "tail two"),
	}
	got := vendors.SelectContinuity(entries, "payload", 5, 200)

	total := 0
	for _, g := range got {
		total += len(g)
	}
	// The protected tail is exempt, so allow it; the point is that the three
	// oversized entries did not all get through.
	if len(got) > 3 {
		t.Fatalf("budget ignored: selected %d entries (%d chars)", len(got), total)
	}
}

// Truncation used to cut the tail, which throws away the part of an
// instruction that says what to do.
func TestSelectContinuity_MiddleOutKeepsBothEnds(t *testing.T) {
	withHome(t)
	ws := t.TempDir()
	long := "REFACTOR THE AUTH MODULE " + strings.Repeat("filler ", 200) + " AND KEEP THE TESTS GREEN"
	if err := vendors.RecordContinuity(ws, "claude", 4242, long); err != nil {
		t.Fatalf("record: %v", err)
	}
	path, _ := vendors.ContinuityPath(ws)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "REFACTOR THE AUTH MODULE") {
		t.Error("the opening of the instruction was lost")
	}
	if !strings.Contains(body, "KEEP THE TESTS GREEN") {
		t.Error("the END of the instruction was lost — this is the tail-cut bug")
	}
}

// A CLI restart produces a new PID. Its own earlier history must not vanish.
func TestReadContinuity_SurvivesAnOriginRestart(t *testing.T) {
	withHome(t)
	ws := t.TempDir()

	// This is an earlier run of the same CLI. PID 1 is not a plausible agent CLI on
	// either platform. And ProcessAlive answers "cannot tell" as true. Therefore
	// this test uses a value that is certainly dead.
	const deadPID = 4294967294
	for _, txt := range []string{"first session turn one", "first session turn two"} {
		if err := vendors.RecordContinuity(ws, "claude", deadPID, txt); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// The restarted CLI, with a live PID (this test process).
	livePID := uint32(os.Getpid())
	if err := vendors.RecordContinuity(ws, "claude", livePID, "second session current turn"); err != nil {
		t.Fatalf("record: %v", err)
	}

	got := vendors.ReadContinuity(ws, "claude", livePID, 6)
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "first session turn one") {
		t.Errorf("history from before the restart was lost.\ngot: %s", joined)
	}
}

// Two CLIs running side by side stay separate — the restart fix must not
// reintroduce the mixing PID scoping exists to prevent.
func TestReadContinuity_DoesNotMixTwoLiveSessions(t *testing.T) {
	withHome(t)
	ws := t.TempDir()

	livePID := uint32(os.Getpid())
	if err := vendors.RecordContinuity(ws, "claude", livePID, "session A private turn"); err != nil {
		t.Fatalf("record: %v", err)
	}
	// A second process that is also live. The parent of this process is live
	// too, but the most simple method is to use the PID of this process again
	// under a different vendor. The scoping must also keep those two
	// separate.
	if err := vendors.RecordContinuity(ws, "cursor-agent", livePID, "session B private turn"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := vendors.RecordContinuity(ws, "claude", livePID, "session A second turn"); err != nil {
		t.Fatalf("record: %v", err)
	}

	got := vendors.ReadContinuity(ws, "claude", livePID, 6)
	for _, g := range got {
		if strings.Contains(g, "session B") {
			t.Errorf("a live session's turns leaked across vendors: %q", g)
		}
	}
}

// The result of a delegate run must reach the next delegate. If it does not,
// that delegate does the work again, or it removes the work.
func TestRecordDelegateOutcome_ReachesTheNextDelegate(t *testing.T) {
	withHome(t)
	ws := t.TempDir()
	pid := uint32(os.Getpid())

	if err := vendors.RecordContinuity(ws, "claude", pid, "rename Handler.Serve across the repo"); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := vendors.RecordDelegateOutcome(ws, "claude", pid, "cursor-agent",
		"renamed Handler.Serve to Handler.Dispatch in 12 files"); err != nil {
		t.Fatalf("outcome: %v", err)
	}
	if err := vendors.RecordContinuity(ws, "claude", pid, "now update the docs"); err != nil {
		t.Fatalf("record: %v", err)
	}

	got := vendors.ReadContinuityFor(ws, "claude", pid, "rename Handler.Serve", 6)
	joined := strings.Join(got, " | ")
	if !strings.Contains(joined, "Handler.Dispatch") {
		t.Fatalf("the delegate's outcome never reached the next delegate.\ngot: %s", joined)
	}
	if !strings.Contains(joined, "cursor-agent:") {
		t.Errorf("the outcome did not say which CLI produced it.\ngot: %s", joined)
	}
}

// Old records, written before entry kinds existed, must still parse.
func TestReadContinuity_ParsesRecordsWrittenBeforeKindsExisted(t *testing.T) {
	withHome(t)
	ws := t.TempDir()
	path, _ := vendors.ContinuityPath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pid := os.Getpid()
	old := "# Glider session continuity\n\n" +
		"- [2026-07-30T10:00:00Z] (claude#" + itoa(pid) + ") an older turn with no kind marker\n" +
		"- [2026-07-30T10:01:00Z] (claude#" + itoa(pid) + ") another older turn\n" +
		"- [2026-07-30T10:02:00Z] (claude#" + itoa(pid) + ") the newest turn\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := vendors.ReadContinuity(ws, "claude", uint32(pid), 6)
	if len(got) == 0 {
		t.Fatal("a pre-kinds continuity file parsed as empty — old records were dropped")
	}
	if !strings.Contains(strings.Join(got, " | "), "older turn with no kind marker") {
		t.Errorf("old entries did not survive the format change: %v", got)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// Compaction must never lose the delegate outcomes or the opening turns, even
// with no summarizer registered at all.
func TestCompactContinuity_DeterministicDigestKeepsWhatMatters(t *testing.T) {
	withHome(t)
	ws := t.TempDir()
	pid := uint32(os.Getpid())

	if err := vendors.RecordContinuity(ws, "claude", pid, "GOAL: migrate the session store to postgres"); err != nil {
		t.Fatalf("record: %v", err)
	}
	for i := 0; i < vendors.CompactThreshold-8; i++ {
		if err := vendors.RecordContinuity(ws, "claude", pid, "routine turn number "+itoa(i)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if err := vendors.RecordDelegateOutcome(ws, "claude", pid, "cursor-agent", "added the migration file"); err != nil {
		t.Fatalf("outcome: %v", err)
	}
	for i := 0; i < 20; i++ {
		if err := vendors.RecordContinuity(ws, "claude", pid, "later turn number "+itoa(i)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	res, err := vendors.CompactContinuity(context.Background(), ws)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.Compacted == 0 {
		t.Fatal("nothing was compacted")
	}
	if res.Source != vendors.SummaryNone {
		t.Fatalf("no summarizer is registered, so the source should be none, got %q", res.Source)
	}

	path, _ := vendors.ContinuityPath(ws)
	data, _ := os.ReadFile(path)
	body := string(data)
	if !strings.Contains(body, "migrate the session store") {
		t.Error("compaction lost the opening turn that states the goal")
	}
	if !strings.Contains(body, "added the migration file") {
		t.Error("compaction lost a delegate outcome — a later delegate could now redo it")
	}
}

// Compacting an already-compacted record must be a no-op, or repeated passes
// summarize summaries and the record degrades to noise.
func TestCompactContinuity_DoesNotCompactTwice(t *testing.T) {
	withHome(t)
	ws := t.TempDir()
	pid := uint32(os.Getpid())
	for i := 0; i < vendors.CompactThreshold+8; i++ {
		if err := vendors.RecordContinuity(ws, "claude", pid, "turn "+itoa(i)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	first, err := vendors.CompactContinuity(context.Background(), ws)
	if err != nil || first.Compacted == 0 {
		t.Fatalf("first compaction did nothing: %+v err=%v", first, err)
	}
	second, err := vendors.CompactContinuity(context.Background(), ws)
	if err != nil {
		t.Fatalf("second compaction: %v", err)
	}
	if second.Compacted != 0 {
		t.Fatalf("compacted an already-compacted record (%d entries) — summaries of summaries", second.Compacted)
	}
}

// The token budget used to apply to only ONE of the two background sources.
// Entries from Glider's own continuity record went through SelectContinuity
// and were bounded; entries the FRONT supplied went into the pack unbounded —
// and Claude Code sends its full conversation history on every call, so the
// unbounded path was the one most likely to be enormous. Render is now the
// single choke point.
func TestContextPack_BoundsFrontSuppliedHistoryToo(t *testing.T) {
	t.Cleanup(func() { vendors.SetBackgroundLimits(20000, 20, 4000) })
	vendors.SetBackgroundLimits(50, 5, 120) // 50 tokens, 5 entries, 120 chars

	var turns []string
	for i := 0; i < 40; i++ {
		turns = append(turns, "front supplied turn "+itoa(i)+" "+strings.Repeat("z", 500))
	}
	pack := vendors.ContextPack{FrontVendor: "claude", Task: "do the thing", RecentTurns: turns}
	out := pack.Render()

	// entry cap
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- front supplied") && len(line) > 200 {
			t.Fatalf("an entry was not capped: %d chars", len(line))
		}
	}
	// entry count
	n := strings.Count(out, "- front supplied")
	if n > 5 {
		t.Fatalf("entry count not bounded: %d entries survived a max of 5", n)
	}
	// the NEWEST turns are what survive
	if !strings.Contains(out, "turn 39") {
		t.Error("the newest front-supplied turn was dropped; trimming must take from the oldest end")
	}
}

// Raising the limits must actually raise what gets through, or the config
// field is decorative.
func TestSetBackgroundLimits_ActuallyChangesWhatIsDelivered(t *testing.T) {
	t.Cleanup(func() { vendors.SetBackgroundLimits(20000, 20, 4000) })

	var turns []string
	for i := 0; i < 30; i++ {
		turns = append(turns, "turn "+itoa(i)+" "+strings.Repeat("w", 300))
	}
	pack := vendors.ContextPack{FrontVendor: "claude", Task: "t", RecentTurns: turns}

	vendors.SetBackgroundLimits(50, 3, 100)
	small := strings.Count(pack.Render(), "- turn")

	vendors.SetBackgroundLimits(20000, 25, 4000)
	large := strings.Count(pack.Render(), "- turn")

	if large <= small {
		t.Fatalf("raising the budget delivered no more context: %d then %d", small, large)
	}
	if vendors.DefaultContextTurns != 25 {
		t.Errorf("DefaultContextTurns did not follow max_entries: got %d, want 25", vendors.DefaultContextTurns)
	}
}
