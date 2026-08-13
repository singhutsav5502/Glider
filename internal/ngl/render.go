package ngl

// DelegateRenderer changes the raw output of a vendor from a run with no
// console into a clean reply for a person. That output is the exact bytes that
// make vendors.RunResult.Text. This is the method for usual work, and the
// alternative is to relay that raw text with no change.
//
// This is a SEPARATE interface from OriginAdapter in origin.go, and this is on
// purpose. The code does not use one interface for both.
//
// OriginAdapter recognizes the LIVE NETWORK traffic of a vendor, and it
// answers that traffic. DelegateRenderer formats the CAPTURED STDOUT OF A
// SUBPROCESS, from a delegate run with no console.
//
// The source of the data is different. The question is different: "what did
// this process write?" against "is this request the traffic of this vendor?".
// The two interfaces share no contract.
//
// This exists because of a true problem that a person reported on 2026-07-28.
// The output of claude and of cursor-agent from "-p --output-format
// stream-json" is raw NDJSON. It has each internal event line: system/init,
// the parts of the assistant message, the tool calls, and each other line. It
// is not the final answer alone.
//
// To relay that full content as a delegate reply gives text that a person
// cannot read in usual work. But it is exactly the content that a person needs
// to find the cause of a problem.
//
// A vendor with no registered renderer gives ok=false. A vendor with raw bytes
// that do not agree with the expectation of this renderer also gives ok=false.
// A caller must then use the raw text, and it must add a note that a person can
// see. It must not make the change with no message.
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
