package router

import (
	"fmt"
	"sort"
	"strings"

	"github.com/glider-ai/glider/internal/config"
)

// LintFinding is one config-time ambiguity LintConfig detected — advisory
// only, never a hard error the way validate.go's structural checks are,
// since an operator may have configured the shadowing on purpose.
type LintFinding struct {
	Severity string // always "warning" today — a fixed field in case a harder class shows up later
	Message  string
	Rules    []string // names of the rules involved, first is the one that wins
	// Example is a best-effort concrete message that would trigger the
	// ambiguity; empty when LintConfig can't safely synthesize one (regex
	// pattern overlap and script rule behavior aren't decidable this way).
	// Feed it to RouteExplain to see the tie-break happen for a real
	// message instead of taking the warning on faith.
	Example string
}

// LintConfig checks a routing config for ambiguity it can prove without
// solving an arbitrary-regex-overlap problem: two enabled rules that would
// race on the exact same trigger. Anything it can't decide (regex pattern
// overlap, script rule behavior) it leaves alone — a false positive is
// worse than silence for an advisory tool nobody's forced to act on.
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

// lintDuplicateTriggerShape flags enabled rules whose trigger is byte-for-
// byte identical (same type, same priority, same pattern/operator+value/
// script file) — the lower-named one can never be reached since the
// higher-priority evaluation loop already returns on the first match.
// "explicit" is deliberately excluded here: lintDuplicateExplicitCommands
// already covers it with finer granularity (per shared command, not just
// fully-identical command lists). "always"/composer_wrapup are excluded
// too — their trigger shape carries no distinguishing data, so every pair
// of enabled rules of that type would trivially "collide," which isn't
// useful information.
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
