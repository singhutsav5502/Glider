// Package vendors finds which agent CLIs are installed on this host and can
// operate. These are claude, cursor-agent, agy and others. It then keeps
// that result as a registry. The dashboard can show the registry and enable
// or disable each entry.
//
// No Go code contains the list of CLIs. The list of candidates is data, in
// configs/vendor_candidates.yaml. Refer to "vendor packs, not Go switch
// statements" in agent_cli_interop.md §1. Discovery adds a candidate to the
// registry only after it runs that candidate. The list alone is not
// sufficient.
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
// Keep this history, because two different values were incorrect. The first
// value was 120s. Then a person made it 6 minutes, because the default
// --print-timeout of agy is 5 minutes. Thus Glider stopped agy three minutes
// before agy stopped itself. But 6 minutes was also a value that Glider
// selected with no evidence. It stopped a true and correct refactor task in
// the middle of the work, with no message (live, 2026-07-31). The delegate
// made progress. It only needed more time than a constant that no person had
// evidence for.
//
// The principle is this. Glider is a relay. It does not decide how long the
// CLI of a different supplier can think. Each vendor already has its own
// time limit, for example --print-timeout in agy. The front CLI has its own
// wait time. And the context of the caller cancels when the request of the
// person goes away. These are three true limits, and each one answers to a
// true condition. A fourth limit with a fixed value can operate only
// *before* one of those three. Therefore the only unique result it can give
// is a stop of work that was correct.
//
// To make a limit again, set this to a value that is more than zero. Refer
// to SetRunTimeout.
//
// A limit has a cost that is more than the run that it stops. A run with no
// limit that truly stops keeps its process and its private context directory
// until Glider exits. Then the kill-on-close job object removes the process,
// and SweepDelegateContextDirs removes the directory.
//
// But such a run does not block a different delegate. Each run makes its own
// directory in PrepareContextDir, and no lock continues for the life of a
// run. Therefore many delegates can operate at the same time, and they can
// go to the same vendor and the same workspace.
//
// An earlier design wrote in the context file of the project and put the
// original content back after the run. That design did hold a lock for the
// full life of a run, and a comment here named its InstallContextPack
// function until 2026-08-02. No such function or lock exists now.
var RunTimeout time.Duration

// SetRunTimeout reimposes a ceiling on headless delegate calls; zero
// removes it. Safe to call at startup before any delegate runs.
func SetRunTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	RunTimeout = d
}

// CommandTemplate is one named method to start the CLI of a vendor. A user
// can change it from the dashboard. Refer to
// planning/permission_relay_design.md §1. It is not fixed in Go code. Args
// can contain the marks {{prompt}} and {{session_id}}. RunWithOptions puts
// the values in their place when it starts the process.
type CommandTemplate struct {
	Name string   `yaml:"name" json:"name"`
	Args []string `yaml:"args" json:"args"`
	// Mode selects what Glider does with the process that it starts:
	//
	//   "headless"    — start the process, capture stdout and stderr, and
	//                   wait for the exit. RunWithOptions does this.
	//   "interactive" — start the native session of the vendor in a new
	//                   console window, and give the window to the person.
	//                   ResolveDelegate sends this mode to
	//                   LaunchInteractiveFunc, because there is no stdout to
	//                   capture. Refer to interactive.go.
	//
	// Do not confuse "interactive" with Path B in §3 of the design document.
	// Path B stops a run in the middle and asks the person through the
	// original session. No person has built Path B. This mode opens a
	// different window, and no text comes back into the session that sent
	// the task.
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
	// ContextFile is the file that the CLI of THIS vendor reads from its
	// work directory when a session starts. The name is CLAUDE.md,
	// AGENTS.md or GEMINI.md, and it is truly different for each vendor.
	// Refer to the comment on ContextPack. Glider writes a delegate context
	// pack in that file, and it puts the original content back after the
	// run. Thus a delegated run does not start cold. An empty value stops
	// the sharing of context for this vendor. This is data, from
	// vendor_candidates.yaml. It is never a test on the vendor name in Go.
	ContextFile string `json:"contextFile,omitempty"`
}

// ResolveTemplate finds a command template by its name.
//
// If Templates is empty and the name is "default" or empty, it makes a
// template from PrintFlag. This keeps each vendor that a person found
// before this function existed. It also keeps each test and each caller
// that makes a plain Vendor{PrintFlag: "-p"} with no Templates.
//
// Each other name that it cannot find is a true failure, and it returns
// ok=false. It does not send that name to the made template.
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
	// DefaultWorkspace keeps the default directory of WorkspaceStore
	// (workspace.go) on disk. A person sets it from the dashboard. Glider
	// puts it in the store in memory when it starts (cmd/glider/main.go).
	// Thus it continues after a restart. Discover() never changes this
	// field.
	DefaultWorkspace string `json:"defaultWorkspace,omitempty"`
	// ResponseDetail keeps the response-detail setting (render.go) on disk.
	// It uses the same pattern as DefaultWorkspace: a person sets it from
	// the dashboard, and Glider reads it when it starts.
	//
	// An empty value or "clean" makes ResolveDelegate send the captured raw
	// output of a delegate through ngl.DelegateRenderer before it replies.
	// If no clean render is available, ResolveDelegate replies with the raw
	// output and adds a note.
	//
	// "raw" makes ResolveDelegate always reply with the captured raw output
	// of the vendor, with no format. This is of use to find the cause of a
	// problem in a delegate run. It is not the value for usual work.
	ResponseDetail string `json:"responseDetail,omitempty"`
}

// Discover examines each candidate with exec.LookPath. For each candidate
// that it finds, it then runs the probe_args of that candidate before it
// adds the candidate to the registry. It does not only test that the binary
// exists. This is the same lesson as "confirmed, not just wire-declared"
// from the agy research in agent_cli_interop.md.
//
// A candidate that cannot run is absent from the result, and this is not an
// error. The candidate can be absent, it can give an error, or it can go
// past its time limit.
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

// DefaultRegistryPath gives the location where Glider keeps the registry
// that it found. Thus discovery occurs one time, or when a person pushes
// "Rescan". It does not occur again for each request.
func DefaultRegistryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".glider", "vendors.json"), nil
}

// LoadRegistry reads a registry that Glider kept before. A file that is
// absent is not an error. It shows only that discovery did not run.
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
	// Use atomicfile. Glider writes this file again for each change of the
	// dashboard config. Examples: a vendor becomes enabled or disabled, a person
	// changes a template, or a person saves a default workspace. A failure
	// during the write must not make the file short or incorrect. If that
	// occurs, a person must do the full discovery again.
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

// SetEnabled changes the enabled state of a vendor in the registry. It
// returns false if the registry does not contain that vendor.
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

// ParseDelegateCommand looks for a flag with the form "/<vendor-name>" or
// "/<vendor-name>:<template>". The flag must be at the very end of
// userText. It must not be at a different position, and it must not be the
// first characters.
//
// This limit is necessary, and it is not a question of style. Some CLI
// clients read a "/" at the start as their own local command. This is
// confirmed live for Claude Code, which gives "Unknown command: /agy".
// Therefore a message that starts with "/word" never goes on the network.
// The client takes it and refuses it before Glider sees it. A flag at the
// end prevents this failure for each front CLI, and not only for the CLIs
// that have this behaviour today.
//
// The prompt is all the text before the flag, with the spaces removed from
// each end. This function keeps that text and does not discard it. An
// earlier version looked for the flag at each position, and it discarded
// the text before the first flag that it found. A flag with no text in
// front of it gives no match, because there is no prompt to run.
//
// The optional ":<template>" part selects a CommandTemplate other than the
// "default" template of that vendor. Refer to CommandTemplate and
// ResolveTemplate. A person can add such a template from the dashboard. An
// example is "fix the auth bug /agy:interactive".
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

// trailingFlag says if s ends with "/name" or with "/name:template", as a
// separate token. A space character must come before that token, or the token
// must be at the start of the text. Therefore "/agycustom" at the end of a
// message never agrees with the vendor "agy". Returns the byte offset the
// flag starts at and, for the ":template" form, the template name; "default"
// for the bare form.
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

// trailingFlagBoundaryOK makes sure that the character immediately before
// a flag is a space character, and not more text of an identifier. There
// can also be no character before the flag. The old matcher for a flag at
// the start did the same test with HasPrefix(rest, " "). This test is at
// the other end of the string.
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

// MaxPromptLen is a limit on the length of a delegate prompt. Glider gives
// that prompt as one argv entry.
//
// An instruction from a person that internal/ngl extracts correctly is much
// shorter than this limit. This limit is a second protection, and it is not
// the primary correction. It is for the failure that a live test found on
// 2026-07-26. An incorrect extraction made a "prompt" that was some hundred
// KB of collected context. That prompt went past the limit on the length of a
// command line in Windows, in CreateProcess. The error was not clear:
// "filename or extension is too long".
//
// A refusal here is a failure that a person can read. A refusal from
// CreateProcess is not.
const MaxPromptLen = 8192

// Denial is one permission that a delegate CLI refused during a run with no
// console. Glider finds it in the output of that CLI. Refer to denial.go
// for the detector of each vendor, and to
// planning/permission_relay_design.md §2.1 for the cause: each vendor needs
// a different detector.
type Denial struct {
	ToolName string `json:"toolName"`
	Detail   string `json:"detail"`
}

// RunResult holds what a delegate run with no console produced. It has
// four parts:
//
//   Text      — the output text.
//   Denials   — each permission that the CLI refused. This is nil unless
//               denial.go has a detector for that vendor.
//   SessionID — a session id from the output, for a resume later. Glider
//               takes it only when the format of the vendor gives one on
//               the wire, as claude and cursor-agent do. agy resumes with a
//               plain --continue flag and needs no id, so this stays empty
//               for agy.
//   EditViews — an ngl.EditViews when the output of the vendor had
//               sufficient structure. This is the part of Path A that
//               observes the result, and not the part that relays a
//               permission. It is nil for the headless output of agy, which
//               is prose only, and for each run that made no edit.
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
	// Cwd gives the value for {{cwd}}. If it is empty, RunWithOptions uses
	// os.Getwd() of the server process. That was the behaviour before
	// 2026-07-26. A live test showed that it is incorrect for true delegation.
	// It sent a resume call to the repository directory of Glider, and not to
	// the project of the origin CLI. A caller must find the true origin
	// directory with WorkspaceStore before it calls RunWithOptions. Refer to
	// resolveWorkspace in resume.go.
	Cwd string
	// If ContextPack is not empty, Glider writes it into the ContextFile of
	// the vendor inside Cwd. The content stays there for this run only, and
	// Glider puts the original content back after the run. Refer to the
	// comment on ContextPack for the cause: this method is better than an
	// addition to the prompt. Glider ignores this field when the vendor
	// declares no ContextFile.
	ContextPack ContextPack
	// Glider adds ExtraArgs after the args of the template, with no change.
	// The only true caller today is resolveAllow (resume.go). It fills this
	// field from VendorAdapter.ExtraResumeArgs(denials). With this
	// mechanism, a vendor can limit a resume to exactly the tools that it
	// refused before. In claude this is "--allowedTools <names>". Without
	// it, Glider would send the same prompt again against the same
	// permission state. This field is nil or empty for a vendor with no such
	// mechanism.
	ExtraArgs []string
}

// Run gives prompt to the CLI of the vendor with its "default" template. It
// returns only the captured output text. This was the first entry point. It
// stays as a thin cover over RunWithOptions, so each caller and each test
// continues with no change.
func Run(ctx context.Context, v Vendor, prompt string) (string, error) {
	res, err := RunWithOptions(ctx, v, prompt, RunOptions{})
	return res.Text, err
}

// RunWithOptions does all that Run does, and more. It selects a template by
// name. This includes each template that a person adds from the dashboard,
// and not only "default". Refer to CommandTemplate. When Resume has a
// value, RunWithOptions puts that value in the {{session_id}} mark of the
// template for a resume.
//
// DetectDenials finds the refused permissions in the captured output. This
// is not always possible, and it is never an error by itself. A run can
// give denials and no other output. This occurs with agy: it writes nothing
// on stdout and puts all the text on stderr. That run still gives a correct
// RunResult and not an error, because the caller needs the denial to act on
// it.
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
		// This refusal is by design, and it does not mean "not now". An
		// interactive template has no stdout to capture. The full contract
		// of RunResult (Text, Denials, SessionID, EditViews) needs a run
		// with no console that exits and leaves a buffer.
		//
		// ResolveDelegate examines Mode itself and sends an interactive
		// template to LaunchInteractiveFunc before the code arrives here.
		// Refer to resolveInteractive in internal/vendors/resume.go. A
		// caller that arrives here goes around ResolveDelegate. It gets a
		// clear error, and not a run with no console that is incorrect and
		// gives no message.
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
	// A private context directory for this one run — see ContextPack. No lock,
	// no restore, nothing shared: it belongs to this delegate alone and is
	// removed when the run ends. A failure here is not fatal, by design. To lose
	// the context makes the answer worse. To refuse to run would be worse than
	// that. And PrepareContextDir leaves nothing on its paths with an error.
	contextDir, contextFile, cleanupContext, ctxErr := PrepareContextDir(v.ContextFile, opts.ContextPack)
	if ctxErr != nil {
		contextDir, contextFile = "", ""
	}
	defer func() { _ = cleanupContext() }()

	args := substituteTemplateArgs(tmpl.Args, prompt, opts.Resume, cwd, contextDir, contextFile)
	args = append(args, opts.ExtraArgs...)

	// Add a time limit only when a person configured one. Refer to
	// RunTimeout. The context of the caller has control in both conditions.
	// Thus a request that the person stopped also stops the subprocess.
	if RunTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, RunTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, v.Path, args...)
	procutil.HideWindow(cmd)
	// This was a true defect, and a live test found it on 2026-07-26. The
	// value for {{cwd}} changes only the ARGUMENT STRING, for example
	// --add-dir={{cwd}} in agy. It never changed the true work directory of
	// the new process. Therefore each vendor with no directory flag in its
	// template continued to run in the server directory of Glider, and it
	// ignored the workspace. claude and cursor-agent have no such flag.
	//
	// cmd.Dir is the true correction. The {{cwd}} mark stays for a vendor
	// such as agy, which also needs the directory as an argument.
	cmd.Dir = cwd
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	// Use Start, and not Run. Then Glider can add the process to the
	// kill-on-close job object between the start and the wait. Refer to the
	// comment on AssignToKillOnCloseJob for the live incident that this
	// closes.
	//
	// A forceful stop of glider.exe does not use the cancellation of ctx.
	// This includes taskkill /F and each external SIGKILL. No Go code
	// operates when a process stops in this way. The job object is the only
	// mechanism that still removes the child process. Windows applies it,
	// and not the code of Glider, which cannot operate.
	//
	// This is not always possible. If the assignment fails, this code writes
	// no message and continues with the behaviour from before 2026-07-31. A
	// child process can then stay after a forceful stop. This is better than
	// a stop of a delegate call that is correct. The gap is in the
	// protection, and not in the correctness.
	//
	// What cancellation of ctx does, measured 2026-08-13. A client stopped a
	// delegate request after 10 seconds. exec.CommandContext then stopped the
	// direct child at once, which is the cmd.exe of cursor-agent.cmd. The
	// DESCENDANTS of that child, which are the true node.exe processes that
	// do the work, continued. They were still present 20 seconds later, and
	// they were gone by 45 seconds. Glider itself stayed alive for all of it.
	//
	// They stop by themselves, and no code here stops them: the pipes of the
	// direct child close when it stops, and the vendor CLI ends its own run
	// when it finds that its output goes nowhere.
	//
	// So this is a delay of under one minute, and it is not a leak. Do not
	// "correct" it with a per-run job object and TerminateJobObject without
	// evidence for a true case that needs it. That change would stop each
	// descendant at the moment of cancellation, and it would also stop a
	// helper process that a vendor shares between runs. The history of
	// RunTimeout, in the comment on that variable, is the same lesson: two
	// values that appeared correct each stopped work that was correct.
	var runErr error
	if runErr = cmd.Start(); runErr == nil {
		// Membership of the job of AssignToKillOnCloseJob has a second use.
		// The Path B fulfillment path in internal/mitm (tryRunSSEFulfill)
		// uses it to know that this process is a delegate subprocess that
		// Glider started. This includes each child process of that process,
		// for example the true node.exe of cursor-agent.cmd. Refer to the
		// comment on procutil.IsInDelegateSubprocessJob for the cause.
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
		// Glider REMOVES a full argument that refers to context when this
		// run has no context. This occurs when the vendor has no
		// context_file, or when the pack is empty.
		//
		// An empty value would leave "--add-dir=" with nothing after it, or
		// an --append-system-prompt-file that points at nothing. The CLI
		// then gives a hard error about the argument. It does not continue
		// with less function.
		//
		// The context flag of each vendor uses the form with one argument
		// and an "=" sign. This is on purpose, because Glider can then
		// remove the flag as one unit. A live test on 2026-07-31 confirmed
		// that all three vendors accept that form.
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
