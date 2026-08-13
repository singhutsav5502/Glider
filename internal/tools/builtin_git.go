package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type gitStatus struct{ root string }

func (t *gitStatus) Name() string        { return "git_status" }
func (t *gitStatus) Description() string { return "git status --short in workspace" }
func (t *gitStatus) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *gitStatus) Call(ctx context.Context, _ string, _ json.RawMessage) (Result, error) {
	return runGit(ctx, t.Name(), t.root, "status", "--short")
}

type gitDiff struct{ root string }

func (t *gitDiff) Name() string        { return "git_diff" }
func (t *gitDiff) Description() string { return "git diff (optionally --stat)" }
func (t *gitDiff) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"stat":{"type":"boolean"}}}`)
}
func (t *gitDiff) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	args := []string{"diff"}
	if strings.Contains(input, "stat") {
		args = append(args, "--stat")
	}
	return runGit(ctx, t.Name(), t.root, args...)
}

type gitLog struct{ root string }

func (t *gitLog) Name() string        { return "git_log" }
func (t *gitLog) Description() string { return "git log -n 20 --oneline" }
func (t *gitLog) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`)
}
func (t *gitLog) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	n := "20"
	if v := strings.TrimSpace(input); v != "" {
		if _, err := strconv.Atoi(v); err == nil {
			n = v
		}
	}
	return runGit(ctx, t.Name(), t.root, "log", "-n", n, "--oneline")
}

type gitClone struct {
	root string
	reg  *Registry
}

func (t *gitClone) Name() string { return "git_clone" }
func (t *gitClone) Description() string {
	return "git clone URL into the tools workspace (default dir=repo). With an active hoop/swarm run, bare dirs land under runs/<id>/work/. Example: https://github.com/org/repo.git audit-target"
}
func (t *gitClone) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"dir":{"type":"string","description":"destination under workspace (alias: targetDir)"},"targetDir":{"type":"string","description":"alias for dir"}},"required":["url"]}`)
}
func (t *gitClone) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) == 0 {
		return fail(t.Name(), fmt.Errorf("url required"))
	}
	url := fields[0]
	dir := "repo"
	if len(fields) > 1 {
		dir = fields[1]
	}
	dir = filepath.ToSlash(strings.TrimSpace(dir))
	// Scope bare relative dirs into the active run's work/ folder.
	if t.reg != nil {
		if id := t.reg.RunID(); id != "" && dir != "" && !strings.HasPrefix(dir, "runs/") {
			lay := LayoutForRun(t.root, id)
			dir = filepath.ToSlash(filepath.Join(lay.RelWork, filepath.FromSlash(dir)))
		}
	}
	dest, err := safeJoin(t.root, dir)
	if err != nil {
		return fail(t.Name(), err)
	}
	c := exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dest)
	out, err := c.CombinedOutput()
	text := string(out)
	relDest := RelFromAbs(t.root, dest)
	if relDest == "" {
		relDest = dest
	}
	if err != nil {
		// If dir already exists from a prior clone, treat as refresh success when it is a git repo.
		if st, stErr := os.Stat(filepath.Join(dest, ".git")); stErr == nil && st.IsDir() {
			pull := exec.CommandContext(ctx, "git", "-C", dest, "pull", "--ff-only")
			pout, perr := pull.CombinedOutput()
			msg := text + "\n" + string(pout) + "\ncloned to " + relDest + " (existing; refreshed)"
			if perr != nil {
				return Result{Name: t.Name(), Kind: KindBuiltin, OK: true, Output: msg + "\n(pull note: " + perr.Error() + ")"}, nil
			}
			return ok(t.Name(), msg), nil
		}
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: false, Output: text, Err: err.Error()}, nil
	}
	return ok(t.Name(), text+"\ncloned to "+relDest), nil
}

func runGit(ctx context.Context, name, root string, args ...string) (Result, error) {
	c := exec.CommandContext(ctx, "git", args...)
	if root != "" {
		c.Dir = root
	}
	out, err := c.CombinedOutput()
	text := string(out)
	if len(text) > 32<<10 {
		text = text[:32<<10] + "\n...truncated"
	}
	if err != nil {
		return Result{Name: name, Kind: KindBuiltin, OK: false, Output: text, Err: err.Error()}, nil
	}
	return ok(name, text), nil
}
