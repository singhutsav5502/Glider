package router

import (
	"path"
	"strings"

	"github.com/glider-ai/glider/internal/config"
)

// ToolFollowupSignals are inputs for per-tool-step re-decide after a parent turn.
type ToolFollowupSignals struct {
	ParentRoute     string // "cloud" | "local" | ""
	ToolNames       []string
	EstimatedTokens int
	ExplicitCloud   bool
	ExplicitLocal   bool
}

// ToolFollowupVerdict is the routing preference for one tool step / tool group.
type ToolFollowupVerdict struct {
	PreferLocal bool
	Reason      string
}

// DecideToolFollowup applies routing.tool_followup policy.
// Priority within this layer (caller still applies explicit /cloud first):
// denylist → allowlist reevaluate → inherit parent → default cloud.
func DecideToolFollowup(cfg config.ToolFollowupConfig, sig ToolFollowupSignals) ToolFollowupVerdict {
	if sig.ExplicitCloud {
		return ToolFollowupVerdict{PreferLocal: false, Reason: "explicit_cloud"}
	}
	if sig.ExplicitLocal {
		return ToolFollowupVerdict{PreferLocal: true, Reason: "explicit_local"}
	}
	if !cfg.Enabled {
		return ToolFollowupVerdict{PreferLocal: false, Reason: "tool_followup_disabled"}
	}

	parentLocal := strings.EqualFold(strings.TrimSpace(sig.ParentRoute), "local")
	startLocal := false
	startReason := "default_cloud"
	if cfg.InheritParentOrDefault() && sig.ParentRoute != "" {
		startLocal = parentLocal
		if startLocal {
			startReason = "inherit_parent_local"
		} else {
			startReason = "inherit_parent_cloud"
		}
	}

	if !cfg.ReevaluateOrDefault() {
		return ToolFollowupVerdict{PreferLocal: startLocal, Reason: startReason}
	}

	names := sig.ToolNames
	if len(names) == 0 {
		// No tool names → cannot safely offload; keep inherit / default.
		return ToolFollowupVerdict{PreferLocal: startLocal, Reason: startReason + "+unknown_tools"}
	}

	if hit := firstToolMatch(names, cfg.CloudToolDenylist); hit != "" {
		return ToolFollowupVerdict{PreferLocal: false, Reason: "denylist:" + hit}
	}

	allow := cfg.LocalToolAllowlist
	if len(allow) == 0 {
		// Reevaluate on but empty allowlist: no local offload beyond inherit.
		return ToolFollowupVerdict{PreferLocal: startLocal, Reason: startReason + "+no_allowlist"}
	}
	for _, n := range names {
		if !toolNameAllowed(n, allow) {
			return ToolFollowupVerdict{PreferLocal: false, Reason: "not_allowlisted:" + n}
		}
	}
	return ToolFollowupVerdict{PreferLocal: true, Reason: "allowlist"}
}

// ToolsAllLocalAllowed reports whether every tool name is allowlisted and none
// are denylisted — used by Path A to skip tools→cloud when safe.
func ToolsAllLocalAllowed(cfg config.ToolFollowupConfig, toolNames []string) bool {
	if !cfg.Enabled || !cfg.ReevaluateOrDefault() || len(toolNames) == 0 {
		return false
	}
	if hit := firstToolMatch(toolNames, cfg.CloudToolDenylist); hit != "" {
		return false
	}
	if len(cfg.LocalToolAllowlist) == 0 {
		return false
	}
	for _, n := range toolNames {
		if !toolNameAllowed(n, cfg.LocalToolAllowlist) {
			return false
		}
	}
	return true
}

func firstToolMatch(names, patterns []string) string {
	for _, n := range names {
		if toolNameAllowed(n, patterns) {
			return n
		}
	}
	return ""
}

// toolNameAllowed matches case-insensitive exact name or path.Match pattern.
func toolNameAllowed(name string, patterns []string) bool {
	n := strings.TrimSpace(name)
	if n == "" || len(patterns) == 0 {
		return false
	}
	nl := strings.ToLower(n)
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		pl := strings.ToLower(p)
		if pl == nl {
			return true
		}
		if ok, _ := path.Match(pl, nl); ok {
			return true
		}
	}
	return false
}
