package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/glider-ai/glider/internal/vendors"
)

// candidatesPath resolves configs/vendor_candidates.yaml relative to the
// current working directory, matching how configs/glider.yaml is already
// referenced elsewhere (Glider is run from the repo/install root).
func candidatesPath() string {
	return filepath.Join("configs", "vendor_candidates.yaml")
}

// handleVendors serves GET /api/vendors (list the persisted registry) and
// POST /api/vendors/discover (rescan candidates and persist the result).
func (s *Server) handleVendors(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/vendors"), "/")

	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		http.Error(w, "registry path unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}

	switch {
	case rest == "" && r.Method == http.MethodGet:
		reg, err := vendors.LoadRegistry(regPath)
		if err != nil {
			http.Error(w, "registry unreadable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, reg)

	case rest == "discover" && r.Method == http.MethodPost:
		candidates, err := vendors.LoadCandidates(candidatesPath())
		if err != nil {
			http.Error(w, "candidates unreadable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Discover builds a fresh Registry from the candidate scan alone —
		// preserve the previously-persisted DefaultWorkspace explicitly, or
		// a "Rescan" click would silently wipe it.
		prior, _ := vendors.LoadRegistry(regPath)
		reg := vendors.Discover(context.Background(), candidates)
		reg.DefaultWorkspace = prior.DefaultWorkspace
		if err := vendors.SaveRegistry(regPath, reg); err != nil {
			http.Error(w, "registry unwritable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, reg)

	case rest == "workspace" && r.Method == http.MethodPost:
		s.setDefaultWorkspace(w, r, regPath)

	case strings.HasSuffix(rest, "/enable") && r.Method == http.MethodPost:
		s.setVendorEnabled(w, regPath, strings.TrimSuffix(rest, "/enable"), true)

	case strings.HasSuffix(rest, "/disable") && r.Method == http.MethodPost:
		s.setVendorEnabled(w, regPath, strings.TrimSuffix(rest, "/disable"), false)

	case strings.HasSuffix(rest, "/templates") && r.Method == http.MethodPost:
		s.setVendorTemplates(w, r, regPath, strings.TrimSuffix(rest, "/templates"))

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) setVendorEnabled(w http.ResponseWriter, regPath, name string, enabled bool) {
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		http.Error(w, "registry unreadable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !reg.SetEnabled(name, enabled) {
		http.Error(w, "unknown vendor: "+name, http.StatusNotFound)
		return
	}
	if err := vendors.SaveRegistry(regPath, reg); err != nil {
		http.Error(w, "registry unwritable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, reg)
}

// setDefaultWorkspace persists a new default workspace directory (or
// clears it, given an empty path) and applies it immediately to the
// live in-memory WorkspaceStore — no restart needed, unlike a vendor
// registry change which only takes effect through the file most callers
// read on every request. See internal/vendors/workspace.go's doc comment
// for why this exists at all.
func (s *Server) setDefaultWorkspace(w http.ResponseWriter, r *http.Request, regPath string) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		http.Error(w, "registry unreadable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reg.DefaultWorkspace = body.Path
	if err := vendors.SaveRegistry(regPath, reg); err != nil {
		http.Error(w, "registry unwritable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	vendors.SetDefaultWorkspace(body.Path)
	writeJSON(w, reg)
}

// setVendorTemplates replaces one vendor's CommandTemplate list wholesale —
// the dashboard's template editor (planning/permission_relay_design.md §1,
// §4) always sends the full, edited array back, not a diff. Rejects a
// blank or duplicate template name outright rather than persisting
// something ResolveTemplate could never resolve correctly later.
func (s *Server) setVendorTemplates(w http.ResponseWriter, r *http.Request, regPath, name string) {
	var body struct {
		Templates []vendors.CommandTemplate `json:"templates"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, t := range body.Templates {
		if strings.TrimSpace(t.Name) == "" {
			http.Error(w, "every template needs a non-empty name", http.StatusBadRequest)
			return
		}
		if seen[t.Name] {
			http.Error(w, fmt.Sprintf("duplicate template name %q", t.Name), http.StatusBadRequest)
			return
		}
		seen[t.Name] = true
		if len(t.Args) == 0 {
			http.Error(w, fmt.Sprintf("template %q needs at least one arg", t.Name), http.StatusBadRequest)
			return
		}
	}

	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		http.Error(w, "registry unreadable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	idx := -1
	for i := range reg.Vendors {
		if reg.Vendors[i].Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		http.Error(w, "unknown vendor: "+name, http.StatusNotFound)
		return
	}
	reg.Vendors[idx].Templates = body.Templates
	if err := vendors.SaveRegistry(regPath, reg); err != nil {
		http.Error(w, "registry unwritable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, reg)
}
