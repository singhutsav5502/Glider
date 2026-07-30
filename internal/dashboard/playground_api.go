package dashboard

import (
	"encoding/json"
	"net/http"

	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/vendors"
)

// handlePlaygroundParse backs the dashboard's Playground tab — a
// teach-by-doing surface for Glider's chat-typed command syntax (delegate
// flags, workspace flags, permission allow/deny tokens, routing
// overrides). It classifies a typed message against Glider's real,
// exported command parsers — vendors.ParseDelegateCommand,
// vendors.ParseWorkspaceCommand, router.MatchExplicitCommand — instead of
// a JS reimplementation, so the explanation shown can never drift out of
// sync with what Glider actually does. Deliberately read-only and
// side-effect-free: no headless vendor run, no permission grant/revert,
// no workspace write, no real pending-resume token consumed — a user can
// mash this as many times as they like. The one piece of real state it
// reads (not writes) is the live vendor registry and routing config, so
// the reference examples reflect the operator's actual setup (their real
// vendor names, their actually-configured routing override commands)
// rather than hardcoded ones that might not even apply here.
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

// playgroundDelegateResult.Kind mirrors the branches ResolveDelegate
// itself takes (internal/vendors/resume.go): "allow"/"deny" are the
// reserved control-flow template names, "interactive" is any other
// template whose Mode is "interactive" (opens a real window, nothing
// relayed back), "run" is a normal headless delegate call, and
// "unknown_template" is what a real request gets too if ":template"
// names something the vendor was never actually given (ParseDelegateCommand
// only checks the trailing-flag *shape*, not that the template exists).
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
	// ConfiguredCommands are every trigger.commands entry from this
	// operator's own enabled "explicit" routing rules, deduplicated — the
	// real universe MatchExplicitCommand was checked against, not a
	// hardcoded /local /cloud pair that may not even be configured here.
	ConfiguredCommands []string `json:"configuredCommands"`
}

func enabledVendorNames(reg vendors.Registry) []string {
	names := []string{}
	for _, v := range reg.Enabled() {
		names = append(names, v.Name)
	}
	return names
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
