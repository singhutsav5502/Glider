package vendors_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/vendors"
)

// isolateAgySettings redirects agySettingsPath() (which resolves under the
// real user's home directory) to a per-test temp dir, so these tests never
// touch the real ~/.gemini/antigravity-cli/settings.json.
func isolateAgySettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	return filepath.Join(dir, ".gemini", "antigravity-cli", "settings.json")
}

func agyGrantAdapter(t *testing.T) vendors.VendorAdapter {
	t.Helper()
	return vendors.AdapterForTest("agy")
}

func TestAgyAdapter_WrapResumePrompt_AddsActNowFraming(t *testing.T) {
	a := agyGrantAdapter(t)
	wrapped := a.WrapResumePrompt("delete old_auth.py")
	if !strings.Contains(wrapped, "delete old_auth.py") {
		t.Fatalf("got %q, expected the original prompt preserved", wrapped)
	}
	if !strings.Contains(wrapped, "already been granted") {
		t.Fatalf("got %q, expected the act-now framing prepended", wrapped)
	}
}

func TestClaudeAndCursorAgentAdapters_WrapResumePromptIsNoop(t *testing.T) {
	for _, name := range []string{"claude", "cursor-agent"} {
		a := vendors.AdapterForTest(name)
		if got := a.WrapResumePrompt("do the thing"); got != "do the thing" {
			t.Fatalf("%s: got %q, expected the prompt unchanged", name, got)
		}
	}
}

func TestAgyGrant_CreatesFileWhenMissing(t *testing.T) {
	path := isolateAgySettings(t)
	a := agyGrantAdapter(t)

	revert, err := a.GrantResumePermission(vendors.Vendor{Name: "agy"}, "", []vendors.Denial{{ToolName: "command"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected settings.json to be created, got: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	perms, _ := doc["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	if len(allow) != 1 || allow[0] != "command(*)" {
		t.Fatalf("got allow %+v", allow)
	}

	if err := revert(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected the created file to be removed on revert, got err=%v", err)
	}
}

func TestAgyGrant_PreservesExistingKeysAndRevertsExactly(t *testing.T) {
	path := isolateAgySettings(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	original := []byte(`{
  "colorScheme": "dark",
  "trustedWorkspaces": [
    "C:\\Users\\Utsav"
  ]
}`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a := agyGrantAdapter(t)
	revert, err := a.GrantResumePermission(vendors.Vendor{Name: "agy"}, "", []vendors.Denial{{ToolName: "command"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc["colorScheme"] != "dark" {
		t.Fatalf("expected unrelated existing keys to be preserved, got %+v", doc)
	}
	perms, _ := doc["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	if len(allow) != 1 || allow[0] != "command(*)" {
		t.Fatalf("got allow %+v", allow)
	}

	if err := revert(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("expected exact byte-for-byte restore, got %s", restored)
	}
}

func TestAgyGrant_NoDenialsIsNoop(t *testing.T) {
	path := isolateAgySettings(t)
	a := agyGrantAdapter(t)

	revert, err := a.GrantResumePermission(vendors.Vendor{Name: "agy"}, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file to be created when there are no denials to grant")
	}
	if err := revert(); err != nil {
		t.Fatalf("unexpected error from a no-op revert: %v", err)
	}
}

func TestAgyGrant_AlreadyPresentRuleIsNoop(t *testing.T) {
	path := isolateAgySettings(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	existing := []byte(`{"permissions":{"allow":["command(*)"]}}`)
	if err := os.WriteFile(path, existing, 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a := agyGrantAdapter(t)
	revert, err := a.GrantResumePermission(vendors.Vendor{Name: "agy"}, "", []vendors.Denial{{ToolName: "command"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := revert(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(after) != string(existing) {
		t.Fatalf("expected the file untouched when the rule already existed, got %s", after)
	}
}

func TestAgyGrant_MultipleDenialsAddOneRulePerTool(t *testing.T) {
	path := isolateAgySettings(t)
	a := agyGrantAdapter(t)

	_, err := a.GrantResumePermission(vendors.Vendor{Name: "agy"}, "", []vendors.Denial{
		{ToolName: "command"}, {ToolName: "write_to_file"}, {ToolName: "command"}, // duplicate on purpose
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	perms, _ := doc["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	if len(allow) != 2 {
		t.Fatalf("got allow %+v, want exactly 2 rules (duplicates collapsed)", allow)
	}
}

// TestAgyGrant_AlsoGrantsInMatchingProjectFile matches the real schema
// found live 2026-07-26 by reading actual project files on this machine:
// ~/.gemini/config/projects/<id>.json, identifying its bound directory via
// projectResources.resources[].gitFolder.folderUri, carrying its own
// allow-list at permissionGrants.permissionGrants.allow — confirmed to
// take precedence over the global settings.json per agy's own changelog.
// A workspace matching an existing project file must get the grant in
// BOTH files, and revert must restore both exactly.
func TestAgyGrant_AlsoGrantsInMatchingProjectFile(t *testing.T) {
	isolateAgySettings(t) // seeds the isolated HOME/USERPROFILE this test also needs
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	projectsDir := filepath.Join(home, ".gemini", "config", "projects")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	workDir := t.TempDir()
	uri, err := vendors.DirToFileURIForTest(workDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	projectFile := filepath.Join(projectsDir, "proj1.json")
	originalProject := []byte(`{
  "id": "proj1",
  "name": "test-project",
  "projectResources": {"resources": [{"gitFolder": {"folderUri": "` + uri + `", "defaultBranch": "master"}}]}
}`)
	if err := os.WriteFile(projectFile, originalProject, 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a := agyGrantAdapter(t)
	revert, err := a.GrantResumePermission(vendors.Vendor{Name: "agy"}, workDir, []vendors.Denial{{ToolName: "command"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	grants, _ := doc["permissionGrants"].(map[string]any)
	inner, _ := grants["permissionGrants"].(map[string]any)
	allow, _ := inner["allow"].([]any)
	if len(allow) != 1 || allow[0] != "command(*)" {
		t.Fatalf("got project file allow %+v, want [\"command(*)\"]", allow)
	}

	if err := revert(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	restored, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(restored) != string(originalProject) {
		t.Fatalf("expected exact byte-for-byte restore of the project file, got %s", restored)
	}
}

// TestAgyGrant_NoMatchingProjectFileIsFineNotAnError proves the common
// case (a scratch/one-off directory never opened interactively) is a
// clean no-op for the project half of the grant, not a failure.
func TestAgyGrant_NoMatchingProjectFileIsFine(t *testing.T) {
	isolateAgySettings(t)
	workDir := t.TempDir()

	a := agyGrantAdapter(t)
	revert, err := a.GrantResumePermission(vendors.Vendor{Name: "agy"}, workDir, []vendors.Denial{{ToolName: "command"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := revert(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAgyGrant_RefusesToModifyUnparsableJSON(t *testing.T) {
	path := isolateAgySettings(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(path, []byte("not valid json"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a := agyGrantAdapter(t)
	_, err := a.GrantResumePermission(vendors.Vendor{Name: "agy"}, "", []vendors.Denial{{ToolName: "command"}})
	if err == nil {
		t.Fatalf("expected an error rather than overwriting a file this package can't parse")
	}
}
