package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ArtifactKind selects work (scratch) vs out (deliverable) scoping.
type ArtifactKind string

const (
	RelWork ArtifactKind = "work"
	RelOut  ArtifactKind = "out"
)

// RunLayout is the per-run work/out folder pair under the tools workspace sandbox.
//
// Default layout (mode=run):
//
//	<workspace>/runs/<run_id>/work   — scratch, clones, intermediates
//	<workspace>/runs/<run_id>/out    — final deliverables
//
// Existing mode binds WorkDir/OutDir to operator-chosen paths that must stay
// inside the same workspace root (safeJoin / path-escape rejection).
type RunLayout struct {
	WorkspaceRoot string `json:"workspace_root"`
	RunID         string `json:"run_id,omitempty"`
	Mode          string `json:"mode,omitempty"` // run|existing
	WorkDir       string `json:"work_dir"`       // absolute
	OutDir        string `json:"out_dir"`        // absolute
	WorkRel       string `json:"work_rel"`       // relative to workspace
	OutRel        string `json:"out_rel"`        // relative to workspace
}

type layoutCtxKey struct{}

// WithRunLayout attaches a layout to ctx so tool path resolution is run-scoped
// without mutating a shared registry (safe for concurrent hoops/swarms).
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

// DefaultWorkspaceDir returns ~/.glider/workspace (or ./.glider/workspace).
func DefaultWorkspaceDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider", "workspace")
	}
	return filepath.Join(home, ".glider", "workspace")
}

// LayoutForRun builds the default runs/<run_id>/{work,out} layout.
func LayoutForRun(workspaceRoot, runID string) RunLayout {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = DefaultWorkspaceDir()
	}
	runID = sanitizeRunID(runID)
	workRel := filepath.ToSlash(filepath.Join("runs", runID, "work"))
	outRel := filepath.ToSlash(filepath.Join("runs", runID, "out"))
	absRoot, _ := filepath.Abs(workspaceRoot)
	if absRoot == "" {
		absRoot = workspaceRoot
	}
	return RunLayout{
		WorkspaceRoot: absRoot,
		RunID:         runID,
		Mode:          "run",
		WorkDir:       filepath.Join(absRoot, filepath.FromSlash(workRel)),
		OutDir:        filepath.Join(absRoot, filepath.FromSlash(outRel)),
		WorkRel:       workRel,
		OutRel:        outRel,
	}
}

// LayoutExisting binds an existing workspace-relative or absolute path as work,
// with out at outPath (or <work>/out when outPath empty). Paths must stay under root.
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
		WorkspaceRoot: absRoot,
		RunID:         sanitizeRunID(runID),
		Mode:          "existing",
		WorkDir:       workAbs,
		OutDir:        outAbs,
		WorkRel:       workRel,
		OutRel:        outRel,
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
		// Reject relative escape attempts (do not clamp ".." into the sandbox).
		cleanRel := filepath.Clean(path)
		if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("path escapes workspace")
		}
		candidate, err = safeJoin(absRoot, cleanRel)
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

// Ensure creates work and out directories.
func (l RunLayout) Ensure() error {
	if strings.TrimSpace(l.WorkDir) == "" || strings.TrimSpace(l.OutDir) == "" {
		return fmt.Errorf("empty work/out dirs")
	}
	if err := os.MkdirAll(l.WorkDir, 0o755); err != nil {
		return err
	}
	return os.MkdirAll(l.OutDir, 0o755)
}

// PromptHint is a short note for stage prompts about where to write files.
func (l RunLayout) PromptHint() string {
	if l.WorkRel == "" && l.OutRel == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Workspace (run-scoped):\n")
	b.WriteString("- work (scratch/clones): ")
	b.WriteString(l.WorkRel)
	b.WriteString("\n- out (deliverables): ")
	b.WriteString(l.OutRel)
	b.WriteString("\nBare tool paths resolve under work; use artifact_write kind=out for deliverables.")
	return b.String()
}

// ScopeRel rewrites a tool-relative path into a workspace-relative path.
//
// Rules:
//   - empty / "." → work root
//   - already under WorkRel or OutRel → unchanged
//   - kind=out (or path prefixed with "out/") → under OutRel
//   - otherwise → under WorkRel (bare paths land in RelWork)
//
// When no layout fields are set, path is returned cleaned (sandbox root only).
func (l RunLayout) ScopeRel(path string, kind ArtifactKind) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		path = ""
	}
	path = filepath.ToSlash(filepath.Clean("/" + path))
	path = strings.TrimPrefix(path, "/")
	if path == "." {
		path = ""
	}

	workRel := strings.Trim(filepath.ToSlash(l.WorkRel), "/")
	outRel := strings.Trim(filepath.ToSlash(l.OutRel), "/")
	if workRel == "" && outRel == "" {
		if path == "" {
			return "."
		}
		return path
	}

	if workRel != "" && (path == workRel || strings.HasPrefix(path, workRel+"/")) {
		return path
	}
	if outRel != "" && (path == outRel || strings.HasPrefix(path, outRel+"/")) {
		return path
	}

	kind = ArtifactKind(strings.ToLower(strings.TrimSpace(string(kind))))
	useOut := kind == RelOut || kind == "output" || strings.HasPrefix(path, "out/")
	if useOut {
		rest := path
		if strings.HasPrefix(rest, "out/") {
			rest = strings.TrimPrefix(rest, "out/")
		}
		if outRel == "" {
			return path
		}
		if rest == "" {
			return outRel
		}
		return outRel + "/" + rest
	}
	if workRel == "" {
		return path
	}
	if path == "" {
		return workRel
	}
	return workRel + "/" + path
}

// ResolveAbs joins ScopeRel into an absolute path under the workspace root.
func (l RunLayout) ResolveAbs(path string, kind ArtifactKind) (string, error) {
	root := l.WorkspaceRoot
	if root == "" {
		root = DefaultWorkspaceDir()
	}
	return safeJoin(root, l.ScopeRel(path, kind))
}

func sanitizeRunID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "run"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "run"
	}
	return out
}
