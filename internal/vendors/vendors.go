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

	"github.com/glider-ai/glider/internal/ngl"
	"gopkg.in/yaml.v3"
)

// ProbeTimeout bounds a single candidate's discovery probe.
const ProbeTimeout = 10 * time.Second

// RunTimeout bounds a single headless delegate call.
const RunTimeout = 120 * time.Second

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
	return os.WriteFile(path, data, 0o644)
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

// ParseDelegateCommand looks for a "/<vendor-name> " flag anywhere in
// userText — same convention as Glider's existing explicit /local /cloud
// /fast /heavy routing commands, matched dynamically against the registry's
// enabled vendors, never a hardcoded vendor name. Everything after the flag
// is the delegate prompt. Returns ok=false if no enabled vendor's flag
// appears.
//
// Matching happens anywhere in the text, not just a HasPrefix at position 0,
// for a real, confirmed reason: Claude Code always prepends a
// "<system-reminder>...</system-reminder>" block ahead of the actual user
// text (observed live, present even in --bare mode), so a flag typed as the
// user's first characters is never actually at index 0 of the text this
// function sees. A *separate* constraint applies only when a human is typing
// directly into Claude Code's own interactive/print-mode input, not to this
// function: Claude Code's own client intercepts "/word" as a local slash
// command and never sends anything over the network at all if the leading
// "/word" is unrecognized (confirmed live — "Unknown command: /agy"). That
// means "/agy " must not be the very first characters of what a human types
// into Claude Code specifically; anywhere else in the message is fine and
// reaches this function normally. Other fronts (Cursor, raw API callers)
// don't have that specific client-side quirk.
// The optional ":<template>" suffix selects a named CommandTemplate other
// than the vendor's "default" (see CommandTemplate, ResolveTemplate) — a
// dashboard-defined variant, e.g. "/agy:interactive" to launch agy's
// interactive-mode template instead of its headless default. No suffix (a
// plain space right after the vendor name, exactly as before this
// addition) means "default" — every pre-existing call matches unchanged.
//
// Matching walks forward past any false-positive prefix collision (e.g.
// "/agycustom" embedded earlier in the text, which is not a real flag)
// rather than stopping at the first occurrence of the vendor's name —
// necessary once matching can no longer rely on searching for one fixed
// "/name " string including its trailing space, since a "/name:template "
// form has a colon in that position instead.
func ParseDelegateCommand(reg Registry, userText string) (vendor Vendor, templateName, prompt string, ok bool) {
	for _, v := range reg.Enabled() {
		prefix := "/" + v.Name
		searchFrom := 0
		for {
			rel := strings.Index(userText[searchFrom:], prefix)
			if rel < 0 {
				break
			}
			idx := searchFrom + rel
			rest := userText[idx+len(prefix):]
			switch {
			case strings.HasPrefix(rest, " "):
				return v, "default", strings.TrimSpace(rest[1:]), true
			case strings.HasPrefix(rest, ":"):
				afterColon := rest[1:]
				if sp := strings.IndexByte(afterColon, ' '); sp > 0 {
					return v, afterColon[:sp], strings.TrimSpace(afterColon[sp+1:]), true
				}
			}
			searchFrom = idx + len(prefix)
		}
	}
	return Vendor{}, "", "", false
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
		return RunResult{}, fmt.Errorf("vendors: template %q on %s is interactive mode (Path B) — "+
			"not implemented by RunWithOptions yet, see planning/permission_relay_design.md §3", tmpl.Name, v.Name)
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
	args := substituteTemplateArgs(tmpl.Args, prompt, opts.Resume, cwd)

	ctx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, v.Path, args...)
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
	runErr := cmd.Run()

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

func substituteTemplateArgs(args []string, prompt, resumeID, cwd string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "{{prompt}}", prompt)
		a = strings.ReplaceAll(a, "{{session_id}}", resumeID)
		a = strings.ReplaceAll(a, "{{cwd}}", cwd)
		out[i] = a
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
