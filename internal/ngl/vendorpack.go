package ngl

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// VendorPack is the catalog of tools for one vendor, and it is data. Refer to
// planning/agent_cli_interop.md §1, "vendor packs, not Go switch statements".
// To add the name of a tool, or a fourth CLI, is a new entry in YAML or a new
// file. It is never a build of the program again.
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
	// Tags has no fixed set of values. One tool can have more than one tag. The
	// values are the best data that a person has, and Tags is not a closed enum.
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

// CanonicalArg changes the name of an arg field of a vendor into a standard
// name, for one tool. A consumer that reads many vendors asks for that
// standard name.
//
// It returns ok=false when the pack does not contain the tool, or when no
// alias agrees. A caller must then read the raw args of the vendor directly.
// A caller must not read this as an error.
//
// The note "fold-then-exact" on LookupToolNameMapping in agent_cli_interop.md
// applies here in the same way. A table of aliases is the best data that a
// person has. It is not the authority.
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

// IsConfirmed says if a live test confirmed the tool for this vendor. It is
// never true for a tool that the pack does not contain.
//
// A tool that the pack does not contain is not "not confirmed". It is unknown.
// A caller must then apply UnknownToolPolicy.
func (p *VendorPack) IsConfirmed(tool string) bool {
	spec, ok := p.Tools[tool]
	return ok && spec.Confirmed
}

// AnnotateToolCall adds the Confirmed data and the Tags of this pack to a
// ToolCall that the code already parsed. The key is tc.Name.
//
// For a tool that the pack does not contain, which is unknown and not only
// "not confirmed", the code leaves Confirmed=false and Tags=nil. A caller that
// implements UnknownToolPolicy decides the meaning of that condition. This
// method never makes an estimate.
//
// The parsing code, in adapter_*.go, never calls this method. To keep the lookup
// in the pack as a separate and explicit step is what lets each adapter stay
// easy to test with no pack. Refer to the tests of the adapters: no test there
// loads a VendorPack.
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
