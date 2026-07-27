package ngl

import "strings"

func init() {
	RegisterDelegateRenderer(agyDelegateRenderer{})
}

// agyDelegateRenderer is near-identity: agy has no --output-format flag at
// all (confirmed in adapter.go/denial.go's own research notes) — its
// headless "-p" stdout is already plain prose, not NDJSON needing
// unwrapping the way claude/cursor-agent's stream-json output does.
// Registered explicitly anyway (rather than leaving agy with no renderer
// at all) so a caller's "no renderer for this vendor" case genuinely means
// "unconfirmed," not "confirmed already clean" — those are different
// facts, and only the former should feel like a gap worth investigating
// later. No specific boilerplate-stripping pattern is applied because none
// has been confirmed live yet; add one here if a real prefix/suffix noise
// pattern is ever actually observed, not speculatively.
type agyDelegateRenderer struct{}

func (agyDelegateRenderer) Vendor() string { return "agy" }

func (agyDelegateRenderer) Render(raw []byte) (string, bool) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", false
	}
	return text, true
}
