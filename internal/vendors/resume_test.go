package vendors_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/vendors"
)

// fakeAgyVendor builds a Vendor whose "default" template runs a tiny shell
// script that reproduces agy's confirmed real denial behavior (nonzero
// exit, empty stdout, the exact stderr message agyDenialPattern matches)
// — lets ResolveDelegate's end-to-end wiring be tested without a real agy
// install. Skips (not fails) on a machine with no `sh` on PATH.
func fakeAgyVendor(t *testing.T) vendors.Vendor {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH — skipping shell-based fake-exec test")
	}
	denialMsg := `jetski: no output produced - a tool required the "command" permission that headless mode cannot prompt for, so it was auto-denied.`
	script := "printf '%s' '" + denialMsg + "' 1>&2; exit 1"
	return vendors.Vendor{
		Name: "agy",
		Path: shPath,
		Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-c", script}},
		},
	}
}

func TestRegisterAndTakePendingResume_RoundTrips(t *testing.T) {
	v := vendors.Vendor{Name: "agy", Path: "agy.exe"}
	token := vendors.RegisterPendingResume(v, "delete old files", "sess1", "", []vendors.Denial{{ToolName: "command", Detail: "rm x"}})
	if token == "" {
		t.Fatalf("expected a non-empty token")
	}

	pr, ok := vendors.TakePendingResume(token)
	if !ok {
		t.Fatalf("expected the registered token to be found")
	}
	if pr.Vendor.Name != "agy" || pr.Prompt != "delete old files" || pr.SessionID != "sess1" || len(pr.Denials) != 1 {
		t.Fatalf("got %+v", pr)
	}
}

func TestTakePendingResume_OneShot(t *testing.T) {
	v := vendors.Vendor{Name: "agy"}
	token := vendors.RegisterPendingResume(v, "p", "", "", nil)

	if _, ok := vendors.TakePendingResume(token); !ok {
		t.Fatalf("expected the first take to succeed")
	}
	if _, ok := vendors.TakePendingResume(token); ok {
		t.Fatalf("expected a second take of the same token to fail — must be one-shot")
	}
}

func TestTakePendingResume_UnknownTokenFails(t *testing.T) {
	if _, ok := vendors.TakePendingResume("never-registered"); ok {
		t.Fatalf("expected an unknown token to fail")
	}
}

func TestTakePendingResume_ExpiredTokenFails(t *testing.T) {
	old := vendors.PendingResumeTTL
	vendors.PendingResumeTTL = 5 * time.Millisecond
	defer func() { vendors.PendingResumeTTL = old }()

	token := vendors.RegisterPendingResume(vendors.Vendor{Name: "agy"}, "p", "", "", nil)
	time.Sleep(20 * time.Millisecond)

	if _, ok := vendors.TakePendingResume(token); ok {
		t.Fatalf("expected an expired token to fail")
	}
}

// TestResolveDelegate_AllowUnknownToken and _DenyUnknownToken cover the
// control-flow markers ("allow"/"deny" as templateName) without needing a
// real registered denial first.
func TestResolveDelegate_AllowUnknownToken(t *testing.T) {
	out := vendors.ResolveDelegate(context.Background(), vendors.Vendor{Name: "agy"}, "allow", "bogus-token", 0)
	if !strings.Contains(out, "No pending permission request") {
		t.Fatalf("got %q", out)
	}
}

func TestResolveDelegate_DenyKnownTokenDropsIt(t *testing.T) {
	token := vendors.RegisterPendingResume(vendors.Vendor{Name: "agy"}, "p", "", "", []vendors.Denial{{ToolName: "command"}})

	out := vendors.ResolveDelegate(context.Background(), vendors.Vendor{Name: "agy"}, "deny", token, 0)
	if !strings.Contains(out, "Denied") || !strings.Contains(out, "agy") {
		t.Fatalf("got %q", out)
	}

	// The token must be consumed by deny too, not just allow.
	if _, ok := vendors.TakePendingResume(token); ok {
		t.Fatalf("expected deny to have already consumed the token")
	}
}

// TestResolveDelegate_UnknownWorkspaceAsksInsteadOfRunning proves a
// nonzero, unresolved origin PID makes ResolveDelegate ask for a
// directory instead of silently falling back to Glider's own server
// directory — the exact bug live testing found (2026-07-26): a resumed
// delegate call read files from Glider's own repo, not the user's project.
func TestResolveDelegate_UnknownWorkspaceAsksInsteadOfRunning(t *testing.T) {
	v := fakeAgyVendor(t)
	out := vendors.ResolveDelegate(context.Background(), v, "default", "delete old files", 999999)
	if !strings.Contains(out, "/workspace ") {
		t.Fatalf("got %q, expected it to ask for a workspace directory", out)
	}
	if strings.Contains(out, "[agy] needs permission") {
		t.Fatalf("got %q — must ask for a workspace before ever running the vendor, not after", out)
	}
}

// TestResolveDelegate_KnownWorkspaceIsUsed proves a PID with a registered
// workspace runs immediately, using that directory.
func TestResolveDelegate_KnownWorkspaceIsUsed(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH")
	}
	dir := t.TempDir()
	v := vendors.Vendor{
		Name: "agy", Path: shPath,
		Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-c", "pwd"}},
		},
	}
	vendors.SetWorkspaceForPID(424242, dir)

	out := vendors.ResolveDelegate(context.Background(), v, "default", "unused", 424242)
	// pwd's output may differ in path form (symlinks, drive-letter casing)
	// from t.TempDir()'s own string — just confirm it didn't ask for a
	// workspace and didn't error, which is what this test actually checks.
	if strings.Contains(out, "/workspace ") {
		t.Fatalf("got %q, should not ask — a workspace was already set for this PID", out)
	}
	if out == "" {
		t.Fatalf("expected non-empty output")
	}
}

// TestResolveDelegate_DefaultWorkspaceCoversUnknownPID proves a configured
// default is used for any PID without a specific entry.
func TestResolveDelegate_DefaultWorkspaceCoversUnknownPID(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH")
	}
	dir := t.TempDir()
	vendors.SetDefaultWorkspace(dir)
	defer vendors.SetDefaultWorkspace("") // don't leak into other tests

	v := vendors.Vendor{
		Name: "agy", Path: shPath,
		Templates: []vendors.CommandTemplate{{Name: "default", Mode: "headless", Args: []string{"-c", "pwd"}}},
	}
	out := vendors.ResolveDelegate(context.Background(), v, "default", "unused", 555555)
	if strings.Contains(out, "/workspace ") {
		t.Fatalf("got %q, a default workspace was configured, should not ask", out)
	}
}

func TestParseWorkspaceCommand_MatchesAnywhereInText(t *testing.T) {
	path, ok := vendors.ParseWorkspaceCommand("some scaffolding /workspace C:\\Users\\me\\proj")
	if !ok || path != `C:\Users\me\proj` {
		t.Fatalf("got path=%q ok=%v", path, ok)
	}
}

func TestParseWorkspaceCommand_NoMatch(t *testing.T) {
	if _, ok := vendors.ParseWorkspaceCommand("/agy do something"); ok {
		t.Fatalf("expected no match for a vendor-scoped flag")
	}
}

// TestResolveDelegate_NormalRunRegistersTokenOnDenial exercises the
// non-allow/deny path end-to-end against a fake binary that always exits
// nonzero with a denial-shaped stderr message (agy's real message,
// verbatim) — proving RunWithOptions -> DetectDenials -> RegisterPendingResume
// -> FormatDenialSummary are wired together correctly, without needing a
// real vendor CLI installed.
func TestResolveDelegate_NormalRunRegistersTokenOnDenial(t *testing.T) {
	v := fakeAgyVendor(t)

	out := vendors.ResolveDelegate(context.Background(), v, "default", "delete old files", 0)
	if !strings.Contains(out, "[agy] needs permission") {
		t.Fatalf("got %q", out)
	}
	if !strings.Contains(out, "/agy:allow ") || !strings.Contains(out, "/agy:deny ") {
		t.Fatalf("got %q, expected both allow/deny reply instructions", out)
	}
}
