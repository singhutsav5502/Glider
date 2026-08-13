package ngl

import (
	"encoding/json"
	"fmt"
	"strings"
)

// cursorStreamEvent is the FULL outer shape of a tool_call event in the
// stream-json output of cursor-agent. A live test confirmed it on
// 2026-07-26: a person ran `cursor-agent -p ... --output-format
// stream-json` and read its output. The first research pass captured only
// the inner oneof with certainty.
//
//	{"type":"tool_call","subtype":"started"|"completed","call_id":"call-<uuid>-<n>\nfc_<uuid>_<n>",
//	 "tool_call":{"<name>ToolCall":{"args":{...},"result":{"success":{...}}},
//	              "hookAdditionalContexts":[],"toolCallId":"<same as call_id>",
//	              "startedAtMs":"<string, not a number>","completedAtMs":"<string>"},
//	 "model_call_id":"...","session_id":"...","timestamp_ms":<number, not a string>}
//
// The earlier version of this file assumed that
// {"tool_call":{"readToolCall":{"args":{...}}}} WAS the full event. It is
// only one field of the event. That version still read the args correctly,
// because ParseCursorToolCall read only the tool_call field. But
// ToolCall.ID was always empty, and the code gave no message, because
// nothing put a value in it.
//
// "hookAdditionalContexts" is a new field that this pass found. Each
// observation showed it empty, as []. It is a mechanism for a hook, and no
// document describes it. This file does not model it more, because no
// person observed an example that is not empty.
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

// ParseCursorToolCall extracts a ToolCall from one tool_call event.
//
// Corrected 2026-07-26, after a person captured the full shape of the
// envelope. The function now puts a value in ID, from toolCallId. A live
// test confirmed that toolCallId is the same as the outer call_id. The
// earlier version always left ID empty, and it gave no message.
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

// CursorToolResult extracts the result.success data from a tool_call event
// that is complete. A caller gives that data to a parser of results:
// CursorEditViews, or a result structure for one tool.
//
// It returns ok=false in two conditions: for an event with the state
// "started", which has no result, and for an event that a person refused.
// Use CursorToolRejection to find the difference. A caller that uses only
// this function cannot see the difference between "it continues" and "a
// person refused it".
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

// CursorRejection agrees with the result shape of a tool_call event that a
// person refused. A live test confirmed it on 2026-07-26, during the
// research pass on permission behaviour. That test ran with no --force
// flag, thus true refusals could occur.
//
// A shellToolCall that is complete has
// {"result":{"rejected":{"command":"...","reason":""}}} in the position of
// result.success. In each refusal that a person observed in this pass,
// Reason was an empty string. Two questions stay open: does any code put a
// value in Reason, and is it always empty for this one path?
type CursorRejection struct {
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

// CursorToolRejection extracts a refusal from a tool_call event that is
// complete. It does the same work as CursorToolResult, but for the
// "rejected" condition. This is the data that Path A uses to find a
// refusal. Refer to planning/permission_relay_design.md §2.1.
//
// It returns ok=false and no error for a tool_call with a result that is
// not "rejected" and not "success". Such an event is still in the
// "started" state. For an event that is not a tool_call, it gives the same
// "no tool_call variant found" error as CursorToolResult.
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

// CursorThinkingEvent agrees exactly with the shape of a "thinking" event.
// Text is present for the subtype "delta". Text is absent for the subtype
// "completed", which is the end marker of one period of reasoning and holds
// no text.
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

// CursorInteractionQuery agrees with a new type of event that this pass
// found. The first research gave it no name and no shape. It is a protocol
// for a permission gate, and it is different from the sequence of a
// tool_call.
//
// A person observed it live around a request and a response for a web
// search: {"query_type":"webSearchRequestQuery", ...,
// "response":{"id":0,"webSearchRequestResponse":{"approved":{}}}}.
//
// Two items are not confirmed. Does "approved" have an equivalent "denied"
// shape? Does this gate apply to tools other than a web search? A person
// observed only the path with approval.
type CursorInteractionQuery struct {
	Type      string          `json:"type"`    // "interaction_query"
	Subtype   string          `json:"subtype"` // "request" | "response"
	QueryType string          `json:"query_type"`
	Query     json.RawMessage `json:"query,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
	SessionID string          `json:"session_id"`
}

// ParseCursorAgentTurn makes a Turn from one line of stream-json. It selects
// the action from the "type" field at the top level.
//
// Extended 2026-07-26 to cover each type of event that a person captured
// live: user, assistant, tool_call and thinking. The first pass covered only
// tool_call.
//
// Three types hold no turn *content*, and they hold data about the session
// and the protocol instead: system/init, result and interaction_query. This
// code keeps Raw for them, and Parts stays empty. It does not put incorrect
// data in Parts.
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

// CursorCompositeID divides the tool-call id of cursor-agent into its two
// parts.
//
// The id for correlation that a person observed live has three parts. First a
// Cursor wrapper id. Then a newline character. Then a function-call id with
// the shape of OpenAI, which starts with fc_. Refer to the Correlation row in
// agent_cli_interop.md section 2. Therefore a model with the shape of OpenAI
// is behind a minimum of one level of routing.
//
// This code divides the id and does not use the full text as one token. The
// two parts can need to correlate against different items: the tool-call
// records of Cursor, and a function-call id with the shape of OpenAI.
type CursorCompositeID struct {
	Raw    string // the full original string, untouched
	Cursor string // the "call-<uuid>-<n>" half
	OpenAI string // the "fc_<uuid>_<n>" half
}

// ParseCursorCompositeID divides id at the newline character inside it. It
// returns ok=false for each id that does not have the shape with two parts.
// This includes a plain id with one part, which is a usual and correct
// condition. This operation adds data when it can, and each id does not have
// to agree with it.
func ParseCursorCompositeID(id string) (CursorCompositeID, bool) {
	parts := strings.SplitN(id, "\n", 2)
	if len(parts) != 2 {
		return CursorCompositeID{}, false
	}
	return CursorCompositeID{Raw: id, Cursor: parts[0], OpenAI: parts[1]}, true
}

// cursorEditResult agrees with the confirmed result shape of editToolCall:
// diffString, which is a standard unified diff, and full copies of the
// content before and after. This is more data than the shape of Claude,
// which gives hunks only.
//
// A live test on 2026-07-26 confirmed each field, and the fields agree with
// the first document. The result also has a "message" field, with text such
// as "The file <path> has been updated.". This code does not put that field
// in EditViews, because it is text to show to a person and not diff
// content.
type cursorEditResult struct {
	DiffString   string `json:"diffString"`
	Before       string `json:"beforeFullFileContent"`
	After        string `json:"afterFullFileContent"`
	LinesAdded   int    `json:"linesAdded"`
	LinesRemoved int    `json:"linesRemoved"`
}

// CursorEditViews reads the result of an editToolCall and makes EditViews.
// Give it the bytes from CursorToolResult, which are result.success, and not
// the full event.
//
// It puts values in UnifiedText, Before and After at the same time, and it
// needs no conversion. The shape of Claude gives hunks only, and the shape of
// agy replaces a range only. Both of those need a conversion.
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

// CursorEditToolCallArgs agrees with the confirmed ARGS shape of
// editToolCall.
//
// Corrected 2026-07-26 after a live capture. The true args are path and
// streamContent. streamContent is the full new content of the file, and the
// CLI sends it in parts. An earlier guess in the tests of this file was not
// verified, and it assumed a pair of old and new parts.
//
// cursor-agent calculates the diff on the server and returns the diff in the
// result. Refer to cursorEditResult. The args hold no diff. They hold only
// the full content for the file.
type CursorEditToolCallArgs struct {
	Path          string `json:"path"`
	StreamContent string `json:"streamContent"`
}

// cursorDeleteResult agrees with the result.success shape of deleteToolCall.
// The content of the file that the CLI deleted stays in prevContent. This is
// a true convention that permits an undo operation. Refer to
// agent_cli_interop.md section 2. NGL keeps this convention in its ToolResult
// for each destructive operation, and not only for the tool of this one
// vendor.
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

// CursorGlobResult agrees with the confirmed result shape of globToolCall.
//
// Corrected 2026-07-26. The true fields are pattern, path, files, totalFiles,
// clientTruncated and ripgrepTruncated. An earlier guess in this file was not
// verified, and it assumed filenames, durationMs, numFiles, truncated,
// totalMatches and countIsComplete. None of those names are true, thus this
// structure replaces that guess fully.
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

// CursorGrepResult agrees with the confirmed result shape of grepToolCall.
//
// Corrected 2026-07-26. The keys of workspaceResults are the ROOT directories
// of a workspace. An earlier guess in this file was not verified, and it
// assumed that the keys are paths of single files. Also, content.matches of
// each root is an array of match groups, and there is one group for each
// file. That is one level more than the first model.
//
// The true shape is:
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

// CursorWebSearchArgs and CursorWebSearchResult agree with the confirmed
// shape of webSearchToolCall.
//
// Corrected 2026-07-26. A live capture shows that references usually holds
// ONE entry. Its title is "Web search results". Its url is empty. Its Chunk
// holds one large block that the CLI made. That block has a list of "Links",
// a "Synthesis" part, and "Highlights" with numbers and citations. The names
// of the fields alone can show one reference for each result of the search,
// but that is not correct.
//
// A document describes a convention to put a large result in a file, under
// ~/.cursor/projects/<hash>/agent-tools/. A person did NOT observe that
// convention in this pass. The block was in the message directly, and the
// block was large. Therefore this research does not confirm that convention,
// and it does not show that the convention is false. The question stays open.
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

// CursorUpdateTodosArgs and CursorUpdateTodosResult agree with the confirmed
// shape of updateTodosToolCall.
//
// Dependencies is a true field for a directed graph between the todo items,
// and a live test confirmed the position of it in the schema. But in the live
// example of this pass, the model wrote a dependency as plain text in
// Content. It wrote "add type hints (depends on: add tests)". It did not put
// a value in Dependencies. Therefore the field and its shape are confirmed,
// but no example shows a true value in it.
//
// The times are strings, and this is confirmed. It is the same convention as
// startedAtMs and completedAtMs in the wire format of this vendor.
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
