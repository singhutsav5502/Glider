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

// candidatesPath finds configs/vendor_candidates.yaml from the current work
// directory. This is the same method that other code already uses for
// configs/glider.yaml. A person runs Glider from the top directory of the
// repository or of the installation.
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
		reg.ResponseDetail = prior.ResponseDetail
		if err := vendors.SaveRegistry(regPath, reg); err != nil {
			http.Error(w, "registry unwritable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, reg)

	case rest == "workspace" && r.Method == http.MethodPost:
		s.setDefaultWorkspace(w, r, regPath)

	case rest == "response-detail" && r.Method == http.MethodPost:
		s.setResponseDetail(w, r, regPath)

	case strings.HasSuffix(rest, "/enable") && r.Method == http.MethodPost:
		s.setVendorEnabled(w, regPath, strings.TrimSuffix(rest, "/enable"), true)

	case strings.HasSuffix(rest, "/disable") && r.Method == http.MethodPost:
		s.setVendorEnabled(w, regPath, strings.TrimSuffix(rest, "/disable"), false)

	case strings.HasSuffix(rest, "/templates") && r.Method == http.MethodPost:
		s.setVendorTemplates(w, r, regPath, strings.TrimSuffix(rest, "/templates"))

	case strings.HasSuffix(rest, "/launch-interactive") && r.Method == http.MethodPost:
		s.launchVendorInteractive(w, r, regPath, strings.TrimSuffix(rest, "/launch-interactive"))

	case strings.HasSuffix(rest, "/permissions") && r.Method == http.MethodGet:
		s.getVendorPermissions(w, r, regPath, strings.TrimSuffix(rest, "/permissions"))

	case strings.HasSuffix(rest, "/permissions") && r.Method == http.MethodPost:
		s.setVendorPermissions(w, r, regPath, strings.TrimSuffix(rest, "/permissions"))

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// resolveVendorAndWorkspace finds name in the registry and finds a workspace
// directory for it. It uses the value in the query or in the body when a person
// gives one. If not, it uses the default in the registry.
//
// The three handlers below use this function. Therefore the condition "no
// workspace is known" fails in the same clear way in each one.
func resolveVendorAndWorkspace(reg vendors.Registry, name, explicit string) (vendors.Vendor, string, error) {
	v, ok := reg.Find(name)
	if !ok {
		return vendors.Vendor{}, "", fmt.Errorf("unknown vendor: %s", name)
	}
	workspace := strings.TrimSpace(explicit)
	if workspace == "" {
		workspace = reg.DefaultWorkspace
	}
	return v, workspace, nil
}

// launchVendorInteractive opens a true window with a terminal, and that window
// runs the interactive mode of the vendor. Refer to vendors.LaunchInteractive.
//
// This is not a delegate call with no console. The code captures no output, and
// it sends nothing back into a conversation of Glider. Refer to
// planning/permission_relay_design.md §3 for the cause: this is more simple
// than a live relay through a pty, which is Path B.
func (s *Server) launchVendorInteractive(w http.ResponseWriter, r *http.Request, regPath, name string) {
	var body struct {
		Workspace string `json:"workspace"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // empty body is fine — falls back to the default workspace

	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		http.Error(w, "registry unreadable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	v, workspace, err := resolveVendorAndWorkspace(reg, name, body.Workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if workspace == "" {
		http.Error(w, "no workspace directory given and no default workspace configured", http.StatusBadRequest)
		return
	}
	if err := vendors.LaunchInteractive(v, workspace); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "workspace": workspace})
}

// getVendorPermissions reports the presets a vendor supports and which
// one (if any) currently matches its config — GET, read-only, never
// writes anything.
func (s *Server) getVendorPermissions(w http.ResponseWriter, r *http.Request, regPath, name string) {
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		http.Error(w, "registry unreadable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	v, workspace, err := resolveVendorAndWorkspace(reg, name, r.URL.Query().Get("workspace"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	current, err := vendors.CurrentPermissionPreset(v, workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"presets":   vendors.PermissionPresetsFor(v.Name),
		"current":   current,
		"workspace": workspace,
	})
}

// setVendorPermissions applies a preset with a name.
//
// For agy, it writes in the project config of agy. That change is permanent,
// and no code removes it. Refer to permissions.go.
//
// For claude, it writes the CommandTemplates of that vendor again in the
// registry, and it keeps them. That is the same file that the JSON editor in
// the "Advanced" area also writes. Refer to setVendorTemplates.
func (s *Server) setVendorPermissions(w http.ResponseWriter, r *http.Request, regPath, name string) {
	var body struct {
		Workspace string `json:"workspace"`
		Preset    string `json:"preset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Preset) == "" {
		http.Error(w, "preset is required", http.StatusBadRequest)
		return
	}

	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		http.Error(w, "registry unreadable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	v, workspace, err := resolveVendorAndWorkspace(reg, name, body.Workspace)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	switch v.Name {
	case "claude":
		newTemplates, err := vendors.ClaudeTemplatesForPreset(v.Templates, body.Preset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for i := range reg.Vendors {
			if reg.Vendors[i].Name == "claude" {
				reg.Vendors[i].Templates = newTemplates
			}
		}
		if err := vendors.SaveRegistry(regPath, reg); err != nil {
			http.Error(w, "registry unwritable: "+err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		if workspace == "" {
			http.Error(w, "no workspace directory given and no default workspace configured", http.StatusBadRequest)
			return
		}
		if err := vendors.ApplyPermissionPreset(v, workspace, body.Preset); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "preset": body.Preset})
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

// setDefaultWorkspace keeps a new default workspace directory. An empty path
// removes the default. It also applies the value immediately to the live
// WorkspaceStore in memory, and a person needs no restart.
//
// A change to the registry of vendors is different: it operates only through
// the file that most callers read for each request. Refer to the comment in
// internal/vendors/workspace.go for the cause of this function.
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

// setResponseDetail persists a new response-detail mode ("clean" or "raw")
// and applies it immediately to the live in-memory value — same
// persist-and-apply pattern as setDefaultWorkspace above. An unrecognized
// mode is rejected outright (400) rather than silently coerced, so a
// dashboard typo is visible immediately instead of quietly becoming
// "clean" with no feedback.
func (s *Server) setResponseDetail(w http.ResponseWriter, r *http.Request, regPath string) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Mode != vendors.ResponseDetailClean && body.Mode != vendors.ResponseDetailRaw {
		http.Error(w, `mode must be "clean" or "raw"`, http.StatusBadRequest)
		return
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		http.Error(w, "registry unreadable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reg.ResponseDetail = body.Mode
	if err := vendors.SaveRegistry(regPath, reg); err != nil {
		http.Error(w, "registry unwritable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	vendors.SetResponseDetail(body.Mode)
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
