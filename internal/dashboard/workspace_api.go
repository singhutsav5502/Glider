package dashboard

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/glider-ai/glider/internal/tools"
)

// workspaceTreeEntry is one node in a work/out directory listing.
type workspaceTreeEntry struct {
	Name  string               `json:"name"`
	Path  string               `json:"path"` // relative to work or out root
	Dir   bool                 `json:"dir,omitempty"`
	Size  int64                `json:"size,omitempty"`
	Kids  []workspaceTreeEntry `json:"children,omitempty"`
}

type workspaceAPIResponse struct {
	RunID         string               `json:"run_id"`
	Mode          string               `json:"mode,omitempty"`
	WorkspaceRoot string               `json:"workspace_root"`
	WorkRel       string               `json:"work_rel"`
	OutRel        string               `json:"out_rel"`
	WorkDir       string               `json:"work_dir"`
	OutDir        string               `json:"out_dir"`
	WorkTree      []workspaceTreeEntry `json:"work_tree"`
	OutTree       []workspaceTreeEntry `json:"out_tree"`
	Source        string               `json:"source,omitempty"` // hoop|swarm|layout
}

func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("run"))
	if runID == "" {
		runID = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if runID == "" {
		http.Error(w, "run query param required", http.StatusBadRequest)
		return
	}

	resp := workspaceAPIResponse{RunID: runID}
	var layout tools.RunLayout
	found := false

	if s.Loops != nil {
		if st, err := s.Loops.Get(runID); err == nil && st != nil && st.Workspace.WorkDir != "" {
			layout = tools.RunLayout{
				WorkspaceRoot: st.Workspace.WorkspaceRoot,
				RunID:         st.Workspace.RunID,
				Mode:          st.Workspace.Mode,
				WorkDir:       st.Workspace.WorkDir,
				OutDir:        st.Workspace.OutDir,
				WorkRel:       st.Workspace.WorkRel,
				OutRel:        st.Workspace.OutRel,
			}
			if layout.RunID == "" {
				layout.RunID = runID
			}
			found = true
			resp.Source = "hoop"
		}
	}

	reg := s.toolsRegistry()
	if !found && reg != nil {
		if cur, ok := reg.CurrentLayout(); ok && (cur.RunID == runID || cur.RunID == tools.LayoutForRun(reg.Workspace(), runID).RunID) {
			layout = cur
			found = true
			resp.Source = "layout"
		}
	}
	if !found {
		root := tools.DefaultWorkspaceDir()
		if reg != nil {
			root = reg.Workspace()
		}
		layout = tools.LayoutForRun(root, runID)
		// Soft-ensure so the tab can show empty trees for known runs.
		_ = layout.Ensure()
		resp.Source = "layout"
	}

	resp.Mode = layout.Mode
	resp.WorkspaceRoot = layout.WorkspaceRoot
	resp.WorkRel = layout.WorkRel
	resp.OutRel = layout.OutRel
	resp.WorkDir = layout.WorkDir
	resp.OutDir = layout.OutDir
	resp.WorkTree = listTree(layout.WorkDir, "", 4, 200)
	resp.OutTree = listTree(layout.OutDir, "", 4, 200)
	writeJSON(w, resp)
}

func (s *Server) toolsRegistry() *tools.Registry {
	if s.Loops != nil && s.Loops.Tools != nil {
		return s.Loops.Tools
	}
	if s.Swarm != nil && s.Swarm.Tools != nil {
		return s.Swarm.Tools
	}
	return nil
}

func listTree(root, rel string, depthLeft, budget int) []workspaceTreeEntry {
	if budget <= 0 || depthLeft < 0 {
		return nil
	}
	dir := root
	if rel != "" {
		dir = filepath.Join(root, filepath.FromSlash(rel))
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []workspaceTreeEntry
	for _, e := range ents {
		if budget <= 0 {
			break
		}
		budget--
		name := e.Name()
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		entry := workspaceTreeEntry{Name: name, Path: childRel, Dir: e.IsDir()}
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				entry.Size = info.Size()
			}
		} else if depthLeft > 0 {
			entry.Kids = listTree(root, childRel, depthLeft-1, budget)
			budget -= len(entry.Kids)
		}
		out = append(out, entry)
	}
	return out
}
