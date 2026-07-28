package vendors_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

// TestRegisterPendingResume_SweepsExpiredEntriesOnRegister is the direct
// regression test for a real finding from the 2026-07-28 security/
// reliability audit: a denial the human never answers used to sit in
// defaultResumeStore's map forever (only TakePendingResume ever deleted
// an entry, and only for the specific token someone actually replied
// to) — unbounded growth on a service meant to run for days/weeks.
// Checks the DELTA in PendingResumeCount(), not an absolute value, since
// the store is shared global state across every test in this file.
func TestRegisterPendingResume_SweepsExpiredEntriesOnRegister(t *testing.T) {
	old := vendors.PendingResumeTTL
	vendors.PendingResumeTTL = 5 * time.Millisecond
	defer func() { vendors.PendingResumeTTL = old }()

	before := vendors.PendingResumeCount()
	vendors.RegisterPendingResume(vendors.Vendor{Name: "agy"}, "p1", "", "", nil)
	afterFirst := vendors.PendingResumeCount()
	if afterFirst != before+1 {
		t.Fatalf("expected count to grow by 1, got before=%d after=%d", before, afterFirst)
	}

	time.Sleep(20 * time.Millisecond) // let the first entry expire

	// A second registration should sweep the now-expired first entry away
	// as a side effect — net count stays the same (one expired removed,
	// one new added) instead of growing unboundedly.
	vendors.RegisterPendingResume(vendors.Vendor{Name: "agy"}, "p2", "", "", nil)
	afterSecond := vendors.PendingResumeCount()
	if afterSecond != afterFirst {
		t.Fatalf("expected the expired entry to be swept (count unchanged at %d), got %d — expired entries are leaking", afterFirst, afterSecond)
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
	if !strings.Contains(out, "/workspace") {
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
	if strings.Contains(out, "/workspace") {
		t.Fatalf("got %q, a default workspace was configured, should not ask", out)
	}
}

func TestParseWorkspaceCommand_MatchesTrailingFlag(t *testing.T) {
	path, ok := vendors.ParseWorkspaceCommand(`C:\Users\me\proj /workspace`)
	if !ok || path != `C:\Users\me\proj` {
		t.Fatalf("got path=%q ok=%v", path, ok)
	}
}

func TestParseWorkspaceCommand_LeadingFlagNoMatch(t *testing.T) {
	if _, ok := vendors.ParseWorkspaceCommand(`/workspace C:\Users\me\proj`); ok {
		t.Fatalf("expected no match — the old leading-flag form is no longer accepted")
	}
}

func TestParseWorkspaceCommand_NoMatch(t *testing.T) {
	if _, ok := vendors.ParseWorkspaceCommand("do something /agy"); ok {
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
	if !strings.Contains(out, "/agy:allow") || !strings.Contains(out, "/agy:deny") {
		t.Fatalf("got %q, expected both allow/deny reply instructions", out)
	}
}

// TestResolveDelegate_InteractiveTemplateDispatchesToLaunchInteractive
// proves ResolveDelegate recognizes Mode == "interactive" and dispatches
// to LaunchInteractiveFunc (with {{prompt}}/{{cwd}} substituted into the
// template's own args) instead of ever calling RunWithOptions — swapping
// LaunchInteractiveFunc for a recording stub instead of letting a real
// detached OS console window open during a test run.
func TestResolveDelegate_InteractiveTemplateDispatchesToLaunchInteractive(t *testing.T) {
	var gotVendor vendors.Vendor
	var gotCwd string
	var gotArgs []string
	orig := vendors.LaunchInteractiveFunc
	vendors.LaunchInteractiveFunc = func(v vendors.Vendor, cwd string, extraArgs ...string) error {
		gotVendor, gotCwd, gotArgs = v, cwd, extraArgs
		return nil
	}
	defer func() { vendors.LaunchInteractiveFunc = orig }()

	v := vendors.Vendor{
		Name: "claude",
		Templates: []vendors.CommandTemplate{
			{Name: "interactive", Mode: "interactive", Args: []string{"{{prompt}}"}},
		},
	}
	out := vendors.ResolveDelegate(context.Background(), v, "interactive", "fix the auth bug", 0)

	if gotVendor.Name != "claude" {
		t.Fatalf("expected LaunchInteractiveFunc to be called with the claude vendor, got %+v", gotVendor)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "fix the auth bug" {
		t.Fatalf("expected substituted args [%q], got %v", "fix the auth bug", gotArgs)
	}
	if gotCwd != "" {
		t.Fatalf("expected empty cwd for an unresolvable origin PID, got %q", gotCwd)
	}
	if !strings.Contains(out, "Opened claude in a new interactive window") {
		t.Fatalf("got %q, expected a confirmation reply, not captured output", out)
	}
	if strings.Contains(out, "fix the auth bug") {
		t.Fatalf("got %q — the reply must not echo the prompt back as if it were a captured answer", out)
	}
}

// TestResolveDelegate_InteractiveTemplateUsesKnownWorkspace is the
// interactive-launch counterpart to TestResolveDelegate_KnownWorkspaceIsUsed
// (the headless case) — a real gap until now: every other interactive-mode
// test in this file uses an unresolvable PID (0) specifically so it can
// assert an EMPTY cwd, which never exercised whether a KNOWN workspace
// actually reaches LaunchInteractiveFunc at all.
func TestResolveDelegate_InteractiveTemplateUsesKnownWorkspace(t *testing.T) {
	var gotCwd string
	orig := vendors.LaunchInteractiveFunc
	vendors.LaunchInteractiveFunc = func(v vendors.Vendor, cwd string, extraArgs ...string) error {
		gotCwd = cwd
		return nil
	}
	defer func() { vendors.LaunchInteractiveFunc = orig }()

	dir := t.TempDir()
	vendors.SetWorkspaceForPID(313131, dir)

	v := vendors.Vendor{
		Name:      "agy",
		Templates: []vendors.CommandTemplate{{Name: "interactive", Mode: "interactive", Args: []string{"{{prompt}}"}}},
	}
	out := vendors.ResolveDelegate(context.Background(), v, "interactive", "task", 313131)

	if gotCwd != dir {
		t.Fatalf("got cwd %q, want the registered workspace %q", gotCwd, dir)
	}
	if !strings.Contains(out, dir) {
		t.Fatalf("got %q, expected the confirmation reply to name the directory it opened in", out)
	}
}

// TestResolveDelegate_InteractiveTemplateSurfacesLaunchFailure proves a
// LaunchInteractiveFunc error becomes a plain reply, not a panic or a
// silently-empty response.
func TestResolveDelegate_InteractiveTemplateSurfacesLaunchFailure(t *testing.T) {
	orig := vendors.LaunchInteractiveFunc
	vendors.LaunchInteractiveFunc = func(v vendors.Vendor, cwd string, extraArgs ...string) error {
		return fmt.Errorf("boom")
	}
	defer func() { vendors.LaunchInteractiveFunc = orig }()

	v := vendors.Vendor{
		Name:      "agy",
		Templates: []vendors.CommandTemplate{{Name: "interactive", Mode: "interactive", Args: []string{"{{prompt}}"}}},
	}
	out := vendors.ResolveDelegate(context.Background(), v, "interactive", "task", 0)
	if !strings.Contains(out, "Could not open agy interactively") || !strings.Contains(out, "boom") {
		t.Fatalf("got %q", out)
	}
}

// TestResolveDelegate_InteractiveTemplateUnknownWorkspaceAsksInsteadOfLaunching
// mirrors the headless case (TestResolveDelegate_UnknownWorkspaceAsksInsteadOfRunning):
// an unresolvable workspace for a known origin PID must ask, not launch
// into an arbitrary directory.
func TestResolveDelegate_InteractiveTemplateUnknownWorkspaceAsksInsteadOfLaunching(t *testing.T) {
	called := false
	orig := vendors.LaunchInteractiveFunc
	vendors.LaunchInteractiveFunc = func(v vendors.Vendor, cwd string, extraArgs ...string) error {
		called = true
		return nil
	}
	defer func() { vendors.LaunchInteractiveFunc = orig }()

	v := vendors.Vendor{
		Name:      "claude",
		Templates: []vendors.CommandTemplate{{Name: "interactive", Mode: "interactive", Args: []string{"{{prompt}}"}}},
	}
	out := vendors.ResolveDelegate(context.Background(), v, "interactive", "task", 777777)
	if called {
		t.Fatalf("expected LaunchInteractiveFunc NOT to be called for an unresolvable workspace")
	}
	if !strings.Contains(out, "/workspace") {
		t.Fatalf("got %q, expected the ask-for-workspace reply", out)
	}
}

// fakeClaudeVendor builds a Vendor whose "default" template prints a
// realistic multi-line stream-json transcript (system/init, one assistant
// delta, a terminal result line) — enough to exercise
// ngl.DelegateRenderer's real parsing through ResolveDelegate end-to-end,
// without a real claude install.
func fakeClaudeVendor(t *testing.T) vendors.Vendor {
	t.Helper()
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH — skipping shell-based fake-exec test")
	}
	transcript := `{"type":"system","subtype":"init","session_id":"s1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"working on it..."}]}}
{"type":"result","subtype":"success","is_error":false,"result":"Fixed the off-by-one in the loop bound.","session_id":"s1"}`
	script := "printf '%s' '" + transcript + "'"
	return vendors.Vendor{
		Name: "claude",
		Path: shPath,
		Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-c", script}},
		},
	}
}

// TestResolveDelegate_CleanModeRendersJustTheResult and
// TestResolveDelegate_RawModeReturnsFullTranscript are the direct
// end-to-end tests for the response-detail feature (2026-07-28): by
// default, ResolveDelegate must show only the vendor's final answer, not
// its raw stream-json transcript — reversible via
// vendors.SetResponseDetail(vendors.ResponseDetailRaw) for debugging.
func TestResolveDelegate_CleanModeRendersJustTheResult(t *testing.T) {
	vendors.SetResponseDetail(vendors.ResponseDetailClean)
	v := fakeClaudeVendor(t)

	out := vendors.ResolveDelegate(context.Background(), v, "default", "fix the loop bug", 0)
	if out != "Fixed the off-by-one in the loop bound." {
		t.Fatalf("got %q, want just the clean final answer", out)
	}
}

func TestResolveDelegate_RawModeReturnsFullTranscript(t *testing.T) {
	vendors.SetResponseDetail(vendors.ResponseDetailRaw)
	defer vendors.SetResponseDetail(vendors.ResponseDetailClean) // don't leak into other tests
	v := fakeClaudeVendor(t)

	out := vendors.ResolveDelegate(context.Background(), v, "default", "fix the loop bug", 0)
	if !strings.Contains(out, `"type":"system"`) || !strings.Contains(out, `"type":"result"`) {
		t.Fatalf("got %q, want the full raw stream-json transcript in raw mode", out)
	}
}

// TestResolveDelegate_CleanModeFallsBackWithNoteOnUnparsableOutput proves
// the "renderer exists but declined" case appends a visible note rather
// than silently showing the raw text as if nothing were unusual.
func TestResolveDelegate_CleanModeFallsBackWithNoteOnUnparsableOutput(t *testing.T) {
	vendors.SetResponseDetail(vendors.ResponseDetailClean)
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH")
	}
	// Not valid stream-json at all — claudeDelegateRenderer must decline,
	// not guess.
	v := vendors.Vendor{
		Name: "claude", Path: shPath,
		Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-c", "printf 'plain unstructured output'"}},
		},
	}
	out := vendors.ResolveDelegate(context.Background(), v, "default", "task", 0)
	if !strings.Contains(out, "plain unstructured output") {
		t.Fatalf("got %q, expected the raw text preserved", out)
	}
	if !strings.Contains(out, "couldn't parse a clean result") {
		t.Fatalf("got %q, expected a visible fallback note", out)
	}
}

// TestResolveDelegate_RealConnectionPIDDrivesRealWorkingDirectory answers a
// real, direct question about how workspace-directory allocation actually
// works: if a human once tells Glider "this origin process's project is
// <dir>" (the /workspace flow, or a dashboard-configured default), does a
// LATER delegate call from that SAME real OS process automatically run in
// <dir> — not Glider's own directory — with no further action needed?
//
// Unlike every other test in this file, this one does NOT use a synthetic
// PID constant: it opens a real TCP connection to a real local listener
// (so the "origin process" is this go test binary's own OS process, the
// same way a real CLI's connection would be) and resolves its PID through
// the exact same vendors.ResolveOriginPID(r.RemoteAddr) codepath
// DelegateHandler itself uses — proving the full, real pipeline (live
// connection -> real OS port/PID lookup -> registered workspace -> actual
// subprocess cwd), not just that a manually-supplied PID constant works.
//
// What this can't and doesn't claim: Glider cannot discover a brand-new
// origin process's real directory with zero prior information — an
// earlier, different approach (reading it directly out of the origin
// process's own memory) was tried and abandoned, confirmed live to be
// blocked by Windows Defender's default cross-process memory-read
// protection (see workspace.go's own doc comment). "Automatic" here means
// "remembered once, applied automatically to every later call from that
// same process," not "detected out of thin air."
func TestResolveDelegate_RealConnectionPIDDrivesRealWorkingDirectory(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer ln.Close()

	// The "server" side sees this test process's own outbound connection —
	// its real OS-assigned ephemeral local port, owned by os.Getpid(),
	// resolvable via the real procinfo lookup exactly like a genuine CLI's
	// connection to Glider's own MITM listener would be.
	var serverConn net.Conn
	accepted := make(chan struct{})
	go func() {
		serverConn, _ = ln.Accept()
		close(accepted)
	}()
	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer clientConn.Close()
	<-accepted
	defer serverConn.Close()

	remoteAddr := serverConn.RemoteAddr().String() // this test process's own address, from the listener's side
	pid := vendors.ResolveOriginPID(remoteAddr)
	if pid == 0 {
		t.Skip("could not resolve this test process's own PID from a real connection — procinfo lookup unavailable in this environment")
	}
	if pid != uint32(os.Getpid()) {
		t.Fatalf("resolved PID %d, want this test process's own PID %d — real port/PID lookup didn't find itself", pid, os.Getpid())
	}

	dir := t.TempDir()
	marker := "workspace-marker-9c2e1b.txt"
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("found-me"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The human "teaches" Glider this origin process's real project
	// directory once — the /workspace flow's own effect, replicated
	// directly here since parsing that flag isn't what this test is about.
	vendors.SetWorkspaceForPID(pid, dir)

	v := vendors.Vendor{
		Name: "fake", Path: shPath,
		Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-c", "cat " + marker}},
		},
	}
	// No cwd passed explicitly anywhere here — ResolveDelegate must derive
	// it purely from pid, the same way it would from a real HTTP request's
	// RemoteAddr inside DelegateHandler.
	out := vendors.ResolveDelegate(context.Background(), v, "default", "unused", pid)
	if out != "found-me" {
		t.Fatalf("got %q, want the marker file's content — the headless run must have executed with dir as its real OS working directory, not Glider's own", out)
	}
}
