// Package vendors discovers which agent CLIs (claude, cursor-agent, agy, ...)
// are actually installed and runnable on this host, and persists that as a
// registry the dashboard can list and toggle. Nothing about which CLIs exist
// is hardcoded in Go — the candidate list is data (configs/vendor_candidates.yaml,
// see agent_cli_interop.md §1's "vendor packs, not Go switch statements"), and
// discovery only registers a candidate after actually running it, not from the
// candidate list alone.
package vendors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/atomicfile"
	"github.com/glider-ai/glider/internal/ngl"
	"github.com/glider-ai/glider/internal/procutil"
	"gopkg.in/yaml.v3"
)

// ProbeTimeout bounds a single candidate's discovery probe.
const ProbeTimeout = 10 * time.Second

// RunTimeout optionally bounds a single headless delegate call. Zero — the
// default — means NO Glider-imposed ceiling.
//
// History worth keeping, because this was wrong twice in opposite ways: it
// started at 120s, then was raised to 6 minutes when agy's own
// --print-timeout default (5m) proved Glider was cutting agy off three
// minutes before agy itself would have stopped. But 6 minutes was still an
// arbitrary number picked by Glider, and it silently killed a real,
// legitimate refactor task mid-work (live, 2026-07-31) — the delegate was
// making progress and simply needed longer than a constant nobody had
// evidence for.
//
// The principle: Glider is a relay, not the arbiter of how long someone
// else's CLI is allowed to think. Every vendor already has its own timeout
// (agy's --print-timeout), the front CLI has its own client-side patience,
// and the caller's context cancels when the human's request goes away —
// three real bounds that respond to actual conditions. A fourth, fixed
// ceiling stacked on top can only ever fire *before* one of those, which
// means the only thing it can uniquely do is interrupt work that was going
// fine.
//
// Set it to a non-zero duration to reimpose a ceiling (SetRunTimeout).
// Note what a ceiling costs beyond the killed run: a delegate holds its
// vendor's context-file lock (see InstallContextPack) for its whole
// lifetime, so an unbounded run that genuinely wedges blocks later
// delegates to that same vendor and workspace until Glider exits — at
// which point the kill-on-close job object reaps it regardless.
var RunTimeout time.Duration

// SetRunTimeout reimposes a ceiling on headless delegate calls; zero
// removes it. Safe to call at startup before any delegate runs.
func SetRunTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	RunTimeout = d
}

// CommandTemplate is one named way to launch a vendor's CLI — user-editable
// from the dashboard (planning/permission_relay_design.md §1), not
// hardcoded. Args may contain the placeholders {{prompt}} and
// {{session_id}}, substituted at exec time by RunWithOptions.
type CommandTemplate struct {
	Name string   `yaml:"name" json:"name"`
	Args []string `yaml:"args" json:"args"`
	// Mode selects how the launched process is handled: "headless" (exec,
	// capture stdout/stderr, wait for exit — what RunWithOptions
	// implements today) or "interactive" (Path B — a live pty-attached
	// session for a genuine mid-execution pause; not yet implemented, see
	// the design doc's §3 — RunWithOptions rejects this mode explicitly
	// rather than silently falling back to headless handling).
	Mode string `yaml:"mode" json:"mode"`
}

// Candidate is one entry from configs/vendor_candidates.yaml.
type Candidate struct {
	Name      string            `yaml:"name" json:"name"`
	Binary    string            `yaml:"binary" json:"binary"`
	ProbeArgs []string          `yaml:"probe_args" json:"probeArgs"`
	PrintFlag string            `yaml:"print_flag" json:"printFlag"`
	Templates []CommandTemplate `yaml:"templates,omitempty" json:"templates,omitempty"`
	// ContextFile — see Vendor.ContextFile.
	ContextFile string `yaml:"context_file,omitempty" json:"contextFile,omitempty"`
}

type candidateFile struct {
	Candidates []Candidate `yaml:"candidates"`
}

// LoadCandidates reads the candidate probe list from a YAML file.
func LoadCandidates(path string) ([]Candidate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("vendors: read candidates %s: %w", path, err)
	}
	var cf candidateFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("vendors: parse candidates %s: %w", path, err)
	}
	return cf.Candidates, nil
}

// Vendor is a candidate that was actually found and confirmed runnable on
// this host, plus the enable/disable state a user controls from the dashboard.
type Vendor struct {
	Name         string            `json:"name"`
	Binary       string            `json:"binary"`
	Path         string            `json:"path"`
	Version      string            `json:"version"`
	PrintFlag    string            `json:"printFlag"`
	Templates    []CommandTemplate `json:"templates,omitempty"`
	Enabled      bool              `json:"enabled"`
	DiscoveredAt time.Time         `json:"discoveredAt"`
	// ContextFile is the file THIS vendor's CLI reads from its working
	// directory at session start (CLAUDE.md / AGENTS.md / GEMINI.md — they
	// genuinely differ per vendor, see ContextPack's doc comment). Glider
	// writes a delegate context pack there and restores the file afterward,
	// so a delegated run isn't a cold start. Empty disables context sharing
	// for this vendor. Data, from vendor_candidates.yaml — never a
	// vendor-name branch in Go.
	ContextFile string `json:"contextFile,omitempty"`
}

// ResolveTemplate looks up a named command template, falling back to a
// template synthesized from PrintFlag when Templates is empty and name is
// "default" (or empty) — preserves every vendor discovered before this
// feature existed, and every test/caller that builds a bare
// Vendor{PrintFlag: "-p"} with no Templates. Any other unresolved name is a
// real miss (ok=false), not silently redirected to the synthesized default.
func (v Vendor) ResolveTemplate(name string) (CommandTemplate, bool) {
	if name == "" {
		name = "default"
	}
	for _, t := range v.Templates {
		if t.Name == name {
			return t, true
		}
	}
	if name == "default" && len(v.Templates) == 0 {
		return CommandTemplate{Name: "default", Args: []string{v.PrintFlag, "{{prompt}}"}, Mode: "headless"}, true
	}
	return CommandTemplate{}, false
}

// Registry is the persisted set of discovered vendors.
type Registry struct {
	Vendors []Vendor `json:"vendors"`
	// DefaultWorkspace is the persisted form of WorkspaceStore's default
	// directory (workspace.go) — set from the dashboard, seeded into the
	// in-memory store at startup (cmd/glider/main.go) so it survives a
	// restart. Discover() never touches this field.
	DefaultWorkspace string `json:"defaultWorkspace,omitempty"`
	// ResponseDetail is the persisted form of the response-detail setting
	// (render.go) — same seed-at-startup / set-from-dashboard pattern as
	// DefaultWorkspace. "" or "clean" means ResolveDelegate renders a
	// delegate's raw captured output through ngl.DelegateRenderer before
	// replying (falling back to raw, with a note, when no clean render is
	// available); "raw" means always reply with the vendor's raw captured
	// output, unformatted — useful for debugging a delegate run, not the
	// day-to-day default.
	ResponseDetail string `json:"responseDetail,omitempty"`
}

// Discover probes every candidate via exec.LookPath, and for each one found,
// actually runs its probe_args (not just checks the binary exists) before
// registering it — mirroring the "confirmed, not just wire-declared" lesson
// from agent_cli_interop.md's agy research. A candidate that fails to run
// (missing, errors, times out) is simply absent from the result, not an error.
func Discover(ctx context.Context, candidates []Candidate) Registry {
	var reg Registry
	for _, c := range candidates {
		path, err := exec.LookPath(c.Binary)
		if err != nil {
			continue
		}

		probeCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
		cmd := exec.CommandContext(probeCtx, path, c.ProbeArgs...)
		procutil.HideWindow(cmd)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		runErr := cmd.Run()
		cancel()
		if runErr != nil {
			continue
		}

		version := firstLine(out.String())
		reg.Vendors = append(reg.Vendors, Vendor{
			Name:         c.Name,
			Binary:       c.Binary,
			Path:         path,
			Version:      version,
			PrintFlag:    c.PrintFlag,
			Templates:    c.Templates,
			ContextFile:  c.ContextFile,
			Enabled:      true,
			DiscoveredAt: time.Now(),
		})
	}
	return reg
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// DefaultRegistryPath is where the discovered registry is persisted, so
// discovery is a one-time (or user-triggered "Rescan") action, not something
// re-run on every request.
func DefaultRegistryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".glider", "vendors.json"), nil
}

// LoadRegistry reads a previously persisted registry. A missing file is not
// an error — it just means discovery hasn't run yet.
func LoadRegistry(path string) (Registry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Registry{}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return Registry{}, fmt.Errorf("vendors: parse registry %s: %w", path, err)
	}
	return reg, nil
}

// SaveRegistry persists the registry, creating the parent directory if needed.
func SaveRegistry(path string, reg Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	// atomicfile: this file is rewritten on every dashboard config change
	// (enable/disable a vendor, edit a template, save a default
	// workspace, ...) — a crash mid-write shouldn't be able to leave it
	// truncated/invalid, forcing a full re-discover to recover.
	return atomicfile.WriteFile(path, data, 0o644)
}

// Find looks up a vendor by name.
func (r Registry) Find(name string) (Vendor, bool) {
	for _, v := range r.Vendors {
		if v.Name == name {
			return v, true
		}
	}
	return Vendor{}, false
}

// SetEnabled toggles a vendor's enabled state in place. Returns false if the
// vendor isn't in the registry.
func (r *Registry) SetEnabled(name string, enabled bool) bool {
	for i := range r.Vendors {
		if r.Vendors[i].Name == name {
			r.Vendors[i].Enabled = enabled
			return true
		}
	}
	return false
}

// Enabled returns only the vendors currently enabled.
func (r Registry) Enabled() []Vendor {
	var out []Vendor
	for _, v := range r.Vendors {
		if v.Enabled {
			out = append(out, v)
		}
	}
	return out
}

// ParseDelegateCommand looks for a "/<vendor-name>" or
// "/<vendor-name>:<template>" flag at the very end of userText — not
// anywhere in it, and specifically not as the first characters. That
// constraint is load-bearing, not stylistic: a human typing directly into
// a CLI whose own client treats a leading "/" as local slash-command
// syntax (confirmed live for Claude Code — "Unknown command: /agy") never
// gets that message onto the network at all if it starts with "/word";
// the client intercepts and rejects it before Glider ever sees it. Putting
// the flag at the end sidesteps that failure mode entirely, for every
// front, not just the ones known to have this quirk today.
//
// Everything before the flag, trimmed, is the prompt — kept intact, not
// discarded, which an earlier "anywhere in the text" version of this
// function used to do to whatever came before the first match. A bare
// flag with nothing in front of it (no prompt to run) does not match.
//
// The optional ":<template>" suffix selects a named CommandTemplate other
// than the vendor's "default" (see CommandTemplate, ResolveTemplate) — a
// dashboard-defined variant, e.g. "fix the auth bug /agy:interactive".
func ParseDelegateCommand(reg Registry, userText string) (vendor Vendor, templateName, prompt string, ok bool) {
	trimmed := strings.TrimRight(userText, " \t\r\n")
	for _, v := range reg.Enabled() {
		start, tmpl, matched := trailingFlag(trimmed, v.Name)
		if !matched {
			continue
		}
		promptPart := strings.TrimSpace(trimmed[:start])
		if promptPart == "" {
			continue // bare flag, nothing before it to act on
		}
		return v, tmpl, promptPart, true
	}
	return Vendor{}, "", "", false
}

// trailingFlag reports whether s ends with "/name" or "/name:template" as
// a distinct trailing token — preceded by whitespace or the start of the
// string, so "/agycustom" at the end of a message never matches vendor
// "agy". Returns the byte offset the flag starts at and, for the
// ":template" form, the template name; "default" for the bare form.
func trailingFlag(s, name string) (start int, template string, ok bool) {
	prefix := "/" + name
	if strings.HasSuffix(s, prefix) && trailingFlagBoundaryOK(s, len(s)-len(prefix)) {
		return len(s) - len(prefix), "default", true
	}
	marker := prefix + ":"
	idx := strings.LastIndex(s, marker)
	if idx < 0 || !trailingFlagBoundaryOK(s, idx) {
		return 0, "", false
	}
	rest := s[idx+len(marker):]
	if rest == "" || strings.ContainsAny(rest, " \t\r\n") {
		return 0, "", false
	}
	return idx, rest, true
}

// trailingFlagBoundaryOK confirms the character immediately before a
// matched flag (if any) is whitespace, not more identifier text — the
// same role a HasPrefix(rest, " ") check played in the old leading-flag
// matcher, just applied at the other end of the string.
func trailingFlagBoundaryOK(s string, at int) bool {
	if at == 0 {
		return true
	}
	switch s[at-1] {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

// Run executes prompt against vendor's CLI in non-interactive print mode and
// returns its captured stdout text, trimmed.
// MaxPromptLen defensively bounds a delegate prompt passed as a single
// argv entry. A correctly extracted human instruction (internal/ngl) is
// naturally far shorter than this — this exists as a second line of
// defense, not the primary fix, for the failure mode caught live on
// 2026-07-26: a mis-extracted "prompt" that was actually hundreds of KB of
// accumulated context blew past Windows' command-line length limit
// (CreateProcess) with an opaque "filename or extension is too long" error.
// Rejecting clearly here is a legible failure; let CreateProcess reject it
// is not.
const MaxPromptLen = 8192

// Denial is one permission check a delegate CLI declined to grant during a
// headless run, detected from its own output — see denial.go for the
// per-vendor detectors and planning/permission_relay_design.md §2.1 for
// why each vendor needs a different one.
type Denial struct {
	ToolName string `json:"toolName"`
	Detail   string `json:"detail"`
}

// RunResult is what a headless delegate invocation actually produced:
// output text, any permission denials detected in it (nil unless the
// vendor has a detector wired up in denial.go), a session id
// opportunistically recovered from the output for a later resume, when the
// vendor's format exposes one on the wire (claude, cursor-agent; agy
// resumes via a bare --continue flag with no id needed, so this stays
// empty for it), and — the observe-the-outcome half of Path A, not just
// the permission-relay half — an ngl.EditViews when the vendor's output
// carried enough structure to parse one (nil for agy's prose-only
// headless output, or any run that made no edit at all).
type RunResult struct {
	Text      string
	Denials   []Denial
	SessionID string
	EditViews *ngl.EditViews
}

// RunOptions selects which named CommandTemplate to launch (empty = the
// vendor's "default") and, for a resume invocation, the prior session id
// to substitute into {{session_id}}.
type RunOptions struct {
	Template string
	Resume   string
	// Cwd overrides {{cwd}} substitution — when empty, RunWithOptions falls
	// back to the server process's own os.Getwd() (the pre-2026-07-26
	// behavior, confirmed live to be wrong for real delegation: it scoped
	// a resume call to Glider's own repo directory instead of the origin
	// CLI's project). Callers should resolve the real origin directory via
	// WorkspaceStore before calling RunWithOptions — see resolveWorkspace
	// in resume.go.
	Cwd string
	// ContextPack, when non-empty, is written into the vendor's own
	// ContextFile inside Cwd for the duration of this run and reverted
	// afterward — see ContextPack's doc comment for why this beats prompt
	// injection. Ignored when the vendor declares no ContextFile.
	ContextPack ContextPack
	// ExtraArgs are appended after the resolved template's own args, verbatim.
	// The one real caller today is resolveAllow (resume.go), which fills
	// this from VendorAdapter.ExtraResumeArgs(denials) — the mechanism
	// that lets a vendor scope a resume retry to exactly the tool(s) that
	// were denied (claude: "--allowedTools <names>") instead of blindly
	// reissuing the identical prompt against the identical permission
	// state. nil/empty for vendors with no such per-denial mechanism.
	ExtraArgs []string
}

// Run executes prompt against vendor's CLI using its "default" template and
// returns just the captured output text — the original entry point,
// preserved as a thin wrapper over RunWithOptions so every existing
// caller/test keeps working unchanged.
func Run(ctx context.Context, v Vendor, prompt string) (string, error) {
	res, err := RunWithOptions(ctx, v, prompt, RunOptions{})
	return res.Text, err
}

// RunWithOptions is Run's superset: selects a named template (including any
// dashboard-defined variant, not just "default" — see CommandTemplate) and,
// when Resume is set, substitutes it into the template's {{session_id}}
// placeholder for a resume invocation. Denials are detected from the
// captured output via DetectDenials — best-effort and never an error on
// its own; a run that produced denials but no other output (agy's failure
// mode: no stdout at all, everything in stderr) is still a successful
// RunResult, not an error, since a caller needs the denial to act on it.
func RunWithOptions(ctx context.Context, v Vendor, prompt string, opts RunOptions) (RunResult, error) {
	if len(prompt) > MaxPromptLen {
		return RunResult{}, fmt.Errorf("vendors: prompt too long (%d bytes, max %d) — refusing to exec %s; "+
			"this usually means the caller extracted more than the genuine human instruction", len(prompt), MaxPromptLen, v.Name)
	}

	tmpl, ok := v.ResolveTemplate(opts.Template)
	if !ok {
		return RunResult{}, fmt.Errorf("vendors: %s has no %q command template", v.Name, opts.Template)
	}
	if tmpl.Mode == "interactive" {
		// By design, not "not yet": an interactive template has no
		// stdout to capture — RunResult's whole contract (Text, Denials,
		// SessionID, EditViews) assumes a headless run that exits and
		// leaves a buffer behind. ResolveDelegate checks Mode itself and
		// dispatches to LaunchInteractiveFunc before ever reaching
		// RunWithOptions (internal/vendors/resume.go's resolveInteractive)
		// — a caller that ends up here anyway (bypassing ResolveDelegate)
		// gets a clear error instead of a silent, wrong headless run.
		return RunResult{}, fmt.Errorf("vendors: template %q on %s is interactive mode — "+
			"RunWithOptions only runs headless templates; use ResolveDelegate (which dispatches "+
			"interactive templates to LaunchInteractiveFunc instead) or vendors.LaunchInteractive directly", tmpl.Name, v.Name)
	}
	if opts.Resume == "" && templateNeedsSessionID(tmpl.Args) {
		return RunResult{}, fmt.Errorf("vendors: template %q on %s requires a session id to resume ({{session_id}} in its args), but none was provided", tmpl.Name, v.Name)
	}

	cwd := opts.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return RunResult{}, fmt.Errorf("vendors: could not determine working directory to substitute {{cwd}}: %w", err)
		}
	}
	// A private context directory for this one run — see ContextPack. No
	// lock, no restore, nothing shared: it belongs to this delegate alone
	// and is removed when the run ends. A failure here is non-fatal by
	// design; losing context degrades the answer, refusing to run would be
	// worse, and PrepareContextDir leaves nothing behind on its error paths.
	contextDir, contextFile, cleanupContext, ctxErr := PrepareContextDir(v.ContextFile, opts.ContextPack)
	if ctxErr != nil {
		contextDir, contextFile = "", ""
	}
	defer func() { _ = cleanupContext() }()

	args := substituteTemplateArgs(tmpl.Args, prompt, opts.Resume, cwd, contextDir, contextFile)
	args = append(args, opts.ExtraArgs...)

	// Only wrap when a ceiling is actually configured — see RunTimeout.
	// The caller's own context still governs either way, so a request the
	// human abandoned still cancels the subprocess.
	if RunTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, RunTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, v.Path, args...)
	procutil.HideWindow(cmd)
	// Real bug, caught live 2026-07-26: {{cwd}} substitution alone only
	// changes the ARGUMENT STRING (agy's --add-dir={{cwd}}) — it never
	// changed the spawned process's actual OS working directory, so any
	// vendor whose template has no explicit directory flag (claude,
	// cursor-agent — neither has one) silently kept running in Glider's
	// own server directory regardless of a resolved workspace. cmd.Dir is
	// the actual fix; the {{cwd}} template placeholder stays for vendors
	// like agy that need the directory as an explicit argument too.
	cmd.Dir = cwd
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	// Start (not Run) so the process can be enrolled in the kill-on-close
	// job object between start and wait — see AssignToKillOnCloseJob's own
	// doc comment for the real, live-confirmed incident this closes: a
	// forceful kill of glider.exe (taskkill /F, or any external SIGKILL)
	// bypasses ctx cancellation entirely, since no Go code runs at all
	// when a process is forcibly terminated. The job object is the only
	// mechanism that still cleans up in that case, enforced by Windows
	// itself rather than by Glider's own (necessarily absent) code.
	// Best-effort: a failed assignment is logged nowhere here and just
	// falls back to pre-2026-07-31 behavior (possible orphan on a
	// forceful kill) rather than aborting an otherwise-healthy delegate
	// call over a hardening gap, not a correctness one.
	var runErr error
	if runErr = cmd.Start(); runErr == nil {
		// AssignToKillOnCloseJob's job membership doubles as the signal
		// internal/mitm's Path B fulfillment path (tryRunSSEFulfill) uses
		// to recognize this process (and any of its own child processes,
		// e.g. cursor-agent.cmd's real node.exe) as a Glider-spawned
		// delegate subprocess — see procutil.IsInDelegateSubprocessJob's
		// doc comment for why that matters.
		_ = procutil.AssignToKillOnCloseJob(cmd)
		runErr = cmd.Wait()
	}

	text := strings.TrimSpace(out.String())
	denials, _ := DetectDenials(v.Name, out.Bytes(), errOut.Bytes())

	if text == "" && len(denials) == 0 {
		if runErr != nil {
			return RunResult{}, fmt.Errorf("vendors: %s exited with error: %w (stderr: %s)", v.Name, runErr, strings.TrimSpace(errOut.String()))
		}
		return RunResult{}, fmt.Errorf("vendors: %s produced no output (stderr: %s)", v.Name, strings.TrimSpace(errOut.String()))
	}

	var editViews *ngl.EditViews
	if views, ok := adapterFor(v.Name).ExtractEditViews(out.Bytes()); ok {
		editViews = &views
	}
	return RunResult{Text: text, Denials: denials, SessionID: extractSessionID(v.Name, out.Bytes()), EditViews: editViews}, nil
}

func templateNeedsSessionID(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "{{session_id}}") {
			return true
		}
	}
	return false
}

func substituteTemplateArgs(args []string, prompt, resumeID, cwd, contextDir, contextFile string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		// An arg referencing context is DROPPED ENTIRELY when there is no
		// context for this run (no context_file configured for the vendor,
		// or an empty pack). Substituting empty would leave a dangling
		// "--add-dir=" or an --append-system-prompt-file pointing at
		// nothing, which is a hard argument error from the CLI rather than
		// a graceful degradation. Every vendor's context flag is written in
		// the single-arg "=" form precisely so it can be dropped as a unit
		// (all three confirmed live 2026-07-31 to accept that form).
		if strings.Contains(a, "{{context_dir}}") && contextDir == "" {
			continue
		}
		if strings.Contains(a, "{{context_file}}") && contextFile == "" {
			continue
		}
		a = strings.ReplaceAll(a, "{{prompt}}", prompt)
		a = strings.ReplaceAll(a, "{{session_id}}", resumeID)
		a = strings.ReplaceAll(a, "{{cwd}}", cwd)
		a = strings.ReplaceAll(a, "{{context_dir}}", contextDir)
		a = strings.ReplaceAll(a, "{{context_file}}", contextFile)
		out = append(out, a)
	}
	return out
}

// extractSessionID opportunistically pulls a session id off a completed
// run's stdout, dispatched through the registered VendorAdapter for
// vendorName — see adapter.go. A vendor with no adapter, or whose adapter
// has nothing to extract (agy: no --output-format at all), returns "".
func extractSessionID(vendorName string, stdout []byte) string {
	return adapterFor(vendorName).ExtractSessionID(stdout)
}
