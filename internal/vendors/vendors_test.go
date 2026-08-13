package vendors_test

import (
	"context"
	"fmt"
	"github.com/glider-ai/glider/internal/vendors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestRun_RejectsOverlongPrompt is the regression test for the second
// protection against the live defect from 2026-07-26.
//
// An incorrect extraction made a "prompt" of some hundred KB of collected
// context, and not a short and true instruction from a person. Such a prompt
// must fail with a clear error that a person can read, before the code arrives
// at exec.CommandContext. It must not appear as an error from the operating
// system that gives no information: "filename or extension is too long".
func TestRun_RejectsOverlongPrompt(t *testing.T) {
	v := vendors.Vendor{Name: "fake", Path: "fake.exe", PrintFlag: "-p", Enabled: true}
	overlong := strings.Repeat("a", vendors.MaxPromptLen+1)

	_, err := vendors.Run(context.Background(), v, overlong)
	if err == nil {
		t.Fatalf("expected an error for an overlong prompt, got nil")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Fatalf("got error %q, want a clear \"too long\" message rather than an OS-level failure", err.Error())
	}
}

func TestParseDelegateCommand_MatchesEnabledVendorFlag(t *testing.T) {
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Path: "agy.exe", PrintFlag: "-p", Enabled: true},
		{Name: "cursor-agent", Path: "cursor-agent.cmd", PrintFlag: "-p", Enabled: false},
	}}

	v, tmpl, prompt, ok := vendors.ParseDelegateCommand(reg, "please handle this: reply with pong /agy")
	if !ok {
		t.Fatalf("expected a match for an enabled vendor's flag")
	}
	if v.Name != "agy" || tmpl != "default" || prompt != "please handle this: reply with pong" {
		t.Fatalf("got vendor=%q template=%q prompt=%q", v.Name, tmpl, prompt)
	}
}

func TestParseDelegateCommand_IgnoresDisabledVendorFlag(t *testing.T) {
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "cursor-agent", Path: "cursor-agent.cmd", PrintFlag: "-p", Enabled: false},
	}}

	_, _, _, ok := vendors.ParseDelegateCommand(reg, "do something /cursor-agent")
	if ok {
		t.Fatalf("expected no match — the only vendor with this flag is disabled")
	}
}

// TestParseDelegateCommand_TemplateSuffix matches the ":<template>"
// syntax — e.g. "/agy:interactive" selects a named CommandTemplate other
// than "default" (planning/permission_relay_design.md §1).
func TestParseDelegateCommand_TemplateSuffix(t *testing.T) {
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Path: "agy.exe", PrintFlag: "-p", Enabled: true},
	}}

	v, tmpl, prompt, ok := vendors.ParseDelegateCommand(reg, "fix the auth bug /agy:interactive")
	if !ok {
		t.Fatalf("expected a match")
	}
	if v.Name != "agy" || tmpl != "interactive" || prompt != "fix the auth bug" {
		t.Fatalf("got vendor=%q template=%q prompt=%q", v.Name, tmpl, prompt)
	}
}

// TestParseDelegateCommand_RequiresTrailingPosition proves that a flag at the
// start no longer agrees. That was the old convention, and a person refused
// it.
//
// The flag must be the last token in the message, and not the first. Some
// CLIs read a "/" at the start as their own command syntax. With a flag at
// the end, a person who types in such a CLI always sends the message. That
// CLI never takes the message before Glider sees it.
func TestParseDelegateCommand_RequiresTrailingPosition(t *testing.T) {
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Path: "agy.exe", PrintFlag: "-p", Enabled: true},
	}}

	_, _, _, ok := vendors.ParseDelegateCommand(reg, "/agy do something")
	if ok {
		t.Fatalf("expected no match — a leading flag is not a trailing flag")
	}
}

// TestParseDelegateCommand_BareFlagNoMatch proves a flag with nothing in
// front of it (no prompt to run) does not match.
func TestParseDelegateCommand_BareFlagNoMatch(t *testing.T) {
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Path: "agy.exe", PrintFlag: "-p", Enabled: true},
	}}

	_, _, _, ok := vendors.ParseDelegateCommand(reg, "/agy")
	if ok {
		t.Fatalf("expected no match — a bare flag has no prompt")
	}
}

// TestRunWithOptions_SubstitutesCwd proves that {{cwd}} gives the true work
// directory.
//
// A person added it on 2026-07-26, after a live test. That test found this:
// with no explicit --add-dir={{cwd}}, the -p mode of agy operates in a
// different position. That position has no relation to the workspace. Refer
// to the "default" template of agy in configs/vendor_candidates.yaml.
//
// The test uses the pattern with a false binary and a shell script, from
// fakeAgyVendor in resume_test.go. Therefore it needs no true CLI.
func TestRunWithOptions_SubstitutesCwd(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH — skipping shell-based fake-exec test")
	}
	v := vendors.Vendor{
		Name: "fake", Path: shPath,
		Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-c", "printf '%s' \"$1\"", "_", "{{cwd}}"}},
		},
	}
	wantCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	res, err := vendors.RunWithOptions(context.Background(), v, "unused", vendors.RunOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != wantCwd {
		t.Fatalf("got %q, want the real cwd %q", res.Text, wantCwd)
	}
}

// TestRunWithOptions_CwdSetsRealProcessDirectory is the regression test for a
// true defect that a live test found on 2026-07-26.
//
// The value for {{cwd}} changes only an ARGUMENT STRING, which is what
// --add-dir={{cwd}} of agy needs. It did NOT change the true work directory
// of the new process, which is cmd.Dir. Therefore each vendor with no
// explicit directory flag in its template continued to operate in the server
// directory of Glider, whatever value RunOptions.Cwd had. claude and
// cursor-agent have no such flag, and a person confirmed this.
//
// The test reads a file with a RELATIVE path. It does not compare the raw
// text that pwd writes. On Windows, sh.exe writes its own view of a path, in
// the form of POSIX. The text of t.TempDir() has the form of Windows. Those
// two texts do not compare cleanly. The behaviour is important here, and the
// identity of two strings is not.
func TestRunWithOptions_CwdSetsRealProcessDirectory(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH — skipping shell-based fake-exec test")
	}
	dir := t.TempDir()
	marker := "marker-8f3c1a.txt"
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("found-me"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v := vendors.Vendor{
		Name: "fake", Path: shPath,
		Templates: []vendors.CommandTemplate{
			{Name: "default", Mode: "headless", Args: []string{"-c", "cat " + marker}},
		},
	}
	res, err := vendors.RunWithOptions(context.Background(), v, "unused", vendors.RunOptions{Cwd: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Text != "found-me" {
		t.Fatalf("got %q — a relative-path read of the marker file only succeeds if cmd.Dir was really set to RunOptions.Cwd, not just substituted into a template argument", res.Text)
	}
}

// TestLoadCandidates_RealConfigParsesWithTemplates examines the true
// configs/vendor_candidates.yaml that this repository ships. Each candidate
// must give a "default" template and a "resume" template, and each must have
// {{prompt}} in its Args. RunWithOptions needs both of them for Path A.
func TestLoadCandidates_RealConfigParsesWithTemplates(t *testing.T) {
	candidates, err := vendors.LoadCandidates("../../configs/vendor_candidates.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3", len(candidates))
	}
	for _, c := range candidates {
		v := vendors.Vendor{Name: c.Name, PrintFlag: c.PrintFlag, Templates: c.Templates}
		def, ok := v.ResolveTemplate("default")
		if !ok {
			t.Fatalf("%s: expected a default template", c.Name)
		}
		if !containsPlaceholder(def.Args, "{{prompt}}") {
			t.Fatalf("%s: default template %+v missing {{prompt}}", c.Name, def.Args)
		}
		resume, ok := v.ResolveTemplate("resume")
		if !ok {
			t.Fatalf("%s: expected a resume template", c.Name)
		}
		if !containsPlaceholder(resume.Args, "{{prompt}}") {
			t.Fatalf("%s: resume template %+v missing {{prompt}}", c.Name, resume.Args)
		}
	}
}

func containsPlaceholder(args []string, placeholder string) bool {
	for _, a := range args {
		if strings.Contains(a, placeholder) {
			return true
		}
	}
	return false
}

// TestRunWithOptions_UnknownTemplateErrors examines the path where the lookup
// of a template fails. That failure occurs before the code starts a process.
// Therefore the test needs no true binary.
//
// This comment was stranded above TestRunWithOptions_SubstitutesCwd until
// 2026-08-03 — the function moved and its doc comment stayed behind.
func TestRunWithOptions_UnknownTemplateErrors(t *testing.T) {
	v := vendors.Vendor{
		Name: "agy", Path: "agy.exe", Enabled: true,
		Templates: []vendors.CommandTemplate{{Name: "default", Args: []string{"-p", "{{prompt}}"}, Mode: "headless"}},
	}
	_, err := vendors.RunWithOptions(context.Background(), v, "hi", vendors.RunOptions{Template: "does-not-exist"})
	if err == nil {
		t.Fatalf("expected an error for an unresolved template name")
	}
}

// TestRunWithOptions_InteractiveModeRejected proves Path B's "interactive"
// mode is explicitly refused rather than silently treated as headless —
// RunWithOptions does not implement pty handling yet
// (planning/permission_relay_design.md §3).
func TestRunWithOptions_InteractiveModeRejected(t *testing.T) {
	v := vendors.Vendor{
		Name: "agy", Path: "agy.exe", Enabled: true,
		Templates: []vendors.CommandTemplate{{Name: "interactive", Args: []string{"{{prompt}}"}, Mode: "interactive"}},
	}
	_, err := vendors.RunWithOptions(context.Background(), v, "hi", vendors.RunOptions{Template: "interactive"})
	if err == nil {
		t.Fatalf("expected an error — interactive mode is not implemented by RunWithOptions")
	}
	if !strings.Contains(err.Error(), "interactive") {
		t.Fatalf("got error %q, want it to explain the interactive-mode gap", err.Error())
	}
}

// TestResolveTemplate_SynthesizesDefaultFromPrintFlag proves a vendor with
// no Templates (every vendor discovered before this feature existed)
// still resolves a usable "default" template from PrintFlag.
func TestResolveTemplate_SynthesizesDefaultFromPrintFlag(t *testing.T) {
	v := vendors.Vendor{Name: "agy", PrintFlag: "-p"}
	tmpl, ok := v.ResolveTemplate("")
	if !ok {
		t.Fatalf("expected a synthesized default template")
	}
	if len(tmpl.Args) != 2 || tmpl.Args[0] != "-p" || tmpl.Args[1] != "{{prompt}}" {
		t.Fatalf("got %+v", tmpl)
	}
}

func TestResolveTemplate_UnknownNamedTemplateNotSynthesized(t *testing.T) {
	v := vendors.Vendor{Name: "agy", PrintFlag: "-p"}
	_, ok := v.ResolveTemplate("resume")
	if ok {
		t.Fatalf("a non-default unresolved template name must not fall back to a synthesized one")
	}
}

// TestFormatDenialSummary_IncludesToolAndDetail is a light sanity check on
// the shared text-rendering used by both DelegateHandler and api.Messages.
func TestFormatDenialSummary_IncludesToolAndDetail(t *testing.T) {
	out := vendors.FormatDenialSummary("agy", "tok123", []vendors.Denial{{ToolName: "command", Detail: "rm old.py"}}, "partial output")
	if !strings.Contains(out, "agy") || !strings.Contains(out, "command") || !strings.Contains(out, "rm old.py") || !strings.Contains(out, "partial output") {
		t.Fatalf("got %q, missing expected content", out)
	}
	if !strings.Contains(out, "tok123") {
		t.Fatalf("got %q, expected the correlation token to be embedded so it round-trips back", out)
	}
}

// TestParseDelegateCommand_GluedFlagNoMatch guards the trailing-boundary
// check: a flag glued directly onto preceding text with no whitespace
// separating them (e.g. a word that ends with the name of the vendor after a
// slash, for a different purpose) must not agree. Only a flag after a space
// character, or at the start of the text, counts.
func TestParseDelegateCommand_GluedFlagNoMatch(t *testing.T) {
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Path: "agy.exe", PrintFlag: "-p", Enabled: true},
	}}

	_, _, _, ok := vendors.ParseDelegateCommand(reg, "check the path something/agy")
	if ok {
		t.Fatalf("expected no match — the flag isn't a distinct trailing token")
	}
}

// TestParseDelegateCommand_UnrelatedSlashWordNoFalseMatch proves a
// trailing word that merely contains the vendor name as a substring
// (rather than being exactly "/<name>") does not match.
func TestParseDelegateCommand_UnrelatedSlashWordNoFalseMatch(t *testing.T) {
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{Name: "agy", Path: "agy.exe", PrintFlag: "-p", Enabled: true},
	}}

	_, _, _, ok := vendors.ParseDelegateCommand(reg, "please run /agycustom")
	if ok {
		t.Fatalf("expected no match — \"/agycustom\" is not the vendor flag \"/agy\"")
	}
}

// The documented template inventory in docs/instructions.md §3 must agree
// with the shipped config. A person who adds, removes or re-modes a template
// has to update that table, and this test is what tells them. Mode is the
// field of most importance. /agy is interactive by DEFAULT, and the other two
// vendors are headless. A change there, with no message, turns "delegate and
// use the answer" into "open a window and lose the work".
func TestRealConfig_TemplateInventoryMatchesDocs(t *testing.T) {
	want := map[string]map[string]string{
		"claude": {
			"default":     "headless",
			"resume":      "headless",
			"interactive": "interactive",
		},
		"cursor-agent": {
			"default":     "headless",
			"resume":      "headless",
			"interactive": "interactive",
		},
		"agy": {
			"default":     "interactive",
			"headless":    "headless",
			"resume":      "headless",
			"interactive": "interactive",
		},
	}

	candidates, err := vendors.LoadCandidates("../../configs/vendor_candidates.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := map[string]map[string]string{}
	for _, c := range candidates {
		modes := map[string]string{}
		for _, tm := range c.Templates {
			modes[tm.Name] = tm.Mode
		}
		got[c.Name] = modes
	}

	for vendor, wantModes := range want {
		gotModes, ok := got[vendor]
		if !ok {
			t.Fatalf("vendor %q missing from the shipped config", vendor)
		}
		for name, mode := range wantModes {
			if gotModes[name] != mode {
				t.Errorf("%s:%s mode = %q, want %q — update the table in docs/instructions.md §3",
					vendor, name, gotModes[name], mode)
			}
		}
		for name := range gotModes {
			if _, documented := wantModes[name]; !documented {
				t.Errorf("%s has an undocumented template %q — add it to docs/instructions.md §3", vendor, name)
			}
		}
	}
}

// docs/instructions.md §3 tells readers not to type ":resume" themselves,
// because claude and cursor-agent both template {{session_id}} and
// ResolveDelegate never supplies one. Pin that: a template carrying the
// placeholder must be rejected rather than run with an empty id.
func TestRunWithOptions_ResumeTemplateWithoutSessionIDErrors(t *testing.T) {
	candidates, err := vendors.LoadCandidates("../../configs/vendor_candidates.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, c := range candidates {
		resume, ok := vendors.Vendor{Name: c.Name, Templates: c.Templates}.ResolveTemplate("resume")
		if !ok {
			continue
		}
		if !containsPlaceholder(resume.Args, "{{session_id}}") {
			continue // agy resumes via a settings.json grant, no id needed
		}
		v := vendors.Vendor{Name: c.Name, Path: "go", Templates: c.Templates}
		_, err := vendors.RunWithOptions(context.Background(), v, "hi", vendors.RunOptions{Template: "resume"})
		if err == nil {
			t.Fatalf("%s:resume ran with no session id — docs promise it is refused", c.Name)
		}
		if !strings.Contains(err.Error(), "session id") {
			t.Fatalf("%s:resume error = %v, want it to name the missing session id", c.Name, err)
		}
	}
}

// Delegation at the same time is a documented function. AGENTS.md tells a
// front CLI to send independent tasks out through its own subagents.
// Therefore the statement "the runs share no state" needs a test, and not a
// read of the code.
//
// This test removes the danger that the OLD design truly had. That design
// wrote the delegate context in the AGENTS.md file of the project, and it put
// the original content back after. Therefore two delegates to the same vendor
// and the same workspace, at the same time, competed for one file.
// PrepareContextDir replaced that with a private directory per run. Each of
// these goroutines writes a context pack. It then reads the file that its own
// template names. If two runs shared a directory, the packs would mix, and a
// minimum of one run would read the task of a different run.
func TestRunWithOptions_ConcurrentDelegatesDoNotShareContext(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found on PATH — skipping shell-based fake-exec test")
	}
	v := vendors.Vendor{
		Name: "fake", Path: shPath,
		ContextFile: "AGENTS.md",
		Templates: []vendors.CommandTemplate{
			// print the context file this run was handed, nothing else
			{Name: "default", Mode: "headless", Args: []string{"-c", "cat \"$1\"", "_", "{{context_dir}}/AGENTS.md"}},
		},
	}

	const n = 12
	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			task := fmt.Sprintf("task-%02d-unique-marker", i)
			res, err := vendors.RunWithOptions(context.Background(), v, task,
				vendors.RunOptions{ContextPack: vendors.ContextPack{Task: task}})
			got[i], errs[i] = res.Text, err
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("run %d failed: %v", i, errs[i])
		}
		mine := fmt.Sprintf("task-%02d-unique-marker", i)
		if !strings.Contains(got[i], mine) {
			t.Errorf("run %d did not receive its own task %q", i, mine)
		}
		for j := 0; j < n; j++ {
			if j == i {
				continue
			}
			if strings.Contains(got[i], fmt.Sprintf("task-%02d-unique-marker", j)) {
				t.Errorf("run %d saw run %d's task — delegates are sharing a context directory", i, j)
			}
		}
	}
}
