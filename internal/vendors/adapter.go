package vendors

import (
	"bytes"
	"encoding/json"

	"github.com/glider-ai/glider/internal/ngl"
)

// VendorAdapter is the one interface point every per-CLI quirk in the
// execution layer must go through — RunWithOptions/ResolveDelegate call
// through a VendorAdapter looked up by name and are otherwise blind to
// which vendor they're talking to. Adding a fourth CLI, or changing how an
// existing one detects denials or grants resume permission, means writing
// or editing one adapter here, never branching on vendor.Name inside
// shared control flow. This mirrors internal/ngl's "vendor packs, not Go
// switch statements" principle (planning/agent_cli_interop.md §1),
// extended past wire-format parsing to execution-time side effects too —
// stated directly by design review (2026-07-26) while fixing agy's resume
// mechanism: "all these things are per adapter... need to be exposed as
// interface points so the main core remains the same for all."
type VendorAdapter interface {
	// DetectDenials extracts permission denials from a completed headless
	// run's raw stdout/stderr. nil means "no denials found" — every
	// adapter implements this, even a trivial no-op for a vendor with no
	// detector wired up yet.
	DetectDenials(stdout, stderr []byte) []Denial

	// ExtractSessionID opportunistically recovers a session/conversation
	// id from a run's output, for a later resume. "" means none available
	// (a vendor whose format carries no such id, or extraction failed).
	ExtractSessionID(stdout []byte) string

	// GrantResumePermission performs whatever scoped, vendor-specific side
	// effect is needed to let a resume invocation succeed where the
	// original run was denied — a side effect OUTSIDE the resume argv
	// itself (agy: a settings.json grant, since its resume is a bare
	// --continue with no per-tool flag at all). Most vendors need none of
	// this — their per-denial scoping happens through ExtraResumeArgs
	// instead (see below) — and return a no-op revert with a nil error.
	// The returned revert func is always called after the resume attempt,
	// success or failure, so any side effect stays scoped to exactly one
	// resume call. cwd is the resolved workspace directory the resume
	// will run in (may be "" if none was resolved) — agy needs this to
	// also grant into a directory-specific project config when one
	// exists, since it takes precedence over any global grant (confirmed
	// live 2026-07-26, see agy_grant.go).
	GrantResumePermission(v Vendor, cwd string, denials []Denial) (revert func() error, err error)

	// ExtraResumeArgs returns extra CLI args to append to the vendor's
	// "resume" CommandTemplate for this specific set of denials, or nil
	// if the vendor has no such mechanism. Added 2026-07-30 to close a
	// real gap: resolveAllow used to unconditionally reissue the resume
	// template's static args, with no way for a vendor to scope the retry
	// to just the denied tool(s) — for claude that meant every "allow"
	// click re-ran the identical prompt against the identical permission
	// state instead of actually granting anything (claudeAdapter's own
	// GrantResumePermission doc used to claim "--allowedTools ... is
	// sufficient on its own" as part of the resume template — false; the
	// registered template has never included it, see
	// configs/vendor_candidates.yaml's claude "resume" entry). claude's
	// implementation builds "--allowedTools <comma-joined tool names>"
	// from denials — confirmed live via `claude --help`: "--allowedTools,
	// --allowed-tools <tools...> Comma or space-separated list of tool
	// names to allow". cursor-agent has no equivalent (confirmed live via
	// `cursor-agent --help`: only -f/--force/--yolo, which grants
	// everything, not just the denied tool — a materially broader
	// escalation than this method is meant for) so cursorAgentAdapter
	// returns nil here, same as noopAdapter and agyAdapter (agy's
	// GrantResumePermission already covers its resume side effect).
	ExtraResumeArgs(denials []Denial) []string

	// ExtractEditViews parses a completed run's raw stdout into NGL's
	// canonical EditViews, when the vendor's output contains enough
	// structure to do so — the "show the file diffs through NGL" feature
	// (planning/permission_relay_design.md's whole reason for existing:
	// observe outcomes, not just relay permission). ok=false (not an
	// error) for a run that made no edit, or whose vendor's headless
	// output carries no structured diff at all — agy's headless mode is
	// prose-only (confirmed: no --output-format flag exists for it), so
	// agyAdapter always returns ok=false here; that's an honest limit of
	// this vendor's headless wire format, not a bug to paper over.
	ExtractEditViews(stdout []byte) (views ngl.EditViews, ok bool)

	// WrapResumePrompt lets a vendor's adapter reframe the resume prompt
	// for its own model's known behavior on a resumed call — a no-op
	// (returns prompt unchanged) for vendors whose resume already reliably
	// completes the original request (claude, cursor-agent, both confirmed
	// live). Added 2026-07-26 for agy specifically: its resume reliably
	// clears the permission gate but the model often responds by
	// describing the directory instead of acting (confirmed live,
	// reproduced 6 consecutive times) — a prompt-engineering mitigation,
	// not a guarantee; see planning/ngl_and_adapters.md §9 for the honest
	// limits and why the real fix is Path B, not this.
	WrapResumePrompt(prompt string) string
}

// noopAdapter is the fallback for any vendor name without a registered
// adapter — every method is a safe no-op, so callers never have to check
// "is there an adapter for this vendor" before calling one.
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

// adapterFor looks up the registered VendorAdapter for a vendor name,
// falling back to noopAdapter — the single place that ever needs to know
// the full set of known vendor names.
func adapterFor(vendorName string) VendorAdapter {
	if a, ok := vendorAdapters[vendorName]; ok {
		return a
	}
	return noopAdapter{}
}

// sessionIDFromJSONLines is the shared ExtractSessionID implementation for
// every vendor whose stream-json output echoes session_id on each line
// (claude, cursor-agent — confirmed live) — a genuine cross-vendor
// commonality, not per-vendor logic, so it lives once here rather than
// duplicated in each adapter.
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
