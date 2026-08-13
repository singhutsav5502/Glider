package vendors

import (
	"bytes"
	"encoding/json"

	"github.com/glider-ai/glider/internal/ngl"
)

// VendorAdapter is the one interface point for each behaviour of one CLI in
// the execution layer. RunWithOptions and ResolveDelegate call through a
// VendorAdapter that they find by name. In each other respect they do not
// know which vendor they operate on.
//
// To add a fourth CLI means to write one adapter here. To change how an
// existing CLI finds a refusal, or gives a permission for a resume, means to
// change one adapter here. It never means a test on vendor.Name inside the
// shared control flow.
//
// This is the same principle as "vendor packs, not Go switch statements" in
// internal/ngl. Refer to planning/agent_cli_interop.md §1. Here it applies
// past the parsing of the wire format, and it also applies to the changes
// that occur at the time of execution. A design review on 2026-07-26 said
// this directly, while a person corrected the resume mechanism of agy: "all
// these things are per adapter... need to be exposed as interface points so
// the main core remains the same for all."
type VendorAdapter interface {
	// DetectDenials takes the refused permissions out of the raw stdout and
	// stderr of a run with no console that is complete. A nil result means "the
	// code found no refusal". Each adapter implements this function. An adapter
	// for a vendor with no detector implements a version that does nothing.
	DetectDenials(stdout, stderr []byte) []Denial

	// ExtractSessionID opportunistically recovers a session/conversation
	// id from a run's output, for a later resume. "" means none available
	// (a vendor whose format carries no such id, or extraction failed).
	ExtractSessionID(stdout []byte) string

	// GrantResumePermission makes the change with a small scope, for one vendor,
	// that lets a resume succeed where the original run got a refusal. That
	// change is OUTSIDE the argv of the resume. For agy it is a permission in
	// settings.json, because the resume of agy is a plain --continue with no
	// flag for one tool.
	//
	// Most vendors need none of this. They give their permission for each
	// refusal through ExtraResumeArgs below, and they return a revert function
	// that does nothing and a nil error.
	//
	// The code always calls the revert function that this function returns,
	// after the attempt to resume. It does this after a success and after a
	// failure. Therefore each change stays inside exactly one resume call.
	//
	// cwd is the workspace directory that the resume operates in. It can be
	// empty when the code found no directory. agy needs it, because agy must
	// also give the permission in a project config for one directory, when such
	// a config exists. That config has control over each global permission. A
	// live test on 2026-07-26 confirmed this. Refer to agy_grant.go.
	GrantResumePermission(v Vendor, cwd string, denials []Denial) (revert func() error, err error)

	// ExtraResumeArgs returns the additional CLI args to add to the "resume"
	// CommandTemplate of the vendor, for this one set of refusals. It returns
	// nil when the vendor has no such mechanism.
	//
	// Added 2026-07-30, to close a true gap. resolveAllow always sent the fixed
	// args of the resume template again. A vendor had no method to limit the new
	// attempt to the tools that the person refused. For claude that meant that
	// each "allow" ran the same prompt again against the same permission state,
	// and it gave no permission.
	//
	// An earlier comment on GrantResumePermission of claudeAdapter said that
	// "--allowedTools ... is sufficient on its own" as one part of the resume
	// template. That statement was false: the registered template never had that
	// flag. Refer to the "resume" entry for claude in
	// configs/vendor_candidates.yaml.
	//
	// The implementation for claude makes "--allowedTools <tool names, joined by
	// commas>" from the refusals. A live test with `claude --help` confirmed the
	// flag: "--allowedTools, --allowed-tools <tools...> Comma or space-separated
	// list of tool names to allow".
	//
	// cursor-agent has no equal flag. A live test with `cursor-agent --help`
	// confirmed this. It has only -f, --force and --yolo. Those flags permit
	// each tool, and not only the tool that the person refused. That is a much
	// larger increase of permission than this method must give. Therefore
	// cursorAgentAdapter returns nil here. noopAdapter and agyAdapter also
	// return nil, and the GrantResumePermission of agy already makes its change
	// for the resume.
	ExtraResumeArgs(denials []Denial) []string

	// ExtractEditViews reads the raw stdout of a run that is complete. It then
	// makes the standard EditViews of NGL, when the output of that vendor has
	// sufficient structure.
	//
	// This gives the function "show the file diffs through NGL". That function
	// is the full cause of planning/permission_relay_design.md: to observe the
	// result, and not only to relay a permission.
	//
	// It returns ok=false, and not an error, for a run that made no edit. It
	// also returns ok=false for a vendor with output that has no structured
	// diff. The mode of agy with no console gives prose only, and this is
	// confirmed: agy has no --output-format flag. Therefore agyAdapter always
	// returns ok=false here. That is an honest limit of the wire format of this
	// vendor, and it is not a defect to hide.
	ExtractEditViews(stdout []byte) (views ngl.EditViews, ok bool)

	// WrapResumePrompt lets the adapter of a vendor write the resume prompt again,
	// for the known behaviour of its own model on a call that resumes.
	//
	// It does nothing, and it returns the prompt with no change, for each vendor
	// with a resume that always completes the original request. claude and
	// cursor-agent are such vendors, and live tests confirmed both.
	//
	// Added 2026-07-26 for agy only. The resume of agy always removes the
	// permission gate, but its model frequently answers with a description of the
	// directory, and it does not act. A live test confirmed this, and a person
	// reproduced it 6 times in sequence. This is a mitigation with the prompt.
	WrapResumePrompt(prompt string) string
}

// noopAdapter is the alternative for each vendor name with no registered
// adapter. Each of its methods is safe and does nothing. Therefore a caller
// never has to ask "does an adapter exist for this vendor" before it calls
// one.
type noopAdapter struct{}

func (noopAdapter) DetectDenials(stdout, stderr []byte) []Denial { return nil }
func (noopAdapter) ExtractSessionID(stdout []byte) string        { return "" }
func (noopAdapter) GrantResumePermission(Vendor, string, []Denial) (func() error, error) {
	return func() error { return nil }, nil
}
func (noopAdapter) ExtraResumeArgs(denials []Denial) []string { return nil }
func (noopAdapter) WrapResumePrompt(prompt string) string     { return prompt }
func (noopAdapter) ExtractEditViews(stdout []byte) (ngl.EditViews, bool) {
	return ngl.EditViews{}, false
}

var vendorAdapters = map[string]VendorAdapter{
	"cursor-agent": cursorAgentAdapter{},
	"claude":       claudeAdapter{},
	"agy":          agyAdapter{},
}

// adapterFor finds the registered VendorAdapter for a vendor name. It uses
// noopAdapter when it finds none. This is the one position that must know the
// full set of vendor names.
func adapterFor(vendorName string) VendorAdapter {
	if a, ok := vendorAdapters[vendorName]; ok {
		return a
	}
	return noopAdapter{}
}

// sessionIDFromJSONLines is the shared ExtractSessionID for each vendor with
// a stream-json output that gives session_id on each line. claude and
// cursor-agent do this, and a live test confirmed both. This is a true
// similarity between vendors, and it is not logic for one vendor. Therefore
// it is here one time, and not in each adapter.
func sessionIDFromJSONLines(stdout []byte) string {
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var probe struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(line, &probe); err == nil && probe.SessionID != "" {
			return probe.SessionID
		}
	}
	return ""
}
