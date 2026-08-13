package vendors

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/glider-ai/glider/internal/ngl"
)

// DetectDenials examines the raw stdout and stderr of a run with no console
// that is complete, and it finds the permissions that a person refused. The
// registered VendorAdapter for vendorName does the work. Refer to adapter.go.
// The core, which is RunWithOptions, knows nothing about the detection logic
// of one vendor, other than this lookup.
//
// A vendor with no registered adapter gives nil and nil, and it does not give
// an error. Therefore RunWithOptions never fails only because no person
// implemented the detection for some vendor.
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

// GrantResumePermission does nothing for cursor-agent. Its resume
// CommandTemplate, which is --resume [chatId] --trust, is sufficient. A live
// test confirmed this. The code needs no other action at the time of
// execution.
func (cursorAgentAdapter) GrantResumePermission(Vendor, string, []Denial) (func() error, error) {
	return func() error { return nil }, nil
}

// ExtraResumeArgs is nil for cursor-agent. A live test with `cursor-agent
// --help` confirmed that it has no flag to permit one tool, and thus it has
// no equivalent of --allowedTools in claude.
//
// Its only control is -f, --force or --yolo. That control permits each future
// permission test, and not only the permission that a person refused. That is
// a much larger increase of permission than this method must give
// automatically. Refer to the comment on ExtraResumeArgs in adapter.go for the
// full cause.
func (cursorAgentAdapter) ExtraResumeArgs(denials []Denial) []string { return nil }

// WrapResumePrompt is a no-op for cursor-agent — its resume reliably
// completes the original request via --resume alone, confirmed live.
func (cursorAgentAdapter) WrapResumePrompt(prompt string) string { return prompt }

// ExtractEditViews searches the stream-json output for an editToolCall that
// is complete (ngl.CursorToolResult), and it makes the views with
// ngl.CursorEditViews. This is a true read of the wire format by NGL, of the
// diffString, before and after result of cursor-agent. A live test on
// 2026-07-26 confirmed it against a true run of cursor-agent.
//
// The path comes directly from the "path" field of the result data. A live
// test confirmed that the field is present there, and not only in the args.
// The code does not estimate the path.
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

// DetectDenials searches for the last "result" line in stream-json and reads
// its confirmed PermissionDenials array (ngl.ClaudeResultEvent). A live
// capture on 2026-07-26 confirmed this: the name of the field agreed exactly
// at the first attempt, and no person had to change it.
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

// GrantResumePermission does nothing for claude. That vendor needs no action
// outside the resume call itself. ExtraResumeArgs below gives the permission
// for each refusal, and this function does not.
func (claudeAdapter) GrantResumePermission(Vendor, string, []Denial) (func() error, error) {
	return func() error { return nil }, nil
}

// ExtraResumeArgs makes "--allowedTools <names>" from the names of the tools
// that a person refused. A live test with `claude --help` confirmed the flag:
// "--allowedTools, --allowed-tools <tools...> Comma or space-separated list
// of tool names to allow".
//
// The registered "resume" CommandTemplate has no --allowedTools of its own,
// and this is on purpose. Refer to configs/vendor_candidates.yaml. It cannot
// have one, because the code knows the name of the tool only for each call,
// and not when a person defines the template.
//
// Without this function, the "allow" flow of resolveAllow sent the same
// prompt again against the same permission state, which refuses. Then only
// chance, or a change in the behaviour of the model, could give a different
// result.
//
// The function removes each repeated name with uniqueDenialToolNames, and
// agy_grant.go also uses that function. One run can show the same tool as
// refused more than one time.
func (claudeAdapter) ExtraResumeArgs(denials []Denial) []string {
	tools := uniqueDenialToolNames(denials)
	if len(tools) == 0 {
		return nil
	}
	return []string{"--allowedTools", strings.Join(tools, ",")}
}

// WrapResumePrompt is a no-op for claude — its resume reliably completes
// the original request via --resume alone.
func (claudeAdapter) WrapResumePrompt(prompt string) string { return prompt }

// ExtractEditViews searches the stream-json output for an Edit or a Write
// tool_use with a result that reads as an edit. The result comes from the
// field beside the message, with ngl.ExtractToolUseResult. This is a true read
// of the wire format by NGL, of the structuredPatch or full-file result of
// claude.
//
// The code needs two passes, and not one. The NAME of the tool, which shows
// "Edit" and not a different tool, is only on the "assistant" event that asked
// for the tool. The result is later, on a "user" event, and tool_use_id
// correlates the two. A live test on 2026-07-26 confirmed that true
// stream-json output with no console has both types of event.
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

// agyDenialPattern agrees exactly with the confirmed stderr message of agy.
// The research pass on permission behaviour captured it on 2026-07-26, with
// no --dangerously-skip-permissions flag: "...a tool required the
// \"<permission>\" permission that headless mode cannot prompt for, so it was
// auto-denied ...".
//
// This reads prose, and it does not read a structured event. The mode of agy
// with no console has no --output-format flag. agent_cli_interop.md confirms
// this, and this pass confirmed it again. Therefore the text on stderr is the
// only signal for this vendor.
var agyDenialPattern = regexp.MustCompile(`a tool required the "([^"]+)" permission that headless mode cannot prompt for`)

func (agyAdapter) DetectDenials(stdout, stderr []byte) []Denial {
	m := agyDenialPattern.FindSubmatch(stderr)
	if m == nil {
		return nil
	}
	return []Denial{{ToolName: string(m[1]), Detail: strings.TrimSpace(string(stderr))}}
}

// ExtractSessionID always returns "" for agy — it has no --output-format
// flag at all, so there is no structured stdout to recover an id from.
func (agyAdapter) ExtractSessionID(stdout []byte) string { return "" }

// ExtractEditViews always returns ok=false for agy. Its output with no
// console is prose only, because this vendor has no --output-format flag, and
// a person confirmed that. Therefore there is no structured diff to read from
// it. Stated as an honest limit of this vendor's headless wire format. This
// code does not hide that limit with a before/after that it invents. (A
// caller with independent access to the target file's before/after content,
// e.g. by reading it directly, can still build an ngl.EditViews{Before,After}
// manually — see the design doc's live-test notes — but that is outside what
// this function can do from stdout bytes alone).
func (agyAdapter) ExtractEditViews(stdout []byte) (ngl.EditViews, bool) {
	return ngl.EditViews{}, false
}

// FormatDenialSummary makes plain text from the refusals of a run. Glider
// puts that text in the reply of the origin CLI. That is the only position
// that a person confirmed on the wire format of the three vendors. Refer to
// planning/permission_relay_design.md §2.2: no vendor has a native
// content-block type for a "permission request". token is the id for
// correlation, from RegisterPendingResume. Glider puts it in the text.
// Therefore the next message of the person sends it back, as "<token>
// /vendor:allow" or "<token> /vendor:deny". That message uses the
// trailing-flag syntax that ParseDelegateCommand already reads, and no code
// needs a new parser. The token comes BEFORE the flag, and not after it. The
// flag must be the last item in the message. Refer to the comment on
// ParseDelegateCommand for the cause: a "/" at the start fails on some front
// CLIs.
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
	b.WriteString("\nReply with \"" + token + " /" + vendorName + ":allow\" to approve and continue, " +
		"or \"" + token + " /" + vendorName + ":deny\" to skip this step.")
	return b.String()
}

// FormatEditSummary makes a diff block that a person can read, from an
// ngl.EditViews. Glider puts that block in the reply of the origin CLI. This
// is the half of Path A that observes the result. Refer to
// planning/permission_relay_design.md. It operates independently of
// FormatDenialSummary, which is the half that asks about the obstacle. The
// two halves operate together. Returns "" (nothing to append) when views is
// nil or carries no renderable diff — e.g. agy, whose ExtractEditViews always
// reports ok=false, or a vendor's run that made no edit at all.
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
