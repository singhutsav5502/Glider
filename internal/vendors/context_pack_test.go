package vendors

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func samplePack() ContextPack {
	return ContextPack{
		Task:        "add a refresh path",
		FrontVendor: "claude",
		Workspace:   "D:/repo",
		RecentTurns: []string{"refactor the auth module", "keep the existing token shape"},
	}
}

func TestInstallContextPack_CreatesThenRemovesWhenFileDidNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	revert, err := InstallContextPack(dir, "CLAUDE.md", samplePack())
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected the context file to exist during the run: %v", err)
	}
	if !strings.Contains(string(got), "add a refresh path") {
		t.Fatalf("task missing from written pack:\n%s", got)
	}
	if !strings.Contains(string(got), "keep the existing token shape") {
		t.Fatalf("prior turns missing from written pack:\n%s", got)
	}

	if err := revert(); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a file Glider created must be removed on revert, not left behind")
	}
}

// TestInstallContextPack_PreservesExistingFileByteForByte is the important
// one: CLAUDE.md / AGENTS.md is a file the USER owns and fills with their own
// instructions. Clobbering it would both misdirect the delegate and destroy
// real work.
func TestInstallContextPack_PreservesExistingFileByteForByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	original := "# House rules\n\nAlways run `go test ./...` before claiming done.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	revert, err := InstallContextPack(dir, "AGENTS.md", samplePack())
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	during, _ := os.ReadFile(path)
	if !strings.Contains(string(during), "Always run `go test ./...`") {
		t.Fatalf("the user's own instructions must survive alongside the pack:\n%s", during)
	}
	if !strings.Contains(string(during), "add a refresh path") {
		t.Fatalf("pack not appended:\n%s", during)
	}

	if err := revert(); err != nil {
		t.Fatalf("revert: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the user's file must still exist after revert: %v", err)
	}
	if string(after) != original {
		t.Fatalf("file not restored byte-for-byte.\nwant: %q\ngot:  %q", original, after)
	}
}

// TestInstallContextPack_SelfHealsStaleBlock covers the crash path: if
// glider.exe is force-killed mid-run, no revert executes and a block is left
// in the user's file. The next delegate to that file must clean it up rather
// than let blocks accumulate forever.
func TestInstallContextPack_SelfHealsStaleBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	userContent := "# House rules\n"
	stale := userContent + "\n" + ContextPack{Task: "an abandoned earlier run"}.Render()
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	revert, err := InstallContextPack(dir, "CLAUDE.md", samplePack())
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	during, _ := os.ReadFile(path)
	if strings.Contains(string(during), "an abandoned earlier run") {
		t.Fatalf("stale block from a crashed run should have been stripped:\n%s", during)
	}
	if strings.Count(string(during), contextPackBegin) != 1 {
		t.Fatalf("expected exactly one Glider block, got %d:\n%s",
			strings.Count(string(during), contextPackBegin), during)
	}

	if err := revert(); err != nil {
		t.Fatalf("revert: %v", err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), contextPackBegin) {
		t.Fatalf("revert must leave no Glider block at all:\n%s", after)
	}
	if !strings.Contains(string(after), "# House rules") {
		t.Fatalf("user content lost while cleaning a stale block:\n%s", after)
	}
}

func TestStripContextPack_ToleratesTruncatedBlock(t *testing.T) {
	// A crash mid-write can leave a BEGIN marker with no END.
	content := []byte("# Rules\n\n" + contextPackBegin + "\n\n## Delegated task context\n\ncut off here")
	got := string(stripContextPack(content))
	if strings.Contains(got, contextPackBegin) {
		t.Fatalf("truncated block not stripped: %q", got)
	}
	if !strings.Contains(got, "# Rules") {
		t.Fatalf("user content lost: %q", got)
	}
}

func TestInstallContextPack_NoopWhenNothingToInstall(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		cwd, file string
		pack      ContextPack
	}{
		"no context file configured": {dir, "", samplePack()},
		"no workspace resolved":      {"", "CLAUDE.md", samplePack()},
		"empty pack":                 {dir, "CLAUDE.md", ContextPack{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			revert, err := InstallContextPack(c.cwd, c.file, c.pack)
			if err != nil {
				t.Fatalf("expected a quiet no-op, got: %v", err)
			}
			if err := revert(); err != nil {
				t.Fatalf("no-op revert should never error: %v", err)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("nothing should have been written, found %d entries", len(entries))
			}
		})
	}
}

// TestInstallContextPack_SerializesSameFile pins the concurrency contract.
// Two delegates to the same vendor in the same workspace target the same
// file and each holds it for a whole subprocess lifetime; without
// serialization the second write lands on the first, and the first's revert
// then wipes the second's context.
func TestInstallContextPack_SerializesSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	var mu sync.Mutex
	overlaps := 0
	inside := 0

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			revert, err := InstallContextPack(dir, "CLAUDE.md", samplePack())
			if err != nil {
				t.Errorf("install: %v", err)
				return
			}

			mu.Lock()
			inside++
			if inside > 1 {
				overlaps++
			}
			mu.Unlock()

			// While held, exactly one Glider block must be present.
			if b, err := os.ReadFile(path); err == nil {
				if n := strings.Count(string(b), contextPackBegin); n != 1 {
					t.Errorf("saw %d concurrent blocks in the file, want 1", n)
				}
			}

			mu.Lock()
			inside--
			mu.Unlock()

			if err := revert(); err != nil {
				t.Errorf("revert: %v", err)
			}
		}()
	}
	wg.Wait()

	if overlaps != 0 {
		t.Fatalf("%d overlapping installs on one path — writes are not serialized", overlaps)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("after every revert the created file should be gone")
	}
}

// TestInstallContextPack_DegradesRatherThanBlockingForever pins the
// liveness property. The lock is necessarily held for a delegate's whole
// subprocess lifetime, and RunTimeout no longer bounds that lifetime — so
// without a bounded acquisition, one wedged delegate would block every
// later delegate to the same vendor and workspace forever.
func TestInstallContextPack_DegradesRatherThanBlockingForever(t *testing.T) {
	orig := ContextPackLockWait
	ContextPackLockWait = 120 * time.Millisecond
	defer func() { ContextPackLockWait = orig }()

	dir := t.TempDir()

	// Hold the file as a "wedged" delegate would: acquired and never released.
	held, err := InstallContextPack(dir, "CLAUDE.md", samplePack())
	if err != nil {
		t.Fatalf("first install: %v", err)
	}

	start := time.Now()
	revert, err := InstallContextPack(dir, "CLAUDE.md", ContextPack{Task: "a second, contending delegate"})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("contention must degrade quietly, not error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waited %v — acquisition is not bounded", elapsed)
	}
	if err := revert(); err != nil {
		t.Fatalf("the degraded revert must be a safe no-op: %v", err)
	}

	// The holder's own block must be untouched by the contender.
	body, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if strings.Contains(string(body), "a second, contending delegate") {
		t.Fatal("a contender that gave up must not have written anything")
	}
	if !strings.Contains(string(body), "add a refresh path") {
		t.Fatal("the holder's block was damaged by a contender")
	}

	if err := held(); err != nil {
		t.Fatalf("holder revert: %v", err)
	}
}

// TestInstallContextPack_LockIsReleasedForTheNextDelegate confirms the
// bounded acquisition doesn't leak the semaphore on the normal path.
func TestInstallContextPack_LockIsReleasedForTheNextDelegate(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		revert, err := InstallContextPack(dir, "CLAUDE.md", samplePack())
		if err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
		if err := revert(); err != nil {
			t.Fatalf("revert %d: %v", i, err)
		}
	}
}
