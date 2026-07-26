package ngl

import (
	"encoding/json"
	"fmt"
	"strings"
)

// cursorStreamEvent is cursor-agent's stream-json tool_call event's FULL
// outer shape, confirmed live 2026-07-26 by actually running
// `cursor-agent -p ... --output-format stream-json` and reading its own
// output — not just the inner oneof, which is all the original research
// pass captured with certainty:
//
//	{"type":"tool_call","subtype":"started"|"completed","call_id":"call-<uuid>-<n>\nfc_<uuid>_<n>",
//	 "tool_call":{"<name>ToolCall":{"args":{...},"result":{"success":{...}}},
//	              "hookAdditionalContexts":[],"toolCallId":"<same as call_id>",
//	              "startedAtMs":"<string, not a number>","completedAtMs":"<string>"},
//	 "model_call_id":"...","session_id":"...","timestamp_ms":<number, not a string>}
//
// The earlier version of this file assumed {"tool_call":{"readToolCall":
// {"args":{...}}}} WAS the whole event, not just one field of it — that
// happened to still parse args correctly (ParseCursorToolCall only ever
// read the tool_call field), but it meant ToolCall.ID was silently always
// empty, since nothing populated it. "hookAdditionalContexts" (always
// observed empty, [] — a hook-injection mechanism not otherwise
// documented) is a genuinely new field this pass found and isn't modeled
// further here since no non-empty example was ever observed.
type cursorStreamEvent struct {
	Type        string                     `json:"type"`
	Subtype     string                     `json:"subtype"`
	CallID      string                     `json:"call_id"`
	ToolCall    map[string]json.RawMessage `json:"tool_call"`
	ModelCallID string                     `json:"model_call_id"`
	SessionID   string                     `json:"session_id"`
	TimestampMs int64                      `json:"timestamp_ms"`
}

type cursorToolCallBody struct {
	Args          json.RawMessage `json:"args"`
	ToolCallID    string          `json:"toolCallId"`
	StartedAtMs   string          `json:"startedAtMs,omitempty"`
	CompletedAtMs string          `json:"completedAtMs,omitempty"`
}

// ParseCursorToolCall extracts a ToolCall from one tool_call event — fixed
// 2026-07-26 to actually populate ID (from toolCallId, confirmed live to
// be identical to the outer call_id) once the full envelope shape was
// captured; the earlier version silently left ID empty always.
func ParseCursorToolCall(raw json.RawMessage) (*ToolCall, error) {
	var env cursorStreamEvent
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	for name, body := range env.ToolCall {
		var b cursorToolCallBody
		if err := json.Unmarshal(body, &b); err != nil {
			return nil, err
		}
		id := b.ToolCallID
		if id == "" {
			id = env.CallID
		}
		return &ToolCall{Name: name, Args: b.Args, ID: id}, nil
	}
	return nil, fmt.Errorf("ngl: no tool_call variant found in %s", raw)
}

// CursorToolResult extracts a completed tool_call event's result.success
// payload for feeding to a result parser (CursorEditViews, or a
// tool-specific result struct). ok=false for a "started" event (no result
// yet) OR a rejected one — check CursorToolRejection to tell those two
// apart; a caller that only calls this function can't distinguish
// "still running" from "denied."
func CursorToolResult(raw json.RawMessage) (name string, resultJSON json.RawMessage, ok bool, err error) {
	var env cursorStreamEvent
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", nil, false, err
	}
	for toolName, body := range env.ToolCall {
		var wrapper struct {
			Result struct {
				Success json.RawMessage `json:"success"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &wrapper); err != nil {
			return "", nil, false, err
		}
		if len(wrapper.Result.Success) == 0 {
			return toolName, nil, false, nil
		}
		return toolName, wrapper.Result.Success, true, nil
	}
	return "", nil, false, fmt.Errorf("ngl: no tool_call variant found in %s", raw)
}

// CursorRejection matches a rejected tool_call event's result shape —
// confirmed live 2026-07-26 (permission-behavior research pass, run
// without --force to let genuine denials occur): a completed shellToolCall
// carries {"result":{"rejected":{"command":"...","reason":""}}} in place
// of result.success. Reason was an empty string in every observed
// rejection this pass — whether it's ever populated, or always blank for
// this specific rejection path, is still open.
type CursorRejection struct {
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

// CursorToolRejection extracts a rejection from a completed tool_call
// event, mirroring CursorToolResult but for the "rejected" case — the raw
// material for Path A's denial detection
// (planning/permission_relay_design.md §2.1). ok=false (no error) for a
// tool_call whose result is neither rejected nor success yet (still
// "started"); the same "no tool_call variant found" error as
// CursorToolResult for an event that isn't a tool_call at all.
func CursorToolRejection(raw json.RawMessage) (name string, rejection *CursorRejection, ok bool, err error) {
	var env cursorStreamEvent
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", nil, false, err
	}
	for toolName, body := range env.ToolCall {
		var wrapper struct {
			Result struct {
				Rejected *CursorRejection `json:"rejected"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &wrapper); err != nil {
			return "", nil, false, err
		}
		if wrapper.Result.Rejected == nil {
			return toolName, nil, false, nil
		}
		return toolName, wrapper.Result.Rejected, true, nil
	}
	return "", nil, false, fmt.Errorf("ngl: no tool_call variant found in %s", raw)
}

// cursorTextBlock is the content-block shape inside "user" and "assistant"
// stream-json events — Anthropic-block-shaped, confirmed live, matching
// agent_cli_interop.md's earlier prediction ("two of three sources lean
// Anthropic-block-shaped").
type cursorTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type cursorMessageEvent struct {
	Type    string `json:"type"`
	Message struct {
		Role    string             `json:"role"`
		Content []cursorTextBlock `json:"content"`
	} `json:"message"`
	SessionID string `json:"session_id"`
}

// CursorThinkingEvent matches the "thinking" event shape exactly — Text is
// present for subtype "delta" and absent for subtype "completed" (the
// closing marker of one reasoning burst, carries no text of its own).
type CursorThinkingEvent struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"` // "delta" | "completed"
	Text        string `json:"text,omitempty"`
	SessionID   string `json:"session_id"`
	TimestampMs int64  `json:"timestamp_ms"`
}

// CursorSystemInit matches the "system"/"init" event shape exactly — the
// first line of every session.
type CursorSystemInit struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	APIKeySource   string `json:"apiKeySource"`
	Cwd            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`
}

// CursorResultEvent matches the final "result" event shape exactly,
// including the real usage field names (camelCase, and a materially
// different shape than Anthropic's own usage block).
type CursorResultEvent struct {
	Type          string      `json:"type"`
	Subtype       string      `json:"subtype"` // "success" observed; failure shape not captured
	DurationMs    int         `json:"duration_ms"`
	DurationAPIMs int         `json:"duration_api_ms"`
	IsError       bool        `json:"is_error"`
	Result        string      `json:"result"`
	SessionID     string      `json:"session_id"`
	RequestID     string      `json:"request_id"`
	Usage         CursorUsage `json:"usage"`
}

type CursorUsage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens"`
	CacheWriteTokens int `json:"cacheWriteTokens"`
}

// CursorInteractionQuery matches a genuinely new event type this pass
// found — not named or shaped anywhere in the original research: a
// permission-gate protocol distinct from the tool_call lifecycle itself.
// Observed live wrapping a web search request/response
// ({"query_type":"webSearchRequestQuery", ..., "response":{"id":0,
// "webSearchRequestResponse":{"approved":{}}}}); whether "approved" has a
// sibling "denied" shape, and whether this gate applies to tools beyond
// web search, are both unconfirmed — only the approved path was observed.
type CursorInteractionQuery struct {
	Type      string          `json:"type"`    // "interaction_query"
	Subtype   string          `json:"subtype"` // "request" | "response"
	QueryType string          `json:"query_type"`
	Query     json.RawMessage `json:"query,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
	SessionID string          `json:"session_id"`
}

// ParseCursorAgentTurn assembles a Turn from one stream-json line,
// dispatching on its top-level "type" — extended 2026-07-26 to cover
// every event type actually captured live (user, assistant, tool_call,
// thinking), not just tool_call as the first pass had it. system/init,
// result, and interaction_query carry no turn *content* (session/protocol
// bookkeeping instead) — Raw is preserved for them, Parts stays empty
// rather than force something into it.
func ParseCursorAgentTurn(raw json.RawMessage) (Turn, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Turn{}, err
	}
	switch probe.Type {
	case "user", "assistant":
		var ev cursorMessageEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return Turn{}, err
		}
		parts := make([]Part, 0, len(ev.Message.Content))
		for _, b := range ev.Message.Content {
			if b.Type == "text" {
				parts = append(parts, Part{Kind: PartUserText, Text: b.Text})
			}
		}
		return Turn{Vendor: "cursor-agent", Raw: raw, Parts: parts}, nil
	case "tool_call":
		tc, err := ParseCursorToolCall(raw)
		if err != nil {
			return Turn{}, err
		}
		return Turn{Vendor: "cursor-agent", Raw: raw, Parts: []Part{{Kind: PartToolCall, ToolCall: tc}}}, nil
	case "thinking":
		var ev CursorThinkingEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return Turn{}, err
		}
		if ev.Text == "" {
			return Turn{Vendor: "cursor-agent", Raw: raw}, nil // "completed" marker, no text of its own
		}
		return Turn{Vendor: "cursor-agent", Raw: raw, Parts: []Part{{Kind: PartReasoning, Text: ev.Text}}}, nil
	default:
		return Turn{Vendor: "cursor-agent", Raw: raw}, nil
	}
}

// CursorCompositeID splits cursor-agent's tool-call id into its two
// concatenated parts. The correlation id observed live is
// "call-<uuid>-<n>\nfc_<uuid>_<n>" — a Cursor wrapper id concatenated
// (embedded newline) with an OpenAI-style fc_… function-call id
// (agent_cli_interop.md §2's Correlation row), implying an OpenAI-shaped
// model sits behind at least one routing tier. Kept as a fossil worth
// parsing explicitly rather than treating the whole string as one opaque
// token, since the two halves may need to correlate against different
// things (Cursor's own tool-call bookkeeping vs. an underlying
// OpenAI-shaped function-call id).
type CursorCompositeID struct {
	Raw    string // the full original string, untouched
	Cursor string // the "call-<uuid>-<n>" half
	OpenAI string // the "fc_<uuid>_<n>" half
}

// ParseCursorCompositeID splits id on its embedded newline. ok=false for
// any id that doesn't match the two-part shape — including a plain,
// non-composite id, which is a normal, valid case (this parsing is
// best-effort enrichment, not a requirement every id satisfies).
func ParseCursorCompositeID(id string) (CursorCompositeID, bool) {
	parts := strings.SplitN(id, "\n", 2)
	if len(parts) != 2 {
		return CursorCompositeID{}, false
	}
	return CursorCompositeID{Raw: id, Cursor: parts[0], OpenAI: parts[1]}, true
}

// cursorEditResult matches editToolCall's confirmed result shape:
// diffString (standard unified diff) plus both full before/after
// snapshots — richer than Claude's hunks-only shape. Confirmed live
// 2026-07-26 exactly as originally documented, field-for-field, including
// a "message" field ("The file <path> has been updated.") this file
// doesn't currently model into EditViews since it's presentation text,
// not diff content.
type cursorEditResult struct {
	DiffString   string `json:"diffString"`
	Before       string `json:"beforeFullFileContent"`
	After        string `json:"afterFullFileContent"`
	LinesAdded   int    `json:"linesAdded"`
	LinesRemoved int    `json:"linesRemoved"`
}

// CursorEditViews parses an editToolCall result (feed it the bytes from
// CursorToolResult, i.e. result.success — not the whole event) into
// EditViews, natively populating UnifiedText, Before, and After all at
// once — no conversion needed, unlike Claude's hunks-only or agy's
// range-replace-only shapes.
func CursorEditViews(path string, resultJSON json.RawMessage) (EditViews, error) {
	var r cursorEditResult
	if err := json.Unmarshal(resultJSON, &r); err != nil {
		return EditViews{}, err
	}
	return EditViews{
		Path:        path,
		UnifiedText: r.DiffString,
		Before:      r.Before,
		After:       r.After,
	}, nil
}

// CursorEditToolCallArgs matches editToolCall's confirmed ARGS shape —
// corrected 2026-07-26 after live capture: the real args are {path,
// streamContent}, where streamContent is the proposed FULL NEW file
// content being streamed in, not an {old,new} snippet pair as an earlier,
// unverified guess in this file's tests assumed. cursor-agent computes the
// diff server-side and returns it in the result (see cursorEditResult) —
// the args themselves carry no diff, only the target whole-file content.
type CursorEditToolCallArgs struct {
	Path          string `json:"path"`
	StreamContent string `json:"streamContent"`
}

// cursorDeleteResult matches deleteToolCall's result.success shape. The
// deleted file's content is preserved in prevContent — a real
// undo-friendly convention (agent_cli_interop.md §2) worth carrying into
// NGL's ToolResult for any destructive op, not just this one vendor's tool.
type cursorDeleteResult struct {
	Path        string `json:"path"`
	DeletedFile bool   `json:"deletedFile"`
	PrevContent string `json:"prevContent"`
}

// ParseCursorDeleteResult extracts a ToolResult from a deleteToolCall
// result, preserving the deleted content rather than discarding it.
func ParseCursorDeleteResult(resultJSON json.RawMessage) (*ToolResult, error) {
	var r cursorDeleteResult
	if err := json.Unmarshal(resultJSON, &r); err != nil {
		return nil, err
	}
	return &ToolResult{Text: r.PrevContent}, nil
}

// CursorGlobArgs matches globToolCall's confirmed args shape.
type CursorGlobArgs struct {
	GlobPattern     string `json:"globPattern"`
	TargetDirectory string `json:"target_directory,omitempty"` // omitted in every live example observed; documented name, unconfirmed live use
}

// CursorGlobResult matches globToolCall's confirmed result shape —
// corrected 2026-07-26: the real fields are pattern/path/files/totalFiles/
// clientTruncated/ripgrepTruncated. An earlier, unverified guess in this
// file assumed {filenames, durationMs, numFiles, truncated, totalMatches,
// countIsComplete} — none of those field names are real; this replaces it
// entirely rather than keep both.
type CursorGlobResult struct {
	Pattern          string   `json:"pattern"`
	Path             string   `json:"path"`
	Files            []string `json:"files"`
	TotalFiles       int      `json:"totalFiles"`
	ClientTruncated  bool     `json:"clientTruncated"`
	RipgrepTruncated bool     `json:"ripgrepTruncated"`
}

// CursorGrepArgs matches grepToolCall's confirmed args shape — richer than
// originally documented: real pagination (offset) and matching
// (caseInsensitive, multiline) controls, confirmed live 2026-07-26.
type CursorGrepArgs struct {
	Pattern         string `json:"pattern"`
	Glob            string `json:"glob"`
	CaseInsensitive bool   `json:"caseInsensitive"`
	Multiline       bool   `json:"multiline"`
	ToolCallID      string `json:"toolCallId"`
	Offset          int    `json:"offset"`
}

// CursorGrepResult matches grepToolCall's confirmed result shape —
// corrected 2026-07-26: workspaceResults keys are workspace ROOTS, not
// individual file paths as an earlier, unverified guess in this file
// assumed, and each root's content.matches is an array of PER-FILE match
// groups ({file, matches:[...]})  — one nesting level deeper than
// originally modeled. Real shape:
//
//	{"pattern":..., "path":..., "outputMode":"content",
//	 "workspaceResults": {"<workspace root>": {"content": {
//	   "matches": [{"file":"./x.py","matches":[{"lineNumber":1,"content":"...",
//	                "contentTruncated":false,"isContextLine":false}]}],
//	   "totalLines":N, "totalMatchedLines":N,
//	   "clientTruncated":false, "ripgrepTruncated":false}}}}
type CursorGrepResult struct {
	Pattern          string                               `json:"pattern"`
	Path             string                                `json:"path"`
	OutputMode       string                               `json:"outputMode"`
	WorkspaceResults map[string]CursorGrepWorkspaceResult `json:"workspaceResults"`
}

type CursorGrepWorkspaceResult struct {
	Content CursorGrepWorkspaceContent `json:"content"`
}

type CursorGrepWorkspaceContent struct {
	Matches           []CursorGrepFileMatches `json:"matches"`
	TotalLines        int                     `json:"totalLines"`
	TotalMatchedLines int                     `json:"totalMatchedLines"`
	ClientTruncated   bool                    `json:"clientTruncated"`
	RipgrepTruncated  bool                    `json:"ripgrepTruncated"`
}

type CursorGrepFileMatches struct {
	File    string            `json:"file"`
	Matches []CursorGrepMatch `json:"matches"`
}

type CursorGrepMatch struct {
	LineNumber       int    `json:"lineNumber"`
	Content          string `json:"content"`
	ContentTruncated bool   `json:"contentTruncated"`
	IsContextLine    bool   `json:"isContextLine"`
}

// CursorWebFetchArgs/Result match webFetchToolCall's confirmed shape —
// pre-converted to markdown, unlike Claude's model-summarized WebFetch.
type CursorWebFetchArgs struct {
	URL        string `json:"url"`
	ToolCallID string `json:"toolCallId"`
}

type CursorWebFetchResult struct {
	URL      string `json:"url"`
	Markdown string `json:"markdown"`
}

// CursorWebSearchArgs/Result match webSearchToolCall's confirmed shape.
// Corrected 2026-07-26: live capture shows references typically holding
// ONE entry — title "Web search results", url "", and Chunk containing a
// large synthesized block (a "Links" list, a "Synthesis" section, and
// numbered "Highlights" with embedded citations) — not one reference per
// search hit as the field names alone might suggest. The documented
// large-result-by-reference file-offload convention
// (~/.cursor/projects/<hash>/agent-tools/<uuid>.txt) was NOT observed in
// this pass — the synthesized chunk was inlined directly even though it
// was substantial — so that convention remains unconfirmed by this
// research, not verified false, just still open.
type CursorWebSearchArgs struct {
	SearchTerm string `json:"searchTerm"`
	ToolCallID string `json:"toolCallId"`
}

type CursorWebSearchResult struct {
	References []CursorWebSearchReference `json:"references"`
}

type CursorWebSearchReference struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Chunk string `json:"chunk"`
}

// CursorUpdateTodosArgs/Result match updateTodosToolCall's confirmed
// shape. Dependencies is a real DAG-between-todos field (schema location
// confirmed live), but this pass's live example had the model express a
// dependency as free text in Content ("add type hints (depends on: add
// tests)") rather than actually populating Dependencies — so the field's
// existence and shape are confirmed, but not yet an example of it holding
// a real value. Timestamps are strings, confirmed, same convention as
// startedAtMs/completedAtMs elsewhere in this vendor's wire format.
type CursorUpdateTodosArgs struct {
	Todos []CursorTodo `json:"todos"`
	Merge bool         `json:"merge"`
}

type CursorTodo struct {
	ID           string   `json:"id"`
	Content      string   `json:"content"`
	Status       string   `json:"status"` // "TODO_STATUS_PENDING" confirmed live; others named in agent_cli_interop.md, not directly observed
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
	Dependencies []string `json:"dependencies"`
}

type CursorUpdateTodosResult struct {
	Todos      []CursorTodo `json:"todos"`
	TotalCount int          `json:"totalCount"`
	WasMerge   bool         `json:"wasMerge"`
}
