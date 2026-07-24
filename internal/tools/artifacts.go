package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var safeRunID = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// SanitizeRunID makes a filesystem-safe run folder name.
func SanitizeRunID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "run"
	}
	id = safeRunID.ReplaceAllString(id, "_")
	if len(id) > 80 {
		id = id[:80]
	}
	return id
}

// RunLayout is the per-hoop / per-swarm scratch + output tree under the tools workspace.
type RunLayout struct {
	RunID   string // sanitized id
	RootAbs string // workspace absolute
	WorkAbs string
	OutAbs  string
	RelRoot string // runs/<id>
	RelWork string // runs/<id>/work
	RelOut  string // runs/<id>/out
}

// LayoutForRun builds paths (does not create dirs).
func LayoutForRun(workspace, runID string) RunLayout {
	root := strings.TrimSpace(workspace)
	if root == "" {
		root = DefaultWorkspaceDir()
	}
	id := SanitizeRunID(runID)
	relRoot := filepath.ToSlash(filepath.Join("runs", id))
	return RunLayout{
		RunID:   id,
		RootAbs: root,
		WorkAbs: filepath.Join(root, "runs", id, "work"),
		OutAbs:  filepath.Join(root, "runs", id, "out"),
		RelRoot: relRoot,
		RelWork: relRoot + "/work",
		RelOut:  relRoot + "/out",
	}
}

// ScopeRel maps a workspace-relative path into the active run's work/ folder when a
// run id is set — matching git_clone. Bare "audit-target" becomes
// runs/<id>/work/audit-target. Paths already under runs/ are unchanged.
// Without a run id, rel is returned as-is (trimmed; empty → ".").
func (r *Registry) ScopeRel(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		rel = "."
	}
	rel = filepath.ToSlash(rel)
	if r == nil {
		return rel
	}
	id := r.RunID()
	if id == "" {
		return rel
	}
	if rel == "runs" || strings.HasPrefix(rel, "runs/") {
		return rel
	}
	lay := LayoutForRun(r.Workspace(), id)
	if rel == "." {
		return lay.RelWork
	}
	return filepath.ToSlash(filepath.Join(lay.RelWork, filepath.FromSlash(rel)))
}

// Ensure creates work/ and out/ under the workspace.
func (l RunLayout) Ensure() error {
	if err := os.MkdirAll(l.WorkAbs, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(l.OutAbs, 0o755)
}

// PromptHint is injected into stage/swarm prompts so models know where to write.
func (l RunLayout) PromptHint() string {
	return fmt.Sprintf(
		"ARTIFACTS (workspace-relative):\n"+
			"- Intermediate / clones / scratch → %s/\n"+
			"- Final deliverables (reports, packs) → %s/\n"+
			"Bare paths like audit-target/ resolve under %s/ (same as git_clone).\n"+
			"Use tools fs_write or artifact_write. Prefer artifact_write kind=work|out.\n"+
			"Cite relative paths in your summary.",
		l.RelWork, l.RelOut, l.RelWork,
	)
}

// RelFromAbs returns a workspace-relative slash path, or empty if outside.
func RelFromAbs(workspace, absPath string) string {
	workspace = strings.TrimSpace(workspace)
	absPath = strings.TrimSpace(absPath)
	if workspace == "" || absPath == "" {
		return ""
	}
	rel, err := filepath.Rel(workspace, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

// ListDir lists files under a workspace-relative path (non-recursive, capped).
func ListDir(workspace, rel string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	abs, err := safeJoin(workspace, rel)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		out = append(out, name)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// WalkFiles lists relative file paths under rel (recursive, capped).
func WalkFiles(workspace, rel string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 200
	}
	abs, err := safeJoin(workspace, rel)
	if err != nil {
		return nil, err
	}
	var out []string
	_ = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		r, err := filepath.Rel(workspace, path)
		if err != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(r))
		if len(out) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	return out, nil
}

// ReadFileCapped reads a workspace-relative text file, capped at maxBytes.
func ReadFileCapped(workspace, rel string, maxBytes int) (content string, truncated bool, size int64, err error) {
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	abs, err := safeJoin(workspace, rel)
	if err != nil {
		return "", false, 0, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", false, 0, err
	}
	if st.IsDir() {
		return "", false, st.Size(), fmt.Errorf("path is a directory")
	}
	size = st.Size()
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", false, size, err
	}
	if len(b) > maxBytes {
		return string(b[:maxBytes]), true, size, nil
	}
	return string(b), false, size, nil
}

// DiffFiles returns a unified-ish diff of two workspace-relative files (via git --no-index when available).
func DiffFiles(workspace, a, b string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	absA, err := safeJoin(workspace, a)
	if err != nil {
		return "", err
	}
	absB, err := safeJoin(workspace, b)
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "diff", "--no-index", "--", absA, absB)
	out, _ := cmd.CombinedOutput() // exit 1 when files differ
	text := string(out)
	if text == "" {
		ca, _, _, errA := ReadFileCapped(workspace, a, maxBytes)
		cb, _, _, errB := ReadFileCapped(workspace, b, maxBytes)
		if errA != nil {
			return "", errA
		}
		if errB != nil {
			return "", errB
		}
		if ca == cb {
			return "(no differences)", nil
		}
		text = simpleLineDiff(a, b, ca, cb)
	}
	if len(text) > maxBytes {
		text = text[:maxBytes] + "\n...truncated"
	}
	return text, nil
}

// GitDiffAt runs `git diff` in the nearest .git repo at or under path (must stay in workspace).
func GitDiffAt(workspace, rel string, maxBytes int) (diff string, repoRel string, err error) {
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	abs, err := safeJoin(workspace, rel)
	if err != nil {
		return "", "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	start := abs
	if !st.IsDir() {
		start = filepath.Dir(abs)
	}
	repo := findGitRoot(workspace, start)
	if repo == "" {
		return "", "", fmt.Errorf("no .git under %s", filepath.ToSlash(rel))
	}
	cmd := exec.Command("git", "diff")
	cmd.Dir = repo
	out, runErr := cmd.CombinedOutput()
	text := string(out)
	if len(text) > maxBytes {
		text = text[:maxBytes] + "\n...truncated"
	}
	repoRel = RelFromAbs(workspace, repo)
	if runErr != nil && text == "" {
		return "", repoRel, runErr
	}
	if text == "" {
		text = "(clean working tree)"
	}
	return text, repoRel, nil
}

func findGitRoot(workspace, startAbs string) string {
	absRoot, err := filepath.Abs(workspace)
	if err != nil {
		return ""
	}
	cur := startAbs
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		rel, err := filepath.Rel(absRoot, parent)
		if err != nil || strings.HasPrefix(rel, "..") {
			return ""
		}
		cur = parent
	}
}

func simpleLineDiff(nameA, nameB, a, b string) string {
	la := strings.Split(a, "\n")
	lb := strings.Split(b, "\n")
	var out strings.Builder
	out.WriteString("--- " + nameA + "\n+++ " + nameB + "\n")
	max := len(la)
	if len(lb) > max {
		max = len(lb)
	}
	for i := 0; i < max; i++ {
		var sa, sb string
		if i < len(la) {
			sa = la[i]
		}
		if i < len(lb) {
			sb = lb[i]
		}
		if sa == sb {
			continue
		}
		if i < len(la) {
			out.WriteString("-" + sa + "\n")
		}
		if i < len(lb) {
			out.WriteString("+" + sb + "\n")
		}
	}
	return out.String()
}
