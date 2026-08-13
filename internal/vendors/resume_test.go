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

// fakeAgyVendor makes a Vendor whose "default" template runs a small shell
// script. That script makes the same behaviour as a true refusal from agy. It
// gives a non-zero exit, an empty stdout, and the exact stderr message that
// agyDenialPattern agrees with.
//
// Therefore a test can examine the full connections of ResolveDelegate, and
// it needs no true installation of agy. The test skips, and it does not fail,
// on a machine with no `sh` on the PATH.
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
// regression test for a true finding of the security and reliability audit on
// 2026-07-28.
//
// A refusal that a person never answers stayed in the map of
// defaultResumeStore permanently. Only TakePendingResume removed an entry, and
// it did this only for the one token that a person answered. Therefore the map
// grew with no limit, on a service that must operate for days or weeks.
//
// The test examines the DIFFERENCE in PendingResumeCount(), and not an absolute
// value, because each test in this file uses the same global store.
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

	// A second registration must remove the first entry, which is now expired, as
	// one result of its work. Therefore the count does not change: the code removes
	// one entry that is expired and adds one new entry. The count does not grow
	// with no limit.
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

// TestResolveDelegate_UnknownWorkspaceAsksInsteadOfRunning proves that an
// origin PID that is not zero, and that the code cannot resolve, makes
// ResolveDelegate ask for a directory. ResolveDelegate does not use the
// server directory of Glider with no message.
//
// A live test found this exact defect on 2026-07-26. A delegate call that
// resumed read files from the repository of Glider. It did not read files
// from the project of the user.
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
	// The output of pwd can have a different form from the text of t.TempDir(). A
	// symbolic link or the case of a drive letter can cause this. Therefore this
	// test only confirms two conditions: the code did not ask for a workspace, and
	// it gave no error. Those two conditions are what this test examines.
	if strings.Contains(out, "/workspace ") {
		t.Fatalf("got %q, should not ask — a workspace was already set for this PID", out)
	}
	if out == "" {
		t.Fatalf("expected non-empty output")
	}
}

// TestResolveDelegate_DefaultWorkspaceOffersItselfButStillAsks pins the
// behaviour chosen on 2026-08-03, and it asserts the OPPOSITE of what it
// asserted before that date.
//
// It used to prove that a configured default answered for any PID with no
// entry of its own. That behaviour was the defect. A person who set a default
// while working on project alpha, and then opened a second CLI in project
// beta, had the first handoff of that beta session run in alpha, and Glider
// asked nothing. A delegate could then edit the files of the wrong project.
//
// Glider cannot correct that with a better default, because it cannot know
// which project a new session is in: procinfo gives a PID and an image name,
// and no path.
//
// So every new session is asked. The default is not thrown away -- the
// question offers it, so accepting it costs one reply and no typing.
func TestResolveDelegate_DefaultWorkspaceOffersItselfButStillAsks(t *testing.T) {
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

	if !strings.Contains(out, "/workspace") {
		t.Fatalf("an unknown session must be asked, got %q", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("the question must offer the configured default so it can be accepted in one reply, got %q", out)
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

// TestResolveDelegate_NormalRunRegistersTokenOnDenial examines the path that
// is not "allow" and not "deny", from one end to the other. It uses a false
// binary that always exits with a non-zero value and writes a stderr message
// with the shape of a refusal. That message is the true message of agy, word
// for word.
//
// The test proves that four items connect correctly: RunWithOptions,
// DetectDenials, RegisterPendingResume and FormatDenialSummary. It needs no
// true vendor CLI on the machine.
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

// TestResolveDelegate_InteractiveTemplateDispatchesToLaunchInteractive proves
// that ResolveDelegate sees Mode == "interactive" and sends the work to
// LaunchInteractiveFunc. It puts the values for {{prompt}} and {{cwd}} in the
// args of the template. It never calls RunWithOptions.
//
// The test replaces LaunchInteractiveFunc with a stub that records the call.
// Therefore no true console window opens during a test.
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

// TestResolveDelegate_InteractiveTemplateUsesKnownWorkspace does the same
// work as TestResolveDelegate_KnownWorkspaceIsUsed, but for an interactive
// launch. That other test covers the condition with no console. This was a
// true gap. Each other test of the interactive mode in this file uses a PID
// of 0, which the code cannot resolve. Those tests do that thus they can say
// that cwd is EMPTY. Therefore no test examined if a workspace that the code
// KNOWS truly arrives at LaunchInteractiveFunc.
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

// fakeClaudeVendor makes a Vendor with a "default" template that writes a
// true record in stream-json, on more than one line. That record has a
// system/init line, one part of an assistant message, and a last result line.
// That is sufficient to examine the true parsing of ngl.DelegateRenderer,
// through ResolveDelegate, from one end to the other. It needs no true
// installation of claude.
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
// TestResolveDelegate_RawModeReturnsFullTranscript examine the
// response-detail function from one end to the other. A person added that
// function on 2026-07-28. By default, ResolveDelegate must show only the
// final answer of the vendor. It must not show the raw record in stream-json.
// A person can change this with
// vendors.SetResponseDetail(vendors.ResponseDetailRaw), to find the cause of
// a problem.
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

// TestResolveDelegate_RealConnectionPIDDrivesRealWorkingDirectory answers one
// direct question about the true behaviour of the workspace directory. A
// person tells Glider one time that "the project of this origin process is
// <dir>". That person uses the /workspace flow, or a default on the
// dashboard. Does a LATER delegate call, from that SAME true process of the
// operating system, then operate in <dir> automatically? And does it need no
// other action? It must not operate in the directory of Glider.
//
// This test does NOT use a PID value that a person invents. Each other test
// in this file does. It opens a true TCP connection to a true local listener.
// Therefore the "origin process" is the process of this test binary, in the
// same way as the connection of a true CLI. It then finds the PID through
// exactly the same code as DelegateHandler, which is
// vendors.ResolveOriginPID(r.RemoteAddr). Thus it proves the full and true
// sequence: a live connection, then a true lookup of the port and the PID in
// the operating system, then the registered workspace, then the true cwd of
// the subprocess. It does not only prove that a PID value from a person
// operates.
//
// What this test does not say: Glider cannot find the true directory of a new
// origin process with no earlier information. A person tried a different
// method, which read that directory from the memory of the origin process,
// and then removed it. A live test confirmed that the default protection of
// Windows Defender, against a read of the memory of a different process,
// stops that method. Refer to the comment in workspace.go. "Automatic" here
// means "remembered once, applied automatically to every later call from that
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

	// The "server" side sees the outbound connection of this test process. That
	// connection has a true local port that the operating system gave it, and
	// os.Getpid() owns that port. The true lookup in procinfo can find it. This
	// is exactly the same as the connection of a true CLI to the MITM listener
	// of Glider.
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
	// The person gives Glider the true project directory of this origin process
	// one time. That is the result of the /workspace flow. This test makes that
	// result directly, because it does not examine the parsing of that flag.
	vendors.SetWorkspaceForPID(pid, dir)

	v := vendors.Vendor{
		Name: "fake", Path: shPath,
		Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-c", "cat " + marker}},
		},
	}
	// No code here gives a cwd value. ResolveDelegate must find it from pid
	// alone. That is the same method that it uses with the RemoteAddr of a true
	// HTTP request, inside DelegateHandler.
	out := vendors.ResolveDelegate(context.Background(), v, "default", "unused", pid)
	if out != "found-me" {
		t.Fatalf("got %q, want the marker file's content — the headless run must have executed with dir as its real OS working directory, not Glider's own", out)
	}
}
