package loop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorkerIsolation is one parallel worker's scratch root under the run work/ tree.
type WorkerIsolation struct {
	Index       int
	RelWork     string // workspace-relative, e.g. runs/<id>/work/w0
	AbsWork     string
	GitWorktree bool
	Note        string
}

// isolateParallelWorkers creates per-worker dirs under runs/<hoop>/work/wN.
// When worktrees is enabled and gitRoot is a git repo, attempts `git worktree add`
// into each dir (falls back to plain mkdir on failure). No-op when n <= 1.
func isolateParallelWorkers(workspace, runID string, n int, worktrees bool, gitRoot string) ([]WorkerIsolation, error) {
	if n <= 1 || !worktrees {
		return nil, nil
	}
	if n > 4 {
		n = 4
	}
	lay := runWorkLayout(workspace, runID)
	if err := os.MkdirAll(lay.workAbs, 0o755); err != nil {
		return nil, err
	}
	useGit := worktrees && gitRoot != "" && isGitRepo(gitRoot)
	out := make([]WorkerIsolation, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("w%d", i)
		abs := filepath.Join(lay.workAbs, name)
		rel := filepath.ToSlash(filepath.Join(lay.relWork, name))
		iso := WorkerIsolation{Index: i, RelWork: rel, AbsWork: abs}
		if useGit {
			_ = os.RemoveAll(abs) // git worktree add needs a missing path
			if err := gitWorktreeAdd(gitRoot, abs); err != nil {
				if mkErr := os.MkdirAll(abs, 0o755); mkErr != nil {
					return out[:i], mkErr
				}
				iso.Note = "git worktree failed; using isolated subdir: " + err.Error()
			} else {
				iso.GitWorktree = true
				iso.Note = "git worktree"
			}
		} else {
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return out[:i], err
			}
			if worktrees && gitRoot != "" && !isGitRepo(gitRoot) {
				iso.Note = "worktrees enabled but not a git repo; isolated subdir"
			} else {
				iso.Note = "isolated subdir"
			}
		}
		out[i] = iso
	}
	return out, nil
}

type runWorkPaths struct {
	workAbs string
	relWork string
}

func runWorkLayout(workspace, runID string) runWorkPaths {
	root := strings.TrimSpace(workspace)
	if root == "" {
		root = "."
	}
	id := sanitizeRunIDLocal(runID)
	return runWorkPaths{
		workAbs: filepath.Join(root, "runs", id, "work"),
		relWork: filepath.ToSlash(filepath.Join("runs", id, "work")),
	}
}

func sanitizeRunIDLocal(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "run"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func isGitRepo(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular()) // .git file for worktrees
}

func gitWorktreeAdd(repo, path string) error {
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "--detach", path, "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// isolationPromptHint tells a parallel worker where to write.
func isolationPromptHint(iso WorkerIsolation) string {
	if iso.RelWork == "" {
		return ""
	}
	mode := "isolated subdirectory"
	if iso.GitWorktree {
		mode = "git worktree"
	}
	return fmt.Sprintf(
		"WORKER_ROOT (%s): write/read only under %s/ (workspace-relative). Do not modify sibling worker dirs.",
		mode, iso.RelWork,
	)
}
