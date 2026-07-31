package vendors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glider-ai/glider/internal/atomicfile"
)

// GrantResumePermission implements agyAdapter's side of the one execution-
// time quirk that doesn't fit the uniform CommandTemplate model.
//
// History (planning/permission_relay_design.md §2.3, §7-§8): a
// settings.json write was the first idea, rejected (2026-07-26) as too
// invasive and too agy-specific for the core design. The alternative —
// scoping a resume template to --dangerously-skip-permissions/--continue —
// was implemented instead, then proven broken by live testing against the
// real agy.exe: --continue/--conversation<id> both resumed an unrelated
// "most recent" conversation rather than the denied one (reproduced 4+
// times, including immediately after the denial), and
// --dangerously-skip-permissions made the model respond by explaining the
// flag instead of performing the task (reproduced across 4 different flag
// positions and prompt phrasings). agy's own CLI changelog then settled
// the question: 1.1.3 "the CLI now soft-denies such tools and prints a
// stderr notice naming the allow-rule needed" and 1.1.6 "headless (-p)
// runs... now honor persisted settings.json policies, including
// permissions" — confirming settings.json's permissions.allow is agy's
// OFFICIALLY DESIGNED headless-permission mechanism, not a workaround.
// Reinstated with explicit sign-off (2026-07-26) once this was known, and
// confirmed live afterward: adding an allow rule does suppress agy's hard
// "command" denial (no stderr error on the next attempt, verified against
// the real settings.json file with a snapshot/restore around the test).
//
// Extended 2026-07-26 (same day) to also grant into a directory-specific
// PROJECT config when one exists: agy's changelog confirms
// "project-specific configurations (located in ~/.gemini/config/projects/)
// take precedence over global settings in
// ~/.gemini/antigravity-cli/settings.json" — so a workspace that has ever
// been opened interactively (and so acquired its own project file) would
// silently ignore a global-only grant. Real schema found live by reading
// actual project files on this machine: each is a JSON file at
// ~/.gemini/config/projects/<id>.json, identifying its bound directory via
// projectResources.resources[].gitFolder.folderUri (a file:// URI), and
// carrying its own allow-list at the confirmed real (if oddly
// double-nested) path permissionGrants.permissionGrants.allow — same
// "tool(target)" string syntax as the global file.
//
// Scope and limits, stated plainly: this grants access to the TOOL
// CATEGORY that was denied (e.g. "command(*)" for any denied "command"
// permission), not the one specific command string — agy exposes no
// finer-grained rule syntax than that (confirmed: the CLI's own error text
// names exactly this "command(<target>)" form, and no scoped variant was
// found in --help). Real project files observed on this machine only ever
// contained single-exact-path rules (e.g. "write_file(C:\...\x.csv)"), not
// a "*" wildcard — "*" is not independently confirmed to be honored by
// agy's rule matcher for a project file the same way it was confirmed live
// for the global file; kept consistent with the global file's proven
// syntax rather than invent an unconfirmed alternative. The revert is a
// byte-for-byte snapshot restore of each touched file, not a surgical
// removal of just the added rule(s) — simpler and safe against Glider's
// own corruption of the file, at the cost that a concurrent write by agy
// itself during the resume call (e.g. updating trustedWorkspaces) would be
// lost on revert. Getting the model to then actually PERFORM the
// originally-requested action (rather than asking a clarifying question)
// is separate — see WrapResumePrompt.
func (agyAdapter) GrantResumePermission(v Vendor, cwd string, denials []Denial) (func() error, error) {
	toolNames := uniqueDenialToolNames(denials)
	if len(toolNames) == 0 {
		return func() error { return nil }, nil
	}
	rules := make([]string, len(toolNames))
	for i, name := range toolNames {
		rules[i] = name + "(*)"
	}

	globalPath, err := agySettingsPath()
	if err != nil {
		return nil, fmt.Errorf("vendors: resolving agy settings.json path: %w", err)
	}
	globalRevert, err := grantRulesInFile(globalPath, []string{"permissions", "allow"}, rules)
	if err != nil {
		return nil, fmt.Errorf("vendors: granting in global agy settings.json: %w", err)
	}

	var projectRevert func() error
	if cwd != "" {
		projectPath, found, err := findProjectFileForDir(cwd)
		if err != nil {
			// Best-effort: a scan failure (unreadable directory, one
			// corrupt sibling file) shouldn't block the global grant that
			// already succeeded above — the global file is still a real,
			// working fallback even if project-matching itself is degraded.
			projectRevert = func() error { return nil }
		} else if found {
			projectRevert, err = grantRulesInFile(projectPath, []string{"permissionGrants", "permissionGrants", "allow"}, rules)
			if err != nil {
				_ = globalRevert()
				return nil, fmt.Errorf("vendors: granting in agy project file %s: %w", projectPath, err)
			}
		} else {
			projectRevert = func() error { return nil }
		}
	} else {
		projectRevert = func() error { return nil }
	}

	return func() error {
		err1 := globalRevert()
		err2 := projectRevert()
		if err1 != nil {
			return err1
		}
		return err2
	}, nil
}

// WrapResumePrompt addresses a real, separately-confirmed gap
// (planning/ngl_and_adapters.md §9): agy's resume reliably clears the
// permission gate (no denial on the resumed call) but the model itself
// often responds by describing the workspace instead of performing the
// requested action — reproduced 6 consecutive times live, across varied
// prompt phrasings, none of which included this specific "you already
// have permission" framing tied to the resume moment. This is a
// prompt-engineering mitigation, not a guaranteed fix — stated plainly
// rather than oversold; the model may still choose to hedge. The real,
// complete fix for guaranteed single-shot reliability is Path B
// (interactive mode), which this does not attempt to replace.
func (agyAdapter) WrapResumePrompt(prompt string) string {
	return "Permission for this action has already been granted. Do not describe the directory or ask a follow-up question — perform the action directly: " + prompt
}

// ExtraResumeArgs is nil for agy — its per-denial grant is a real
// settings.json side effect (GrantResumePermission above), not a resume
// argv flag; agy's own resume is a bare --continue with no per-tool flag
// to append to in the first place.
func (agyAdapter) ExtraResumeArgs(denials []Denial) []string { return nil }

func uniqueDenialToolNames(denials []Denial) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range denials {
		if d.ToolName == "" || seen[d.ToolName] {
			continue
		}
		seen[d.ToolName] = true
		out = append(out, d.ToolName)
	}
	return out
}

func containsAnyString(list []any, want string) bool {
	for _, v := range list {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

// grantRulesInFile reads path (JSON object; treated as {} if the file
// doesn't exist yet), ensures each rule in rules is present in the array
// at the given nested key path (creating intermediate objects/the array as
// needed), writes the file back if anything changed, and returns a revert
// func restoring the exact original bytes (or removing the file entirely
// if this call created it). No-ops (revert does nothing) if every rule was
// already present.
func grantRulesInFile(path string, keyPath []string, rules []string) (func() error, error) {
	original, existed, err := readIfExists(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	doc := map[string]any{}
	if existed {
		if err := json.Unmarshal(original, &doc); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON, refusing to modify it: %w", path, err)
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	cur := doc
	for _, key := range keyPath[:len(keyPath)-1] {
		next, _ := cur[key].(map[string]any)
		if next == nil {
			next = map[string]any{}
			cur[key] = next
		}
		cur = next
	}
	lastKey := keyPath[len(keyPath)-1]
	allow, _ := cur[lastKey].([]any)
	changed := false
	for _, rule := range rules {
		if !containsAnyString(allow, rule) {
			allow = append(allow, rule)
			changed = true
		}
	}
	if !changed {
		return func() error { return nil }, nil
	}
	cur[lastKey] = allow

	updated, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling updated %s: %w", path, err)
	}
	// atomicfile, not os.WriteFile: this is the user's real, external agy
	// settings.json, not a Glider-owned file — a crash mid-write here
	// (this call, or the revert closure below, which runs again on every
	// resumed delegate call) would corrupt a config file Glider has no
	// way to help the user recover, not just lose Glider's own state.
	if err := atomicfile.WriteFile(path, updated, 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}

	return func() error {
		if !existed {
			return os.Remove(path)
		}
		return atomicfile.WriteFile(path, original, 0o644)
	}, nil
}

// readIfExists reads path, reporting existed=false (no error) for a
// missing file rather than making the caller special-case os.IsNotExist.
func readIfExists(path string) (data []byte, existed bool, err error) {
	data, err = os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// agySettingsPath resolves agy's real GLOBAL settings file —
// ~/.gemini/antigravity-cli/settings.json, confirmed live 2026-07-26. This
// is a DIFFERENT file from ~/.gemini/config/config.json, which also exists
// on a machine with agy installed and also has permissions-shaped data —
// don't confuse the two; only antigravity-cli/settings.json is honored by
// headless (-p) runs per the changelog citation above.
func agySettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), nil
}

// agyProjectsDir resolves ~/.gemini/config/projects — where agy stores one
// JSON file per known project.
func agyProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "config", "projects"), nil
}

// findProjectFileForDir scans agy's known projects for one whose bound
// folder matches dir. ok=false with a nil error is the common, expected
// case for a scratch/one-off delegation target that's never been opened
// interactively — not a failure, just "nothing to layer the grant into
// beyond the global file."
//
// Real project files on this machine use TWO different shapes for the
// same field, not one: git-repo-bound projects nest it under
// resources[].gitFolder.folderUri, but plain-directory projects (no git
// repo involved — e.g. a scratch folder, or one bound via a bare
// "folderUri" some agy versions write) put it directly at
// resources[].folderUri instead. An earlier version of this function only
// checked the gitFolder-nested form — confirmed live (2026-07-26, found
// while building the permission-preset writer) to silently miss every
// bare-form project file, which meant GrantResumePermission's per-project
// grant was skipping those directories entirely and falling back to the
// global-only grant, unnoticed because the global grant still succeeded.
// Fixed by checking both shapes.
func findProjectFileForDir(dir string) (path string, ok bool, err error) {
	projectsDir, err := agyProjectsDir()
	if err != nil {
		return "", false, err
	}
	entries, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	wantURI, err := dirToFileURI(dir)
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		p := filepath.Join(projectsDir, entry.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue // one unreadable sibling shouldn't abort the whole scan
		}
		var doc struct {
			ProjectResources struct {
				Resources []struct {
					FolderURI string `json:"folderUri"` // bare form
					GitFolder struct {
						FolderURI string `json:"folderUri"` // git-repo-bound form
					} `json:"gitFolder"`
				} `json:"resources"`
			} `json:"projectResources"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			continue
		}
		for _, r := range doc.ProjectResources.Resources {
			if strings.EqualFold(r.FolderURI, wantURI) || strings.EqualFold(r.GitFolder.FolderURI, wantURI) {
				return p, true, nil
			}
		}
	}
	return "", false, nil
}

// dirToFileURI converts an absolute filesystem path into the file:// URI
// form confirmed live in real project files (e.g.
// "file:///C:/Users/Utsav/Documents/antigravity/keen-einstein" — forward
// slashes, uppercase drive letter, no trailing slash).
func dirToFileURI(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	abs = filepath.ToSlash(abs)
	if len(abs) >= 2 && abs[1] == ':' {
		return "file:///" + strings.ToUpper(abs[:1]) + abs[1:], nil
	}
	return "file://" + abs, nil
}
