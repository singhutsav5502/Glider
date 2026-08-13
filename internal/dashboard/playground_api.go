package dashboard

import (
	"encoding/json"
	"net/http"

	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/vendors"
)

// handlePlaygroundParse supports the Playground tab of the dashboard. That tab
// teaches the syntax of the commands that a person types in a chat: the
// delegate flags, the workspace flags, the tokens to allow or refuse a
// permission, and the routing overrides. A person learns by doing.
//
// It classifies a message that a person types with the true and exported
// parsers of Glider: vendors.ParseDelegateCommand, vendors.ParseWorkspaceCommand
// and router.MatchExplicitCommand. It does not use a new version in JavaScript.
// Therefore the explanation that a person sees can never become different from
// the true behaviour of Glider.
//
// This code only reads, and it changes nothing. This is on purpose. It runs no
// vendor with no console, it gives and removes no permission, it writes no
// workspace, and it uses no true token for a pending resume. A person can push
// the button many times.
//
// It reads one part of the true state, and it does not write it: the live
// registry of vendors and the routing config. Therefore the examples show the
// true setup of the operator, with the true names of their vendors and the
// routing commands that they configured. The examples are not fixed values that
// can have no relation to this machine.
func (s *Server) handlePlaygroundParse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var reg vendors.Registry
	if regPath, err := vendors.DefaultRegistryPath(); err == nil {
		reg, _ = vendors.LoadRegistry(regPath) // best-effort: an unreadable registry just means nothing to test delegate/allow/deny against yet
	}

	var cfg *config.Config
	if s.Config != nil {
		cfg = s.Config.Get()
	}

	writeJSON(w, playgroundParseResponse{
		Vendors:     enabledVendorNames(reg),
		Workspace:   classifyPlaygroundWorkspace(body.Text),
		Delegate:    classifyPlaygroundDelegate(reg, body.Text),
		Routing:     classifyPlaygroundRouting(cfg, body.Text),
		ScriptRules: playgroundScriptRuleNames(cfg),
	})
}

type playgroundParseResponse struct {
	// Vendors lists the currently enabled CLI names (e.g. "agy",
	// "cursor-agent") a delegate flag can actually target right now.
	Vendors     []string                  `json:"vendors"`
	Workspace   playgroundWorkspaceResult `json:"workspace"`
	Delegate    playgroundDelegateResult  `json:"delegate"`
	Routing     playgroundRoutingResult   `json:"routing"`
	ScriptRules []string                  `json:"scriptRules"`
}

type playgroundWorkspaceResult struct {
	Matched bool   `json:"matched"`
	Path    string `json:"path,omitempty"`
}

// playgroundDelegateResult.Kind gives the same branches that ResolveDelegate
// uses. Refer to internal/vendors/resume.go.
//
//   "allow" and "deny"    the reserved names for the flow of control.
//   "interactive"         each other template with Mode "interactive". It
//                         opens a true window, and it sends nothing back.
//   "run"                 a usual delegate call with no console.
//   "unknown_template"    a true request also gets this result when
//                         ":template" names a template that no person gave
//                         to the vendor. ParseDelegateCommand examines only
//                         the *shape* of the trailing flag, and it does not
//                         examine that the template exists.
type playgroundDelegateResult struct {
	Matched  bool   `json:"matched"`
	Kind     string `json:"kind,omitempty"`
	Vendor   string `json:"vendor,omitempty"`
	Template string `json:"template,omitempty"`
	// Prompt is the text that would run (kind=="run"/"interactive") or the
	// pending-resume token being answered (kind=="allow"/"deny").
	Prompt string `json:"prompt,omitempty"`
}

type playgroundRoutingResult struct {
	Matched   bool   `json:"matched"`
	Command   string `json:"command,omitempty"`
	Remainder string `json:"remainder,omitempty"`
	RuleName  string `json:"ruleName,omitempty"`
	Target    string `json:"target,omitempty"`
	// ConfiguredCommands holds each entry of trigger.commands from the enabled
	// "explicit" routing rules of this operator, with each repeated value removed.
	// This is the true set that MatchExplicitCommand examined. It is not a fixed
	// pair of /local and /cloud, which a person can have no configuration for on
	// this machine.
	ConfiguredCommands []string `json:"configuredCommands"`
}

// enabledVendorNames gives the enabled vendors, and it puts the vendors with a
// headless default first.
//
// The order is not cosmetic. The page uses the first name to build its
// examples, and the first lesson is "send a task, and get the answer back". A
// vendor with an interactive default opens a window and returns nothing, so it
// teaches the opposite of what that lesson says.
//
// The test reads the mode of the template, and it never reads the name of the
// vendor. A fourth CLI with a headless default sorts to the front by itself.
func enabledVendorNames(reg vendors.Registry) []string {
	headless := []string{}
	other := []string{}
	for _, v := range reg.Enabled() {
		if t, ok := v.ResolveTemplate("default"); ok && t.Mode == "headless" {
			headless = append(headless, v.Name)
			continue
		}
		other = append(other, v.Name)
	}
	return append(headless, other...)
}

func classifyPlaygroundWorkspace(text string) playgroundWorkspaceResult {
	path, ok := vendors.ParseWorkspaceCommand(text)
	return playgroundWorkspaceResult{Matched: ok, Path: path}
}

func classifyPlaygroundDelegate(reg vendors.Registry, text string) playgroundDelegateResult {
	vendor, templateName, prompt, ok := vendors.ParseDelegateCommand(reg, text)
	if !ok {
		return playgroundDelegateResult{}
	}
	res := playgroundDelegateResult{Matched: true, Vendor: vendor.Name, Template: templateName, Prompt: prompt}
	switch templateName {
	case "allow":
		res.Kind = "allow"
	case "deny":
		res.Kind = "deny"
	default:
		tmpl, found := vendor.ResolveTemplate(templateName)
		switch {
		case !found:
			res.Kind = "unknown_template"
		case tmpl.Mode == "interactive":
			res.Kind = "interactive"
		default:
			res.Kind = "run"
		}
	}
	return res
}

func classifyPlaygroundRouting(cfg *config.Config, text string) playgroundRoutingResult {
	res := playgroundRoutingResult{ConfiguredCommands: []string{}}
	if cfg == nil {
		return res
	}
	seen := map[string]bool{}
	byCommand := map[string]config.RuleConfig{}
	for _, rule := range cfg.Routing.Rules {
		if rule.Trigger.Type != "explicit" || !rule.IsEnabled() {
			continue
		}
		for _, c := range rule.Trigger.Commands {
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			res.ConfiguredCommands = append(res.ConfiguredCommands, c)
			byCommand[c] = rule
		}
	}
	cmd, remainder, ok := router.MatchExplicitCommand(text, res.ConfiguredCommands)
	if !ok {
		return res
	}
	res.Matched = true
	res.Command = cmd
	res.Remainder = remainder
	if rule, found := byCommand[cmd]; found {
		res.RuleName = rule.Name
		res.Target = rule.Action.Target
	}
	return res
}

// playgroundScriptRuleNames lists enabled script-triggered rules — their
// trigger phrases are whatever the .star file itself looks for, not a
// fixed grammar this endpoint can safely evaluate, so the playground can
// only point at them by name rather than classify against them directly.
func playgroundScriptRuleNames(cfg *config.Config) []string {
	names := []string{}
	if cfg == nil {
		return names
	}
	for _, rule := range cfg.Routing.Rules {
		if rule.Trigger.Type == "script" && rule.IsEnabled() {
			names = append(names, rule.Name)
		}
	}
	return names
}
