package ngl

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// VendorPack is the data-driven tool catalog for one vendor, per
// planning/agent_cli_interop.md §1 ("vendor packs, not Go switch
// statements"): adding a tool name, or a fourth CLI, is a new YAML entry
// or file, never a recompile.
type VendorPack struct {
	Vendor             string              `yaml:"vendor"`
	ObservedCLIVersion string              `yaml:"observed_cli_version"`
	Tools              map[string]ToolSpec `yaml:"tools"`
	UnknownToolPolicy  string              `yaml:"unknown_tool_policy"`
	ConfirmedPolicy    string              `yaml:"confirmed_policy"`
}

// ToolSpec describes one tool entry in a vendor pack.
type ToolSpec struct {
	// Confirmed means actually invoked live and decoded, not just
	// declared on the wire (a real distinction — see agy's ~40-entry
	// wire-declared catalog vs. its ~7-tool live surface, agent_cli_interop.md).
	Confirmed bool `yaml:"confirmed"`
	// Tags are open-ended, multi-valued, best-effort — not a closed enum.
	Tags []string `yaml:"tags"`
	// Args maps a canonical arg name to the vendor's own field name
	// alias(es) — e.g. canonical "path" -> vendor's "TargetFile".
	Args map[string][]string `yaml:"args"`
	// DiffView names which EditViews field this tool's raw args populate
	// natively (e.g. "range_replace", "whole_file") — empty for non-edit tools.
	DiffView string `yaml:"diff_view"`
}

// LoadVendorPack reads one vendor pack YAML file.
func LoadVendorPack(path string) (*VendorPack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ngl: read vendor pack %s: %w", path, err)
	}
	var pack VendorPack
	if err := yaml.Unmarshal(data, &pack); err != nil {
		return nil, fmt.Errorf("ngl: parse vendor pack %s: %w", path, err)
	}
	return &pack, nil
}

// CanonicalArg resolves a vendor's own arg field name to the canonical
// name a cross-vendor consumer asked for, for a given tool. Returns
// ok=false if the tool isn't in the pack or no alias matches — callers
// should fall back to reading the vendor's raw args directly rather than
// treat this as an error (agent_cli_interop.md's "fold-then-exact" note on
// LookupToolNameMapping applies the same way here: alias tables are
// best-effort, not authoritative).
func (p *VendorPack) CanonicalArg(tool, vendorFieldName string) (string, bool) {
	spec, ok := p.Tools[tool]
	if !ok {
		return "", false
	}
	for canonical, aliases := range spec.Args {
		for _, a := range aliases {
			if a == vendorFieldName {
				return canonical, true
			}
		}
	}
	return "", false
}

// IsConfirmed reports whether tool is confirmed live for this vendor —
// never true for a tool absent from the pack (an absent tool is not
// "unconfirmed", it's unknown, and callers should apply UnknownToolPolicy).
func (p *VendorPack) IsConfirmed(tool string) bool {
	spec, ok := p.Tools[tool]
	return ok && spec.Confirmed
}

// AnnotateToolCall attaches this pack's Confirmed/Tags metadata to an
// already-parsed ToolCall, keyed by tc.Name. A tool absent from the pack
// (unknown, not just unconfirmed) is left with Confirmed=false, Tags=nil —
// callers implementing UnknownToolPolicy decide what that means, this
// method never guesses. Parsing (adapter_*.go) never calls this itself —
// keeping pack-lookup a separate, explicit step is what lets every adapter
// stay testable without a pack loaded at all (see the adapter tests, none
// of which load a VendorPack).
func (p *VendorPack) AnnotateToolCall(tc *ToolCall) {
	if p == nil || tc == nil {
		return
	}
	spec, ok := p.Tools[tc.Name]
	if !ok {
		return
	}
	tc.Confirmed = spec.Confirmed
	tc.Tags = append([]string(nil), spec.Tags...)
}
