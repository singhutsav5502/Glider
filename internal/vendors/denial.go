package vendors

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/glider-ai/glider/internal/ngl"
)

// DetectDenials inspects a completed headless run's raw stdout/stderr for
// permission denials, dispatched through the registered VendorAdapter for
// vendorName (see adapter.go) — the core (RunWithOptions) is blind to
// per-vendor detection logic beyond this lookup. A vendor without a
// registered adapter returns nil, nil — not an error — so RunWithOptions
// never fails just because denial detection isn't implemented for some
// vendor.
func DetectDenials(vendorName string, stdout, stderr []byte) ([]Denial, error) {
	return adapterFor(vendorName).DetectDenials(stdout, stderr), nil
}

// cursorAgentAdapter implements VendorAdapter for cursor-agent.
type cursorAgentAdapter struct{}

// DetectDenials scans each stream-json line for a rejected tool_call event
// (ngl.CursorToolRejection) — confirmed live shape, see that function's
// doc comment. Lines that error (not JSON, or a JSON object with no
// tool_call field at all — most lines in a real transcript, e.g.
// system/user/assistant/thinking/result events) are silently skipped: this
// is deliberately a best-effort scan across a whole transcript, not a
// strict per-line parse.
func (cursorAgentAdapter) DetectDenials(stdout, stderr []byte) []Denial {
	var denials []Denial
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		name, rejection, ok, err := ngl.CursorToolRejection(line)
		if err != nil || !ok {
			continue
		}
		denials = append(denials, Denial{ToolName: name, Detail: rejection.Command})
	}
	return denials
}

func (cursorAgentAdapter) ExtractSessionID(stdout []byte) string { return sessionIDFromJSONLines(stdout) }

// GrantResumePermission is a no-op for cursor-agent — its resume
// CommandTemplate (--resume [chatId] --trust) is sufficient on its own,
// confirmed live; no execution-time side effect is needed.
func (cursorAgentAdapter) GrantResumePermission(Vendor, string, []Denial) (func() error, error) {
	return func() error { return nil }, nil
}

// WrapResumePrompt is a no-op for cursor-agent — its resume reliably
// completes the original request via --resume alone, confirmed live.
func (cursorAgentAdapter) WrapResumePrompt(prompt string) string { return prompt }

// ExtractEditViews scans the stream-json output for a completed
// editToolCall (ngl.CursorToolResult) and renders it via
// ngl.CursorEditViews — genuine NGL wire-format parsing of cursor-agent's
// own real diffString/before/after result, confirmed live 2026-07-26
// against a real cursor-agent run. The path comes straight from the
// result payload's own "path" field (confirmed present there, not just in
// args), not guessed.
func (cursorAgentAdapter) ExtractEditViews(stdout []byte) (ngl.EditViews, bool) {
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		name, resultJSON, ok, err := ngl.CursorToolResult(line)
		if err != nil || !ok || name != "editToolCall" {
			continue
		}
		var argsProbe struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(resultJSON, &argsProbe) // best-effort; CursorEditViews works even with Path == ""
		views, err := ngl.CursorEditViews(argsProbe.Path, resultJSON)
		if err != nil {
			continue
		}
		return views, true
	}
	return ngl.EditViews{}, false
}

// claudeAdapter implements VendorAdapter for claude (Claude Code).
type claudeAdapter struct{}

// DetectDenials scans for the terminal "result" stream-json line and reads
// its confirmed PermissionDenials array (ngl.ClaudeResultEvent) — verified
// against a real live capture (2026-07-26): the field name matched exactly
// on the first try, no adjustment needed.
func (claudeAdapter) DetectDenials(stdout, stderr []byte) []Denial {
	var denials []Denial
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev ngl.ClaudeResultEvent
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type != "result" {
			continue
		}
		for _, d := range ev.PermissionDenials {
			denials = append(denials, Denial{ToolName: d.ToolName, Detail: string(d.ToolInput)})
		}
	}
	return denials
}

func (claudeAdapter) ExtractSessionID(stdout []byte) string { return sessionIDFromJSONLines(stdout) }

// GrantResumePermission is a no-op for claude — its resume CommandTemplate
// (--resume <session-id> --allowedTools ...) is sufficient on its own.
func (claudeAdapter) GrantResumePermission(Vendor, string, []Denial) (func() error, error) {
	return func() error { return nil }, nil
}

// WrapResumePrompt is a no-op for claude — its resume reliably completes
// the original request via --resume alone.
func (claudeAdapter) WrapResumePrompt(prompt string) string { return prompt }

// ExtractEditViews scans the stream-json output for an Edit or Write
// tool_use whose result (via the tool_use_result sibling-field pattern,
// ngl.ExtractToolUseResult) parses as an edit — genuine NGL wire-format
// parsing of claude's own real structuredPatch/whole-file result. Two
// passes are needed (not one): the tool NAME ("Edit" vs some other tool)
// only appears on the "assistant" event that requested it, while the
// actual result only appears later on a "user" event correlated by
// tool_use_id — confirmed live 2026-07-26 that both event types are
// present in real headless stream-json output.
func (claudeAdapter) ExtractEditViews(stdout []byte) (ngl.EditViews, bool) {
	editToolUseIDs := map[string]bool{}
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var assistantProbe struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &assistantProbe); err == nil && assistantProbe.Type == "assistant" {
			for _, block := range assistantProbe.Message.Content {
				if block.Type == "tool_use" && (block.Name == "Edit" || block.Name == "Write") {
					editToolUseIDs[block.ID] = true
				}
			}
			continue
		}

		toolUseID, resultJSON, deniedText, ok, err := ngl.ExtractToolUseResult(line)
		if err != nil || !ok || deniedText != "" || !editToolUseIDs[toolUseID] {
			continue
		}
		views, err := ngl.ClaudeEditViews(resultJSON)
		if err != nil {
			continue
		}
		return views, true
	}
	return ngl.EditViews{}, false
}

// agyAdapter implements VendorAdapter for agy. Unlike the other two, its
// resume needs a real side effect — see GrantResumePermission's doc
// comment in agy_grant.go for the full history of why.
type agyAdapter struct{}

// agyDenialPattern matches agy's own confirmed stderr message verbatim
// (permission-behavior research pass, 2026-07-26, run without
// --dangerously-skip-permissions): "...a tool required the \"<permission>\"
// permission that headless mode cannot prompt for, so it was auto-denied
// ...". This is prose-parsing, not a structured event — agy's headless
// mode has no --output-format flag at all (confirmed in
// agent_cli_interop.md and reconfirmed this pass), so stderr text is the
// only signal available for this vendor.
var agyDenialPattern = regexp.MustCompile(`a tool required the "([^"]+)" permission that headless mode cannot prompt for`)

func (agyAdapter) DetectDenials(stdout, stderr []byte) []Denial {
	m := agyDenialPattern.FindSubmatch(stderr)
	if m == nil {
		return nil
	}
	return []Denial{{ToolName: string(m[1]), Detail: strings.TrimSpace(string(stderr))}}
}

// ExtractSessionID always returns "" for agy — it has no --output-format
// flag at all, so there's no structured stdout to recover an id from.
func (agyAdapter) ExtractSessionID(stdout []byte) string { return "" }

// ExtractEditViews always returns ok=false for agy — its headless output
// is prose-only (no --output-format flag exists for this vendor at all,
// confirmed), so there is no structured diff to parse out of it. Stated
// as an honest limit of this vendor's headless wire format, not a gap to
// paper over with a fabricated before/after (a caller that has independent
// access to the target file's before/after content, e.g. by reading it
// directly, can still build an ngl.EditViews{Before,After} manually — see
// the design doc's live-test notes — but that's outside what this
// function can do from stdout bytes alone).
func (agyAdapter) ExtractEditViews(stdout []byte) (ngl.EditViews, bool) {
	return ngl.EditViews{}, false
}

// FormatDenialSummary renders a run's denials as plain text suitable for
// injecting into the origin CLI's reply — the only slot confirmed to exist
// on any of the three vendors' wire formats (planning/permission_relay_design.md
// §2.2: none of them have a native "permission request" content-block
// type). token is the correlation id from RegisterPendingResume — embedded
// so the human's next message ("/vendor:allow <token>" or
// "/vendor:deny <token>") round-trips it straight back through
// ParseDelegateCommand's existing flag syntax, no new parser needed.
func FormatDenialSummary(vendorName, token string, denials []Denial, text string) string {
	var b strings.Builder
	if text != "" {
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	if len(denials) == 1 {
		b.WriteString("[" + vendorName + "] needs permission for 1 action it could not complete headlessly:\n")
	} else {
		b.WriteString("[" + vendorName + "] needs permission for multiple actions it could not complete headlessly:\n")
	}
	for _, d := range denials {
		if d.ToolName != "" {
			b.WriteString("  - " + d.ToolName + ": " + d.Detail + "\n")
		} else {
			b.WriteString("  - " + d.Detail + "\n")
		}
	}
	b.WriteString("\nReply with \"/" + vendorName + ":allow " + token + "\" to approve and continue, " +
		"or \"/" + vendorName + ":deny " + token + "\" to skip this step.")
	return b.String()
}

// FormatEditSummary renders an ngl.EditViews as a readable diff block for
// injecting into the origin CLI's reply — the "observe the outcome" half
// of Path A (planning/permission_relay_design.md), independent of and
// complementary to FormatDenialSummary's "ask about the blocker" half.
// Returns "" (nothing to append) when views is nil or carries no
// renderable diff — e.g. agy, whose ExtractEditViews always reports
// ok=false, or a vendor's run that made no edit at all.
func FormatEditSummary(vendorName string, views *ngl.EditViews) string {
	if views == nil {
		return ""
	}
	unified, ok := views.Get("unified_text")
	if !ok {
		return ""
	}
	diff, ok := unified.(string)
	if !ok || diff == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[" + vendorName + "] file change")
	if views.Path != "" {
		b.WriteString(" (" + views.Path + ")")
	}
	b.WriteString(":\n```diff\n")
	b.WriteString(diff)
	if !strings.HasSuffix(diff, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```")
	return b.String()
}
