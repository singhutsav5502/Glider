package vendors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/glider-ai/glider/internal/atomicfile"
	"github.com/google/uuid"
)

// PermissionPreset is one named, pre-calibrated permission profile a
// non-technical user can pick from the dashboard instead of hand-editing
// a vendor's own config file or CommandTemplate args. Presets are
// deliberately coarse — three choices, not a settings matrix — because
// the dashboard's Vendors tab is meant to work for someone who has never
// seen agy's settings.json schema, not just for someone who already has.
// The raw underlying config (agy's settings.json, or a vendor's
// CommandTemplate args) stays reachable under the tab's "Advanced"
// section for anyone who wants more control than these three give.
type PermissionPreset struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Risky       bool   `json:"risky"`
}

var (
	presetAskEveryTime = PermissionPreset{
		ID: "ask", Label: "Ask every time",
		Description: "Safest. A denied action gets relayed back to you to approve — the default, and the right choice unless delegation into this folder keeps hitting denials you always approve anyway.",
	}
	presetTrustFolder = PermissionPreset{
		ID: "trust_folder", Label: "Trust this folder",
		Description: "Reading and writing files in this folder is pre-approved, so headless delegation stops hitting a first-attempt denial for ordinary file access. Running shell commands and reaching the internet still ask first.",
	}
	presetFullAuto = PermissionPreset{
		ID: "full_auto", Label: "Full auto",
		Description: "Everything pre-approved: files, commands, internet access. Nothing gets relayed back to you to confirm. Only use this for a folder you'd trust an unsupervised script to run in.",
		Risky:       true,
	}
	presetTrustSession = PermissionPreset{
		ID: "trust_session", Label: "Trust this session",
		Description: "Skips claude's own permission checks for delegated calls (--dangerously-skip-permissions). Nothing gets relayed back to you to confirm.",
		Risky:       true,
	}
	presetAlwaysTrusted = PermissionPreset{
		ID: "always_trusted", Label: "Always trusted (fixed)",
		Description: "cursor-agent requires --trust for headless delegation to reach the model at all (confirmed live — there is no working \"ask every time\" option for this vendor in headless mode). This is informational, not a switch.",
	}
)

// PermissionPresetsFor returns the presets a given vendor actually
// supports — never the same three for every vendor, because the three
// vendors' own permission mechanisms are not equivalent (see
// planning/permission_relay_design.md §3 and the agy_grant.go doc
// comment). An unknown vendor name gets no presets, not a guess.
func PermissionPresetsFor(vendorName string) []PermissionPreset {
	switch vendorName {
	case "agy":
		return []PermissionPreset{presetAskEveryTime, presetTrustFolder, presetFullAuto}
	case "claude":
		return []PermissionPreset{presetAskEveryTime, presetTrustSession}
	case "cursor-agent":
		return []PermissionPreset{presetAlwaysTrusted}
	default:
		return nil
	}
}

// agyPresetSettings is the persistent (not scoped/reverted, unlike
// GrantResumePermission) settings block written into a project's own
// ~/.gemini/config/projects/<id>.json — the real, confirmed schema found
// on this machine (see agy_grant.go's doc comment). "ask" mirrors agy's
// own out-of-the-box conservative posture; the other two progressively
// relax it.
var agyPresetSettings = map[string]map[string]string{
	"ask": {
		"fileAccessPolicy":    "AGENT_SETTING_POLICY_ASK",
		"internetPolicy":      "AGENT_SETTING_POLICY_ASK",
		"autoExecutionPolicy": "CASCADE_COMMANDS_AUTO_EXECUTION_OFF",
		"artifactReviewMode":  "ARTIFACT_REVIEW_MODE_ALWAYS",
	},
	"trust_folder": {
		"fileAccessPolicy":    "AGENT_SETTING_POLICY_ALLOW",
		"internetPolicy":      "AGENT_SETTING_POLICY_ASK",
		"autoExecutionPolicy": "CASCADE_COMMANDS_AUTO_EXECUTION_OFF",
		"artifactReviewMode":  "ARTIFACT_REVIEW_MODE_ALWAYS",
	},
	"full_auto": {
		"fileAccessPolicy":    "AGENT_SETTING_POLICY_ALLOW",
		"internetPolicy":      "AGENT_SETTING_POLICY_ALLOW",
		"autoExecutionPolicy": "CASCADE_COMMANDS_AUTO_EXECUTION_EAGER",
		"artifactReviewMode":  "ARTIFACT_REVIEW_MODE_ALWAYS",
	},
}

// ApplyPermissionPreset persists presetID as vendor v's permission
// configuration for cwd — the file-backed vendors only (agy today).
// Unlike GrantResumePermission (internal/vendors/agy_grant.go), this is a
// deliberate, explicit, PERSISTENT user choice — no revert function, no
// scoping to one resume call. claude's preset works differently (it's
// expressed as CommandTemplate args, not a separate config file) — see
// ClaudeTemplatesForPreset. cursor-agent has no settable preset at all.
func ApplyPermissionPreset(v Vendor, cwd, presetID string) error {
	switch v.Name {
	case "agy":
		if strings.TrimSpace(cwd) == "" {
			return fmt.Errorf("vendors: a workspace directory is required to apply a permission preset")
		}
		settings, ok := agyPresetSettings[presetID]
		if !ok {
			return fmt.Errorf("vendors: agy has no preset %q", presetID)
		}
		return writeAgyProjectSettings(cwd, settings)
	case "claude":
		return fmt.Errorf("vendors: claude's preset is applied via its CommandTemplates, not ApplyPermissionPreset — see ClaudeTemplatesForPreset")
	case "cursor-agent":
		return fmt.Errorf("vendors: cursor-agent's trust posture is fixed (see PermissionPresetsFor), not settable")
	default:
		return fmt.Errorf("vendors: no permission presets defined for vendor %q", v.Name)
	}
}

// claudeSkipPermissionsFlag is inserted right after the print flag in
// every headless template when the "trust_session" preset is selected,
// and stripped back out for "ask" — the one flag Claude Code itself
// documents for bypassing its own permission checks in print mode.
const claudeSkipPermissionsFlag = "--dangerously-skip-permissions"

// ClaudeTemplatesForPreset returns a copy of templates with
// claudeSkipPermissionsFlag inserted into (presetTrustSession) or removed
// from (presetAskEveryTime) every headless template's args. Idempotent —
// applying the same preset twice is a no-op the second time, so this is
// safe to call from a handler that doesn't first check the current state.
func ClaudeTemplatesForPreset(templates []CommandTemplate, presetID string) ([]CommandTemplate, error) {
	want := presetID == presetTrustSession.ID
	if !want && presetID != presetAskEveryTime.ID {
		return nil, fmt.Errorf("vendors: claude has no preset %q", presetID)
	}
	out := make([]CommandTemplate, len(templates))
	for i, t := range templates {
		out[i] = t
		if t.Mode != "headless" {
			continue
		}
		has := false
		var args []string
		for _, a := range t.Args {
			if a == claudeSkipPermissionsFlag {
				has = true
				if !want {
					continue // drop it
				}
			}
			args = append(args, a)
		}
		if want && !has {
			// Right after the print flag (args[0], e.g. "-p") so it reads
			// naturally next to it rather than trailing after {{prompt}}.
			inserted := make([]string, 0, len(args)+1)
			inserted = append(inserted, args[0], claudeSkipPermissionsFlag)
			inserted = append(inserted, args[1:]...)
			args = inserted
		}
		out[i].Args = args
	}
	return out, nil
}

// CurrentPermissionPreset reports which preset, if any, cleanly matches
// vendor v's current config — "" (not an error) when nothing written by
// ApplyPermissionPreset/ClaudeTemplatesForPreset matches, e.g. the user
// hand-edited the file/templates. "unknown" configs are real and
// expected, not a bug to hide. cwd is only meaningful for agy (its
// permission config is per-project); claude's is registry-wide.
func CurrentPermissionPreset(v Vendor, cwd string) (presetID string, err error) {
	if v.Name == "claude" {
		for _, t := range v.Templates {
			if t.Mode != "headless" {
				continue
			}
			if slices.Contains(t.Args, claudeSkipPermissionsFlag) {
				return presetTrustSession.ID, nil
			}
		}
		return presetAskEveryTime.ID, nil
	}
	if v.Name != "agy" || strings.TrimSpace(cwd) == "" {
		return "", nil
	}
	current, err := readAgyProjectSettings(cwd)
	if err != nil {
		return "", err
	}
	if current == nil {
		return "", nil // no project file yet — agy's own out-of-the-box default applies, not any preset we wrote
	}
	for id, want := range agyPresetSettings {
		if settingsEqual(current, want) {
			return id, nil
		}
	}
	return "", nil
}

func settingsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}

// writeAgyProjectSettings finds cwd's existing agy project file, or
// creates a minimal new one (bare "folderUri", no git metadata — several
// real project files on this machine already use that simpler form, see
// agy_grant.go's examples), and overwrites its "settings" object wholesale
// with the preset's four fields. Fields outside "settings"
// (permissionGrants, projectResources) are left untouched.
func writeAgyProjectSettings(cwd string, settings map[string]string) error {
	path, found, err := findProjectFileForDir(cwd)
	if err != nil {
		return fmt.Errorf("vendors: scanning agy project files: %w", err)
	}
	doc := map[string]any{}
	if found {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("vendors: reading %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("vendors: %s is not valid JSON, refusing to modify it: %w", path, err)
		}
	} else {
		uri, err := dirToFileURI(cwd)
		if err != nil {
			return fmt.Errorf("vendors: resolving %s to a file URI: %w", cwd, err)
		}
		projectsDir, err := agyProjectsDir()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(projectsDir, 0o755); err != nil {
			return fmt.Errorf("vendors: creating %s: %w", projectsDir, err)
		}
		id := uuid.NewString()
		path = filepath.Join(projectsDir, id+".json")
		doc["id"] = id
		doc["name"] = filepath.Base(cwd)
		doc["projectResources"] = map[string]any{
			"resources": []any{map[string]any{"folderUri": uri}},
		}
	}

	settingsAny := make(map[string]any, len(settings))
	for k, v := range settings {
		settingsAny[k] = v
	}
	doc["settings"] = settingsAny

	updated, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("vendors: marshaling %s: %w", path, err)
	}
	// Same reasoning as agy_grant.go's grantRulesInFile: this is the
	// user's real, external agy project-settings file, not Glider's own.
	if err := atomicfile.WriteFile(path, updated, 0o644); err != nil {
		return fmt.Errorf("vendors: writing %s: %w", path, err)
	}
	return nil
}

// readAgyProjectSettings returns cwd's project file's "settings" object
// as a flat map[string]string, or nil (no error) if no project file
// exists for cwd yet.
func readAgyProjectSettings(cwd string) (map[string]string, error) {
	path, found, err := findProjectFileForDir(cwd)
	if err != nil || !found {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("vendors: reading %s: %w", path, err)
	}
	var doc struct {
		Settings map[string]string `json:"settings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("vendors: %s is not valid JSON: %w", path, err)
	}
	return doc.Settings, nil
}
