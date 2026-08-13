package router

import (
	"fmt"
	"sort"
	"strings"

	"github.com/glider-ai/glider/internal/config"
)

// LintFinding is one doubt in the config that LintConfig found. It gives
// advice only. It is never a hard error, and the structural tests in
// validate.go do give hard errors. An operator can make one rule hide a
// different rule on purpose.
type LintFinding struct {
	Severity string // always "warning" today — a fixed field in case a harder class shows up later
	Message  string
	Rules    []string // names of the rules involved, first is the one that wins
	// Example is a true message that would cause the doubt, when LintConfig can
	// make one. It is empty when LintConfig cannot make one with confidence: this
	// method cannot decide the overlap of two regular expressions or the behaviour
	// of a script rule.
	//
	// Give the example to RouteExplain. Then you see the decision for a true
	// message, and you do not have to believe the warning.
	Example string
}

// LintConfig examines a routing config for a doubt that it can prove. It does
// not solve the general problem of two regular expressions that overlap. It
// finds two enabled rules that would compete on exactly the same trigger.
//
// It leaves each condition that it cannot decide, which includes the overlap of
// two patterns and the behaviour of a script rule. For a tool that gives advice
// only, and that no person must obey, an incorrect finding is worse than
// silence.
func LintConfig(routing config.RoutingConfig) []LintFinding {
	var findings []LintFinding
	findings = append(findings, lintDuplicateExplicitCommands(routing)...)
	findings = append(findings, lintDuplicateTriggerShape(routing)...)
	return findings
}

func lintDuplicateExplicitCommands(routing config.RoutingConfig) []LintFinding {
	type owner struct {
		name     string
		priority int
	}
	byCommand := map[string][]owner{}
	for _, r := range routing.Rules {
		if !r.IsEnabled() || strings.ToLower(strings.TrimSpace(r.Trigger.Type)) != "explicit" {
			continue
		}
		for _, c := range r.Trigger.Commands {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			byCommand[c] = append(byCommand[c], owner{name: r.Name, priority: r.Priority})
		}
	}

	commands := make([]string, 0, len(byCommand))
	for c := range byCommand {
		commands = append(commands, c)
	}
	sort.Strings(commands) // deterministic output regardless of map iteration order

	var findings []LintFinding
	for _, c := range commands {
		owners := byCommand[c]
		if len(owners) < 2 {
			continue
		}
		sort.SliceStable(owners, func(i, j int) bool { return owners[i].priority > owners[j].priority })
		names := make([]string, len(owners))
		for i, o := range owners {
			names[i] = o.name
		}
		findings = append(findings, LintFinding{
			Severity: "warning",
			Message: fmt.Sprintf("%q is triggered by %d rules (%s) — %q wins on priority, the rest never fire for this command",
				c, len(owners), strings.Join(names, ", "), names[0]),
			Rules:   names,
			Example: c + " example message",
		})
	}
	return findings
}

// lintDuplicateTriggerShape shows each pair of enabled rules with a trigger
// that is the same, byte for byte. The type, the priority and the data are all
// equal: the same pattern, or the same operator and value, or the same script
// file. The evaluation loop returns at the first rule that agrees, and that
// loop uses the sequence of priority. Therefore the code can never reach the
// second rule.
//
// This function excludes the type "explicit" on purpose.
// lintDuplicateExplicitCommands already covers that type with more detail: it
// examines each command that two rules share, and not only two lists of
// commands that are fully equal.
//
// It also excludes "always" and composer_wrapup. The shape of their trigger has
// no data that makes one rule different from another. Therefore each pair of
// enabled rules of those types would appear to collide, and that is not
// information of use.
func lintDuplicateTriggerShape(routing config.RoutingConfig) []LintFinding {
	type key struct {
		kind     string
		priority int
		shape    string
	}
	byShape := map[key][]string{}
	for _, r := range routing.Rules {
		if !r.IsEnabled() {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(r.Trigger.Type))
		var shape string
		switch kind {
		case "regex":
			shape = r.Trigger.Pattern
		case "context_size":
			shape = fmt.Sprintf("%s:%d", r.Trigger.Operator, r.Trigger.Value)
		case "script":
			shape = r.Trigger.File
		default:
			continue
		}
		k := key{kind: kind, priority: r.Priority, shape: shape}
		byShape[k] = append(byShape[k], r.Name)
	}

	keys := make([]key, 0, len(byShape))
	for k := range byShape {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		if keys[i].shape != keys[j].shape {
			return keys[i].shape < keys[j].shape
		}
		return keys[i].priority < keys[j].priority
	})

	var findings []LintFinding
	for _, k := range keys {
		names := byShape[k]
		if len(names) < 2 {
			continue
		}
		findings = append(findings, LintFinding{
			Severity: "warning",
			Message: fmt.Sprintf("rules %s have identical %s triggers at the same priority (%d) — only %q can ever be reached",
				strings.Join(names, ", "), k.kind, k.priority, names[0]),
			Rules: names,
		})
	}
	return findings
}
