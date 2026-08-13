package dashboard

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/glider-ai/glider/internal/tools"
)

func (s *Server) workspaceRoot() string {
	if s.Workspace != "" {
		return s.Workspace
	}
	return tools.DefaultWorkspaceDir()
}

// handleWorkspace lists / reads / diffs files under the tools sandbox (safeJoin only).
//
//	GET /api/workspace?path=runs/<id>/out
//	GET /api/workspace?run=<hoop_or_turn_id>          → work + out trees
//	GET /api/workspace?file=runs/<id>/out/report.md   → capped text preview
//	GET /api/workspace?diff=1&a=...&b=...             → unified diff of two paths
//	GET /api/workspace?diff=1&path=runs/<id>/work/x   → git diff if .git present
func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	root := s.workspaceRoot()
	q := r.URL.Query()

	if file := strings.TrimSpace(q.Get("file")); file != "" {
		s.handleWorkspaceFile(w, root, file, q.Get("limit"))
		return
	}
	if q.Get("diff") == "1" || q.Get("diff") == "true" {
		s.handleWorkspaceDiff(w, root, q.Get("a"), q.Get("b"), q.Get("path"), q.Get("limit"))
		return
	}

	runID := strings.TrimSpace(q.Get("run"))
	rel := strings.TrimSpace(q.Get("path"))
	recursive := q.Get("recursive") == "1" || q.Get("recursive") == "true"
	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	if runID != "" {
		lay := tools.LayoutForRun(root, runID)
		work, _ := tools.WalkFiles(root, lay.RelWork, limit)
		out, _ := tools.WalkFiles(root, lay.RelOut, limit)
		writeJSON(w, map[string]any{
			"workspace": root,
			"run":       lay.RunID,
			"work_dir":  lay.RelWork,
			"out_dir":   lay.RelOut,
			"mode":      lay.Mode,
			"binding": map[string]any{
				"workspace_root": lay.RootAbs,
				"work_rel":       lay.RelWork,
				"out_rel":        lay.RelOut,
				"work_dir":       lay.WorkAbs,
				"out_dir":        lay.OutAbs,
				"mode":           lay.Mode,
				"run_id":         lay.RunID,
			},
			"work": work,
			"out":  out,
		})
		return
	}

	if rel == "" {
		rel = "runs"
	}
	rel = filepath.ToSlash(rel)
	var files []string
	var err error
	if recursive {
		files, err = tools.WalkFiles(root, rel, limit)
	} else {
		files, err = tools.ListDir(root, rel, limit)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"workspace": root,
		"path":      rel,
		"files":     files,
	})
}

func (s *Server) handleWorkspaceFile(w http.ResponseWriter, root, rel, limitStr string) {
	maxBytes := 64 << 10
	if v := strings.TrimSpace(limitStr); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 256<<10 {
			maxBytes = n
		}
	}
	content, truncated, size, err := tools.ReadFileCapped(root, rel, maxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"workspace": root,
		"path":      filepath.ToSlash(rel),
		"content":   content,
		"truncated": truncated,
		"size":      size,
		"binary":    strings.Contains(content, "\x00"),
	})
}

func (s *Server) handleWorkspaceDiff(w http.ResponseWriter, root, a, b, path, limitStr string) {
	maxBytes := 64 << 10
	if v := strings.TrimSpace(limitStr); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 256<<10 {
			maxBytes = n
		}
	}
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	path = strings.TrimSpace(path)

	if a != "" && b != "" {
		diff, err := tools.DiffFiles(root, a, b, maxBytes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"workspace": root,
			"mode":      "files",
			"a":         filepath.ToSlash(a),
			"b":         filepath.ToSlash(b),
			"diff":      diff,
		})
		return
	}

	if path == "" {
		http.Error(w, "diff requires a+b paths or path= for git diff", http.StatusBadRequest)
		return
	}
	diff, repoRel, err := tools.GitDiffAt(root, path, maxBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"workspace": root,
		"mode":      "git",
		"path":      filepath.ToSlash(path),
		"repo":      repoRel,
		"diff":      diff,
	})
}
