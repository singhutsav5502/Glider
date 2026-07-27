package ngl

// DelegateRenderer formats a vendor's raw headless-run output (the exact
// bytes vendors.RunResult.Text is built from) into a clean, human-facing
// reply — the day-to-day counterpart to relaying that raw text verbatim.
// This is deliberately a SEPARATE interface from OriginAdapter (origin.go),
// not a reused/overloaded one: OriginAdapter recognizes and replies to a
// vendor's own LIVE NETWORK traffic; DelegateRenderer formats a vendor's
// CAPTURED SUBPROCESS STDOUT from a headless delegate run — different data
// source, different question ("what did this process print" vs "is this
// request this vendor's own traffic"), no shared contract between them.
//
// Exists because of a real, reported problem (2026-07-28): Claude's and
// cursor-agent's headless "-p --output-format stream-json" output is raw
// NDJSON — every internal event line (system/init, assistant deltas, tool
// calls, the works), not just the final answer — and relaying that whole
// blob as a delegate reply is unreadable for day-to-day use, even though
// it's exactly what a debugging session wants to see. A vendor with no
// registered renderer, or whose raw bytes don't parse the way this
// renderer expects, means ok=false — callers must fall back to the raw
// text (with a visible note, not a silent swap) rather than guess at a
// clean answer that might not be there.
type DelegateRenderer interface {
	// Vendor is this renderer's vendor name, matching vendors.Vendor.Name.
	Vendor() string

	// Render parses raw (a completed headless run's captured stdout) and
	// returns the clean, human-facing final answer text.
	Render(raw []byte) (clean string, ok bool)
}

var delegateRenderers []DelegateRenderer

// RegisterDelegateRenderer adds a to the registry consulted by
// ResolveDelegateRenderer. Called from each renderer's own init(); never
// call this from shared dispatch code — same discipline as
// RegisterOriginAdapter (origin.go).
func RegisterDelegateRenderer(r DelegateRenderer) {
	delegateRenderers = append(delegateRenderers, r)
}

// ResolveDelegateRenderer returns the registered renderer for vendorName,
// or nil if none is registered — a vendor with no renderer yet is a
// normal, valid state (its raw output stays the reply, same as today),
// not an error.
func ResolveDelegateRenderer(vendorName string) DelegateRenderer {
	for _, r := range delegateRenderers {
		if r.Vendor() == vendorName {
			return r
		}
	}
	return nil
}
