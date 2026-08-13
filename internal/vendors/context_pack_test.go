package vendors

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func samplePack() ContextPack {
	return ContextPack{
		Task:        "add a refresh path",
		FrontVendor: "claude",
		Workspace:   "D:/repo",
		RecentTurns: []string{"refactor the auth module", "keep the existing token shape"},
	}
}

func TestPrepareContextDir_WritesPackThenCleansUp(t *testing.T) {
	withTempHome(t)

	dir, file, cleanup, err := PrepareContextDir("AGENTS.md", samplePack())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if dir == "" || file == "" {
		t.Fatal("expected a real dir and file")
	}
	if filepath.Base(file) != "AGENTS.md" {
		t.Fatalf("context file must use the vendor's expected name, got %q", filepath.Base(file))
	}

	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("context file unreadable: %v", err)
	}
	for _, want := range []string{"add a refresh path", "keep the existing token shape", "You are already the delegate"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("context missing %q:\n%s", want, body)
		}
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the per-delegate directory must be removed on cleanup")
	}
}

// TestPrepareContextDir_IsolatesConcurrentDelegates is the property the whole
// per-delegate design exists for. The previous shared-file design needed a
// lock held for each delegate's entire subprocess lifetime, plus byte-exact
// restore, stale-block healing, and bounded acquisition to avoid deadlock.
// With a private directory each, concurrent delegates simply cannot interact.
func TestPrepareContextDir_IsolatesConcurrentDelegates(t *testing.T) {
	withTempHome(t)

	const n = 8
	dirs := make([]string, n)
	cleanups := make([]func() error, n)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pack := samplePack()
			pack.Task = "task number " + string(rune('a'+i))
			dir, file, cleanup, err := PrepareContextDir("AGENTS.md", pack)
			if err != nil {
				t.Errorf("prepare %d: %v", i, err)
				return
			}
			body, err := os.ReadFile(file)
			if err != nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
			// Each delegate must see ONLY its own task — the exact failure
			// a shared file would produce.
			if !strings.Contains(string(body), pack.Task) {
				t.Errorf("delegate %d lost its own task", i)
			}
			for j := 0; j < n; j++ {
				if j == i {
					continue
				}
				if strings.Contains(string(body), "task number "+string(rune('a'+j))) {
					t.Errorf("delegate %d saw delegate %d's task — directories are not isolated", i, j)
				}
			}
			mu.Lock()
			dirs[i], cleanups[i] = dir, cleanup
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, d := range dirs {
		if d == "" {
			t.Fatalf("delegate %d produced no directory", i)
		}
		if seen[d] {
			t.Fatalf("two delegates shared directory %q", d)
		}
		seen[d] = true
	}
	for _, c := range cleanups {
		if c != nil {
			_ = c()
		}
	}
}

func TestPrepareContextDir_NoopWhenNothingToWrite(t *testing.T) {
	withTempHome(t)
	cases := map[string]struct {
		contextFile string
		pack        ContextPack
	}{
		"vendor declares no context file": {"", samplePack()},
		"empty pack":                      {"AGENTS.md", ContextPack{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir, file, cleanup, err := PrepareContextDir(c.contextFile, c.pack)
			if err != nil {
				t.Fatalf("expected a quiet no-op: %v", err)
			}
			if dir != "" || file != "" {
				t.Fatalf("expected empty paths, got dir=%q file=%q", dir, file)
			}
			if err := cleanup(); err != nil {
				t.Fatalf("no-op cleanup must never error: %v", err)
			}
		})
	}
}

// TestSubstituteTemplateArgs_DropsContextArgsWhenAbsent pins the graceful
// degradation: substituting empty would leave "--add-dir=" or an
// --append-system-prompt-file pointing nowhere, which is a hard CLI argument
// error rather than simply running without context.
func TestSubstituteTemplateArgs_DropsContextArgsWhenAbsent(t *testing.T) {
	tmpl := []string{"-p", "--add-dir={{context_dir}}", "--append-system-prompt-file={{context_file}}", "{{prompt}}"}

	got := substituteTemplateArgs(tmpl, "do the thing", "", "D:/repo", "", "")
	for _, a := range got {
		if strings.Contains(a, "context_dir") || strings.Contains(a, "context_file") ||
			a == "--add-dir=" || a == "--append-system-prompt-file=" {
			t.Fatalf("dangling context arg survived: %q (all args: %v)", a, got)
		}
	}
	if len(got) != 2 || got[0] != "-p" || got[1] != "do the thing" {
		t.Fatalf("got %v, want just the non-context args", got)
	}

	withCtx := substituteTemplateArgs(tmpl, "do the thing", "", "D:/repo", "C:/ctx", "C:/ctx/AGENTS.md")
	if len(withCtx) != 4 {
		t.Fatalf("got %v, want all four args when context exists", withCtx)
	}
	if withCtx[1] != "--add-dir=C:/ctx" || withCtx[2] != "--append-system-prompt-file=C:/ctx/AGENTS.md" {
		t.Fatalf("context paths not substituted: %v", withCtx)
	}
}

func TestSweepDelegateContextDirs_RemovesLeftovers(t *testing.T) {
	withTempHome(t)

	// A directory a force-killed run never cleaned up.
	_, _, _, err := PrepareContextDir("AGENTS.md", samplePack())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	root, _ := DelegateContextRoot()
	if entries, _ := os.ReadDir(root); len(entries) == 0 {
		t.Fatal("expected a leftover directory to exist before the sweep")
	}

	if err := SweepDelegateContextDirs(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("sweep left %d directories behind", len(entries))
	}
}

func TestSweepDelegateContextDirs_NoRootIsNotAnError(t *testing.T) {
	withTempHome(t)
	if err := SweepDelegateContextDirs(); err != nil {
		t.Fatalf("a missing root is normal on first run, got: %v", err)
	}
}
