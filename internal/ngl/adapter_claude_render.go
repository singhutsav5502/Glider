package ngl

import (
	"bytes"
	"encoding/json"
	"strings"
)

func init() {
	RegisterDelegateRenderer(claudeDelegateRenderer{})
}

// claudeDelegateRenderer takes the output of Claude Code from "-p
// --output-format stream-json" and gives only the final answer.
//
// That output is raw NDJSON. It has the system/init line, each part of the
// assistant message, each tool_use and each tool_result, and each other line.
//
// The Result field of the last "result" line is already the final text that
// Claude made. Refer to ClaudeResultEvent in adapter_claude.go.
//
// Therefore this renderer does not make an answer again from the full record. It
// only finds the one line that already IS the answer.
type claudeDelegateRenderer struct{}

func (claudeDelegateRenderer) Vendor() string { return "claude" }

func (claudeDelegateRenderer) Render(raw []byte) (string, bool) {
	result, ok := lastResultEvent(raw)
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(result.Result)
	if text == "" {
		// A result event with no text occurs with some shapes of an error. It has no
		// clean content to show. The raw record is more of use than an empty reply.
		return "", false
	}
	return text, true
}

// lastResultEvent walks raw stream-json NDJSON (shared shape between
// claude and cursor-agent — both emit a terminal {"type":"result",
// "result":"<final text>",...} line, confirmed independently for each in
// adapter_claude.go's ClaudeResultEvent and adapter_cursor.go's
// CursorResultEvent) and returns the LAST such line found — "last," not
// "first," for the same defensive reason ngl.go's LastUserInstruction
// takes the last user-role message: a well-formed run has exactly one,
// but a stream that somehow carried more than one should prefer the most
// recent, not the first.
func lastResultEvent(raw []byte) (resultLine, bool) {
	var last resultLine
	found := false
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &probe) != nil || probe.Type != "result" {
			continue
		}
		var ev resultLine
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		last, found = ev, true
	}
	return last, found
}

// resultLine is the field subset both ClaudeResultEvent and
// CursorResultEvent share exactly (same JSON field names, confirmed live
// for each independently) — kept as its own minimal type here rather than
// importing either vendor-specific struct, since this file's job is
// exactly this one field, not the full result-event shape either of those
// types model for other callers.
type resultLine struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
}
