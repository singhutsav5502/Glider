package vendors_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/vendors"
)

// TestRun_RejectsOverlongPrompt is the second-line-of-defense regression
// test for the 2026-07-26 live bug: a mis-extracted "prompt" (hundreds of
// KB of accumulated context, not a genuine short human instruction) must
// fail with a clear, legible error before ever reaching exec.CommandContext
// — not surface as an opaque OS-level "filename or extension is too long".
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

// TestParseDelegateCommand_RequiresTrailingPosition proves a leading flag
// (the old, rejected convention) no longer matches — the flag must be the
// last token in the message, not the first, so a human typing directly
// into a CLI that reads a leading "/" as its own slash-command syntax
// never has that message intercepted client-side before Glider sees it.
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

// TestRunWithOptions_UnknownTemplateErrors exercises the template lookup
// failure path, which happens before any process is spawned — no real
// binary needed to test it.
// TestRunWithOptions_SubstitutesCwd proves {{cwd}} resolves to the real
// working directory — added 2026-07-26 after live testing found agy's -p
// mode silently operates in an unrelated fallback location without an
// explicit --add-dir={{cwd}} (see configs/vendor_candidates.yaml's agy
// "default" template). Uses the shell-script fake-exec pattern from
// resume_test.go's fakeAgyVendor to avoid depending on a real CLI.
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

// TestRunWithOptions_CwdSetsRealProcessDirectory is the regression test
// for a real bug found live 2026-07-26: {{cwd}} template substitution
// alone only changes an ARGUMENT STRING (what agy's --add-dir={{cwd}}
// needs) — it did NOT change the spawned process's actual OS working
// directory (cmd.Dir), so a vendor whose template has no explicit
// directory flag (claude, cursor-agent — confirmed neither has one) kept
// silently running in Glider's own server directory regardless of
// RunOptions.Cwd. Verified via a RELATIVE-path file read (not by comparing
// pwd's raw string output — sh.exe on Windows reports its own
// POSIX-translated view of a path, which doesn't string-compare cleanly
// against t.TempDir()'s Windows-style path; behavior, not string identity,
// is what actually matters here): a script that can only find
// marker-<random>.txt via a relative path proves the process really is
// running with dir as its cwd. This test would have failed before the
// cmd.Dir fix even though TestRunWithOptions_SubstitutesCwd (argument-
// string substitution only) passed throughout — the two tests catch
// genuinely different bugs.
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

// TestLoadCandidates_RealConfigParsesWithTemplates smoke-tests the actual
// shipped configs/vendor_candidates.yaml — every candidate must resolve
// both a "default" and a "resume" template with {{prompt}} present in
// Args, since RunWithOptions depends on both existing for Path A.
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
// RunWithOptions doesn't implement pty handling yet
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
// separating them (e.g. a word that just happens to end in the vendor's
// name preceded by a slash used for something else) must not match — only
// a flag preceded by whitespace or the start of the string counts.
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
