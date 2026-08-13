package tools

import (
	"context"
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
	Mode    string // run | existing
	RootAbs string // workspace absolute (JSON: workspace_root)
	WorkAbs string
	OutAbs  string
	RelRoot string // runs/<id> (empty when mode=existing)
	RelWork string // runs/<id>/work or existing work rel
	RelOut  string // runs/<id>/out or existing out rel
}

type layoutCtxKey struct{}

// WithRunLayout attaches a layout to ctx for concurrent-safe path resolution.
func WithRunLayout(ctx context.Context, layout RunLayout) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, layoutCtxKey{}, layout)
}

// RunLayoutFrom returns the layout bound on ctx, if any.
func RunLayoutFrom(ctx context.Context) (RunLayout, bool) {
	if ctx == nil {
		return RunLayout{}, false
	}
	l, ok := ctx.Value(layoutCtxKey{}).(RunLayout)
	return l, ok
}

// WorkspaceRoot is an alias used by status APIs.
func (l RunLayout) WorkspaceRoot() string { return l.RootAbs }

// WorkDir / OutDir absolute aliases for status APIs.
func (l RunLayout) WorkDir() string { return l.WorkAbs }
func (l RunLayout) OutDir() string  { return l.OutAbs }

// LayoutForRun builds paths (does not create dirs).
func LayoutForRun(workspace, runID string) RunLayout {
	root := strings.TrimSpace(workspace)
	if root == "" {
		root = DefaultWorkspaceDir()
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	id := SanitizeRunID(runID)
	relRoot := filepath.ToSlash(filepath.Join("runs", id))
	return RunLayout{
		RunID:   id,
		Mode:    "run",
		RootAbs: root,
		WorkAbs: filepath.Join(root, "runs", id, "work"),
		OutAbs:  filepath.Join(root, "runs", id, "out"),
		RelRoot: relRoot,
		RelWork: relRoot + "/work",
		RelOut:  relRoot + "/out",
	}
}

// LayoutExisting binds an existing path under the tools sandbox as work,
// with out at outPath (or <work>/out when outPath empty).
func LayoutExisting(workspaceRoot, runID, workPath, outPath string) (RunLayout, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = DefaultWorkspaceDir()
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return RunLayout{}, err
	}
	workAbs, workRel, err := resolveUnderRoot(absRoot, workPath)
	if err != nil {
		return RunLayout{}, fmt.Errorf("workspace_path: %w", err)
	}
	var outAbs, outRel string
	if strings.TrimSpace(outPath) == "" {
		outAbs = filepath.Join(workAbs, "out")
		outRel, err = filepath.Rel(absRoot, outAbs)
		if err != nil || strings.HasPrefix(outRel, "..") {
			return RunLayout{}, fmt.Errorf("out_path escapes workspace")
		}
		outRel = filepath.ToSlash(outRel)
	} else {
		outAbs, outRel, err = resolveUnderRoot(absRoot, outPath)
		if err != nil {
			return RunLayout{}, fmt.Errorf("out_path: %w", err)
		}
	}
	return RunLayout{
		RunID:   SanitizeRunID(runID),
		Mode:    "existing",
		RootAbs: absRoot,
		WorkAbs: workAbs,
		OutAbs:  outAbs,
		RelWork: workRel,
		RelOut:  outRel,
	}, nil
}

func resolveUnderRoot(absRoot, path string) (abs, rel string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", fmt.Errorf("path required")
	}
	var candidate string
	if filepath.IsAbs(path) {
		candidate, err = filepath.Abs(path)
		if err != nil {
			return "", "", err
		}
	} else {
		cleanRel := filepath.Clean(path)
		if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("path escapes workspace")
		}
		candidate = filepath.Join(absRoot, cleanRel)
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			return "", "", err
		}
	}
	rel, err = filepath.Rel(absRoot, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path escapes workspace")
	}
	return candidate, filepath.ToSlash(rel), nil
}

// ScopeRel maps a workspace-relative path into the active run's work/ folder when a
// run id / layout is set — matching git_clone. Bare "audit-target" becomes
// runs/<id>/work/audit-target (or existing RelWork). Paths already under runs/
// or the bound RelWork/RelOut are unchanged.
func (r *Registry) ScopeRel(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		rel = "."
	}
	rel = filepath.ToSlash(rel)
	if r == nil {
		return rel
	}
	lay, ok := r.CurrentLayout()
	if !ok {
		id := r.RunID()
		if id == "" {
			return rel
		}
		lay = LayoutForRun(r.Workspace(), id)
	}
	if lay.RelWork == "" && lay.RelOut == "" {
		return rel
	}
	if rel == "runs" || strings.HasPrefix(rel, "runs/") {
		return rel
	}
	if lay.RelWork != "" && (rel == lay.RelWork || strings.HasPrefix(rel, lay.RelWork+"/")) {
		return rel
	}
	if lay.RelOut != "" && (rel == lay.RelOut || strings.HasPrefix(rel, lay.RelOut+"/")) {
		return rel
	}
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
	mode := l.Mode
	if mode == "" {
		mode = "run"
	}
	return fmt.Sprintf(
		"ARTIFACTS (workspace-relative, mode=%s):\n"+
			"- Intermediate / clones / scratch → %s/\n"+
			"- Final deliverables (reports, packs) → %s/\n"+
			"Bare paths like audit-target/ resolve under %s/ (same as git_clone).\n"+
			"Use tools fs_write or artifact_write. Prefer artifact_write kind=work|out.\n"+
			"Cite relative paths in your summary.",
		mode, l.RelWork, l.RelOut, l.RelWork,
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
