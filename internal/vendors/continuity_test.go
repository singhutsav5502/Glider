package vendors

import (
	"os"
	"strings"
	"testing"
)

// withTempHome points ContinuityPath at a throwaway HOME so tests never
// touch the real ~/.glider.
func withTempHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func TestRecordAndReadContinuity_ReturnsPriorTurnsOldestFirst(t *testing.T) {
	withTempHome(t)
	const ws = "D:/repo"

	for _, turn := range []string{"refactor the auth module", "keep the token shape", "now add a refresh path"} {
		if err := RecordContinuity(ws, "claude", 100, turn); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	got := ReadContinuity(ws, "claude", 100, 6)
	// The newest turn is the delegate's own task and must be excluded.
	want := []string{"refactor the auth module", "keep the token shape"}
	if len(got) != len(want) {
		t.Fatalf("got %d turns %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("turn %d: got %q, want %q (order must be oldest-first)", i, got[i], want[i])
		}
	}
}

// TestReadContinuity_IsolatesOrigins is the core guarantee: two CLIs open in
// one workspace are two unrelated conversations. Handing a delegate the
// other session's turns would be worse than handing it nothing.
func TestReadContinuity_IsolatesOrigins(t *testing.T) {
	withTempHome(t)
	const ws = "D:/repo"

	_ = RecordContinuity(ws, "claude", 100, "claude turn one")
	_ = RecordContinuity(ws, "cursor-agent", 200, "cursor turn one")
	_ = RecordContinuity(ws, "claude", 100, "claude turn two")
	_ = RecordContinuity(ws, "cursor-agent", 200, "cursor turn two")
	_ = RecordContinuity(ws, "claude", 100, "claude current task")

	got := ReadContinuity(ws, "claude", 100, 6)
	joined := strings.Join(got, "|")
	if strings.Contains(joined, "cursor") {
		t.Fatalf("claude's history leaked another origin's turns: %q", got)
	}
	if len(got) != 2 || got[0] != "claude turn one" || got[1] != "claude turn two" {
		t.Fatalf("got %q, want claude's own two prior turns", got)
	}
}

func TestReadContinuity_NoBackgroundYet(t *testing.T) {
	withTempHome(t)
	const ws = "D:/repo"

	// A single turn is the current task — there is no background to give.
	_ = RecordContinuity(ws, "claude", 100, "the only turn")
	if got := ReadContinuity(ws, "claude", 100, 6); len(got) != 0 {
		t.Fatalf("expected no background from a single turn, got %q", got)
	}

	// An origin with no record at all must return nothing, not someone else's.
	_ = RecordContinuity(ws, "claude", 100, "another claude turn")
	if got := ReadContinuity(ws, "agy", 999, 6); len(got) != 0 {
		t.Fatalf("unknown origin must get nothing, got %q", got)
	}
}

func TestRecordContinuity_SuppressesRepeatedTurn(t *testing.T) {
	withTempHome(t)
	const ws = "D:/repo"

	// A front CLI can re-send the same turn across retries — cursor-agent
	// reconnects several times per call under some conditions.
	_ = RecordContinuity(ws, "cursor-agent", 200, "first real turn")
	for i := 0; i < 5; i++ {
		_ = RecordContinuity(ws, "cursor-agent", 200, "a retried turn")
	}
	_ = RecordContinuity(ws, "cursor-agent", 200, "current task")

	got := ReadContinuity(ws, "cursor-agent", 200, 10)
	if len(got) != 2 {
		t.Fatalf("retries should collapse to one entry, got %d: %q", len(got), got)
	}
}

func TestRecordContinuity_BoundsTheRing(t *testing.T) {
	withTempHome(t)
	const ws = "D:/repo"

	for i := 0; i < continuityMaxEntries+25; i++ {
		if err := RecordContinuity(ws, "claude", 100, "turn "+string(rune('a'+i%26))+strings.Repeat("x", i%7)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	path, err := ContinuityPath(ws)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := readContinuityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > continuityMaxEntries {
		t.Fatalf("ring not bounded: %d entries, max %d", len(entries), continuityMaxEntries)
	}
}

func TestRecordContinuity_TruncatesHugeTurn(t *testing.T) {
	withTempHome(t)
	const ws = "D:/repo"

	huge := strings.Repeat("y", continuityMaxTextLen*3)
	_ = RecordContinuity(ws, "claude", 100, huge)
	_ = RecordContinuity(ws, "claude", 100, "current task")

	got := ReadContinuity(ws, "claude", 100, 6)
	if len(got) != 1 {
		t.Fatalf("got %d turns, want 1", len(got))
	}
	if len([]rune(got[0])) > continuityMaxTextLen+1 {
		t.Fatalf("entry not truncated: %d runes", len([]rune(got[0])))
	}
}

func TestRecordContinuity_IgnoresEmptyInputs(t *testing.T) {
	withTempHome(t)
	if err := RecordContinuity("", "claude", 1, "text"); err != nil {
		t.Fatalf("empty workspace should be a quiet no-op: %v", err)
	}
	if err := RecordContinuity("D:/repo", "claude", 1, "   "); err != nil {
		t.Fatalf("blank text should be a quiet no-op: %v", err)
	}
	path, _ := ContinuityPath("D:/repo")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("nothing should have been written for empty inputs")
	}
}

// TestContinuityFileIsReadableMarkdown keeps the on-disk format honest: a
// user should be able to open this file and understand what Glider recorded
// about them, and delete it.
func TestContinuityFileIsReadableMarkdown(t *testing.T) {
	withTempHome(t)
	const ws = "D:/repo"
	_ = RecordContinuity(ws, "claude", 100, "refactor the auth module")

	path, _ := ContinuityPath(ws)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"# Glider session continuity", ws, "Safe to delete", "refactor the auth module"} {
		if !strings.Contains(body, want) {
			t.Fatalf("continuity file missing %q:\n%s", want, body)
		}
	}
}

// TestRecordContinuity_SkipsUnidentifiedOrigin pins a real leak found by
// dogfooding 2026-07-31: a plain `curl` against the gateway was recorded as
// "(#56632)". A turn Glider cannot attribute to a CLI session can never be
// correctly read back, so it is pure noise in a file meant to orient a
// delegate.
func TestRecordContinuity_SkipsUnidentifiedOrigin(t *testing.T) {
	withTempHome(t)
	const ws = "D:/repo"

	if err := RecordContinuity(ws, "", 56632, "a turn from some unidentified process"); err != nil {
		t.Fatalf("should be a quiet no-op: %v", err)
	}
	path, _ := ContinuityPath(ws)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an unidentified origin must not be recorded at all")
	}
}
