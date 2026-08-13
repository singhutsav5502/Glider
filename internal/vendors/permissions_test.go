package vendors_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glider-ai/glider/internal/vendors"
)

func TestClaudeTemplatesForPreset_TrustSessionAddsFlag(t *testing.T) {
	templates := []vendors.CommandTemplate{
		{Name: "default", Mode: "headless", Args: []string{"-p", "--output-format", "stream-json", "{{prompt}}"}},
	}
	out, err := vendors.ClaudeTemplatesForPreset(templates, "trust_session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Args[0] != "-p" || out[0].Args[1] != "--dangerously-skip-permissions" {
		t.Fatalf("got args %v, want the skip flag right after -p", out[0].Args)
	}
}

func TestClaudeTemplatesForPreset_Idempotent(t *testing.T) {
	templates := []vendors.CommandTemplate{
		{Name: "default", Mode: "headless", Args: []string{"-p", "{{prompt}}"}},
	}
	once, err := vendors.ClaudeTemplatesForPreset(templates, "trust_session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	twice, err := vendors.ClaudeTemplatesForPreset(once, "trust_session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(twice[0].Args) != len(once[0].Args) {
		t.Fatalf("applying the same preset twice should be a no-op, got %v then %v", once[0].Args, twice[0].Args)
	}
}

func TestClaudeTemplatesForPreset_AskRemovesFlag(t *testing.T) {
	templates := []vendors.CommandTemplate{
		{Name: "default", Mode: "headless", Args: []string{"-p", "--dangerously-skip-permissions", "{{prompt}}"}},
	}
	out, err := vendors.ClaudeTemplatesForPreset(templates, "ask")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range out[0].Args {
		if a == "--dangerously-skip-permissions" {
			t.Fatalf("got args %v, expected the skip flag removed", out[0].Args)
		}
	}
}

func TestCurrentPermissionPreset_Claude(t *testing.T) {
	v := vendors.Vendor{Name: "claude", Templates: []vendors.CommandTemplate{
		{Name: "default", Mode: "headless", Args: []string{"-p", "{{prompt}}"}},
	}}
	id, err := vendors.CurrentPermissionPreset(v, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "ask" {
		t.Fatalf("got %q, want \"ask\" for a plain template with no skip flag", id)
	}
}

// TestApplyPermissionPreset_AgyWritesAndRoundTrips proves a preset applied
// to a directory with no existing agy project file creates one, and that
// CurrentPermissionPreset reads back the same preset id — isolated to a
// temp HOME so it never touches the real ~/.gemini on this machine.
func TestApplyPermissionPreset_AgyWritesAndRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	workDir := filepath.Join(t.TempDir(), "myproject")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	v := vendors.Vendor{Name: "agy"}
	if err := vendors.ApplyPermissionPreset(v, workDir, "trust_folder"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id, err := vendors.CurrentPermissionPreset(v, workDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "trust_folder" {
		t.Fatalf("got %q, want \"trust_folder\"", id)
	}

	// A different, unrelated directory must not pick up the same preset.
	otherDir := filepath.Join(t.TempDir(), "other")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	id, err = vendors.CurrentPermissionPreset(v, otherDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Fatalf("got %q, want no match for an unrelated directory", id)
	}
}

func TestApplyPermissionPreset_CursorAgentRejected(t *testing.T) {
	v := vendors.Vendor{Name: "cursor-agent"}
	if err := vendors.ApplyPermissionPreset(v, t.TempDir(), "ask"); err == nil {
		t.Fatalf("expected an error — cursor-agent has no settable preset")
	}
}
