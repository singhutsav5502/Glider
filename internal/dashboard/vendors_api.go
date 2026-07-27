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

// resolveVendorAndWorkspace looks up name in the registry and resolves a
// workspace directory for it — the explicit query/body value if given,
// else the registry's configured default. Shared by the three handlers
// below so "no workspace known yet" fails the same clear way in each.
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

// launchVendorInteractive opens a real, visible terminal window running
// the vendor's own interactive mode (vendors.LaunchInteractive) — not a
// headless delegate call, no output captured, nothing relayed back into
// any Glider conversation. See planning/permission_relay_design.md §3 for
// why this is deliberately simpler than a live pty relay (Path B).
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

// setVendorPermissions applies a named preset — for agy this writes
// (persistently, no revert — see permissions.go) into its own project
// config; for claude it rewrites and persists the vendor's own
// CommandTemplates in the registry, the same file the "Advanced" JSON
// editor (setVendorTemplates) also writes.
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
