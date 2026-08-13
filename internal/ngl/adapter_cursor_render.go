package ngl

import "strings"

func init() {
	RegisterDelegateRenderer(cursorDelegateRenderer{})
}

// cursorDelegateRenderer cleans up cursor-agent's headless "-p --output-format
// stream-json" output the same way claudeDelegateRenderer does for Claude
// Code — same NDJSON shape, same terminal {"type":"result","result":"..."}
// line (CursorResultEvent, adapter_cursor.go, confirmed live to share the
// exact field names with Claude's own ClaudeResultEvent) — reuses
// lastResultEvent (adapter_claude_render.go) rather than duplicating the
// line-walking logic for a genuinely identical wire shape.
type cursorDelegateRenderer struct{}

func (cursorDelegateRenderer) Vendor() string { return "cursor-agent" }

func (cursorDelegateRenderer) Render(raw []byte) (string, bool) {
	result, ok := lastResultEvent(raw)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(result.Result)
	if text == "" {
		return "", false
	}
	return text, true
}
