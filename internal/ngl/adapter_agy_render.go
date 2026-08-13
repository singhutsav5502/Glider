package ngl

import "strings"

func init() {
	RegisterDelegateRenderer(agyDelegateRenderer{})
}

// agyDelegateRenderer changes almost nothing. agy has no --output-format flag.
// The research notes in adapter.go and denial.go confirm this. Therefore its
// stdout from "-p" with no console is already plain prose. It is not NDJSON that
// needs work, and the stream-json output of claude and cursor-agent does need
// that work.
//
// This code registers the renderer for agy in any condition, and it does not
// leave agy with no renderer. The cause: a caller must see the difference
// between two facts. "This vendor has no renderer" must truly mean "no person
// confirmed the output of this vendor". It must not mean "a person confirmed
// that the output is already clean".
//
// Those are two different facts. Only the first one is a gap that a person must
// examine later.
//
// This code applies no pattern to remove text at the start or at the end,
// because no live test confirmed such a pattern. Add one here if a person truly
// observes noise at the start or the end. Do not add one from an estimate.
type agyDelegateRenderer struct{}

func (agyDelegateRenderer) Vendor() string { return "agy" }

func (agyDelegateRenderer) Render(raw []byte) (string, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", false
	}
	return text, true
}
