package ngl

import (
	"encoding/json"
	"strings"
)

// claudeContentBlock covers the block shapes Claude Code's assistant
// messages actually use (planning/agent_cli_interop.md §1): text and
// tool_use. Input is kept raw — Args on the resulting ToolCall, untouched.
type claudeContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// ParseClaudeAssistantParts changes the array of content blocks of an
// assistant message into Parts. A text block becomes a PartUserText, and a
// tool_use block becomes a PartToolCall.
//
// The use of PartUserText here is an approximation. That Kind means "plain
// text that the speaker of this turn wrote", and it does not mean input from
// a person only. An assistant message has no Kind of its own yet.
//
// The code sets Hosted from the prefix of the tool_use id. The hosted tools
// of Anthropic, in the class of WebSearch and WebFetch, operate on the
// servers of Anthropic and not on this machine. They use the id prefix
// `srvtoolu_`. Each tool on the client side uses `toolu_`. Refer to
// agent_cli_interop.md section 1.
//
// This difference is necessary for delegation. A call to a hosted tool has no
// local process to start. Therefore Glider can never send it to a session
// with no console, but it can send a call to a local tool. The prefix of the
// id is the signal of Claude for this fact, and NGL did not invent it.
func ParseClaudeAssistantParts(content json.RawMessage) ([]Part, error) {
	var blocks []claudeContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, err
	}
	parts := make([]Part, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, Part{Kind: PartUserText, Text: b.Text})
		case "tool_use":
			parts = append(parts, Part{Kind: PartToolCall, ToolCall: &ToolCall{
				ID: b.ID, Name: b.Name, Args: b.Input,
				Hosted: strings.HasPrefix(b.ID, "srvtoolu_"),
			}})
		default:
			parts = append(parts, Part{Kind: PartOther})
		}
	}
	return parts, nil
}

// ParseClaudeTurn makes a full Turn from the raw array of content blocks of
// an assistant message. It is the entry point at the level of a Turn, and it
// uses ParseClaudeAssistantParts. It keeps the raw bytes in the result, as
// the contract of Turn requires. Therefore code can always send those bytes
// to the same vendor again, with no change.
func ParseClaudeTurn(raw json.RawMessage) (Turn, error) {
	parts, err := ParseClaudeAssistantParts(raw)
	if err != nil {
		return Turn{}, err
	}
	return Turn{Vendor: "claude", Raw: raw, Parts: parts}, nil
}

// claudeToolResultBlock is a tool_result block. Code sends it back as one
// part of a message with role="user". This is the exact shape that the
// correction in ngl.go keeps separate from true text of a person. Refer to
// the package comment in that file.
type claudeToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error,omitempty"`
	// Content can be a plain string, or an array of blocks inside other
	// blocks. The tool_result data of Claude that a person observed live is a
	// plain string, or the structured shape of an edit result. ClaudeEditViews
	// processes that structured shape separately. ParseClaudeToolResult needs
	// only the form with plain text.
	Content string `json:"content,omitempty"`
}

// ParseClaudeToolResult extracts a ToolResult from one tool_result block.
func ParseClaudeToolResult(block json.RawMessage) (*ToolResult, error) {
	var b claudeToolResultBlock
	if err := json.Unmarshal(block, &b); err != nil {
		return nil, err
	}
	return &ToolResult{ToolCallID: b.ToolUseID, Text: b.Content, IsError: b.IsError}, nil
}

// claudeUserToolResultEvent agrees with the FULL outer envelope of a "user"
// line in stream-json that carries a tool result. A live test confirmed it on
// 2026-07-26, in a true captured session with ANTHROPIC_BASE_URL directed at
// a local test harness.
//
// This is the fact about the structure that ClaudeEditViews and
// ClaudeWebSearchResult always used, but which had no name. The result data
// with types is in a tool_use_result field at the top level. That field is
// BESIDE "message", and it is not inside message.content[].content. The inner
// field holds only a flat summary in text of the same data. Examples of the
// typed data are filePath and structuredPatch for Edit, and query and results
// for WebSearch. Refer to the comment on ClaudeWebSearchResult for an example
// of the two forms together.
//
// Use ParseClaudeToolResult above for that inner field with text. Use
// ExtractToolUseResult below for this field.
//
// A live test confirmed this for two kinds of tool: the lookup of a deferred
// tool by ToolSearch, and WebSearch. This pass did not capture Edit, Write or
// Bash again, because the temporary session in this capture never used them.
// Therefore their use of this same convention comes from the internal
// agreement of Claude Code, and no person confirmed it again. Make a new
// capture before you use this fact for work of high importance.
type claudeUserToolResultEvent struct {
	Type    string `json:"type"` // "user"
	Message struct {
		Role    string                   `json:"role"`
		Content []claudeToolResultBlock `json:"content"`
	} `json:"message"`
	ParentToolUseID string          `json:"parent_tool_use_id,omitempty"`
	SessionID       string          `json:"session_id"`
	UUID            string          `json:"uuid"`
	Timestamp       string          `json:"timestamp"`
	// The shape of ToolUseResult is different for each tool AND for each result.
	// Therefore this code keeps it raw and does not give it a type at this level.
	// A live test confirmed a minimum of three shapes:
	//
	//   1. A result object for one tool, for example ClaudeWebSearchResult.
	//   2. An object for a lazy load by ToolSearch, ClaudeToolSearchResult.
	//   3. A plain JSON string, when a person refuses a permission. An example is
	//      "Error: Claude requested permissions to use WebSearch, but you have
	//      not granted it yet." It has no object around it.
	//
	// A caller must try json.Unmarshal into the structure that it expects. It
	// must read a failure as "examine the condition with the plain string", and
	// not as a hard error of the parser.
	ToolUseResult json.RawMessage `json:"tool_use_result,omitempty"`
}

// ExtractToolUseResult takes two items out of one "user" event in
// stream-json. The first is the tool_use_id, from the tool_result block at
// message.content[0]. The second is the raw bytes of the tool_use_result
// field beside it.
//
// It returns ok=false when the event has no tool_use_result. That occurs for
// a usual chat turn from a person, and for a tool result where the CLI did
// not add the field.
//
// The code sets deniedText, and leaves resultJSON as nil, for the confirmed
// shape of a plain string that shows a refused permission. Thus a caller can
// see the difference between two conditions. The first: there is no result
// with types, because no person permitted this call to operate. The second:
// there is no result with types, because this event is not a tool result.
func ExtractToolUseResult(raw json.RawMessage) (toolUseID string, resultJSON json.RawMessage, deniedText string, ok bool, err error) {
	var ev claudeUserToolResultEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return "", nil, "", false, err
	}
	if len(ev.Message.Content) > 0 {
		toolUseID = ev.Message.Content[0].ToolUseID
	}
	if len(ev.ToolUseResult) == 0 {
		return toolUseID, nil, "", false, nil
	}
	var asString string
	if err := json.Unmarshal(ev.ToolUseResult, &asString); err == nil {
		return toolUseID, nil, asString, true, nil
	}
	return toolUseID, ev.ToolUseResult, "", true, nil
}

// ClaudeToolSearchResult agrees with the confirmed live shape of the
// tool_use_result for a lazy lookup by ToolSearch. That is the mechanism for
// the deferred load of a tool in Claude Code, which uses "select:ToolName".
// This is the ToolSearch tool of this repository, seen from the other side of
// the wire.
//
// The block at message.content[0] beside it is
// {"type":"tool_reference","tool_name":"..."}. It is not a usual tool_result
// with text or content. Know this different shape if you also read that
// block, and not only this field.
type ClaudeToolSearchResult struct {
	Query              string   `json:"query"`
	Matches            []string `json:"matches"`
	TotalDeferredTools int      `json:"total_deferred_tools"`
}

// claudeHunk matches structuredPatch's per-hunk shape natively.
type claudeHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// claudeEditResult covers the result shapes of Edit and of Write. Write uses
// the schema of Edit exactly. The one difference is an empty structuredPatch
// with a null originalFile. Refer to the finding "Whole-file write
// representation" in planning/agent_cli_interop.md section 1.
type claudeEditResult struct {
	FilePath        string       `json:"filePath"`
	Content         string       `json:"content"`      // populated for Write
	OriginalFile    *string      `json:"originalFile"` // null for Write, non-null for Edit
	StructuredPatch []claudeHunk `json:"structuredPatch"`
}

// ClaudeEditViews reads a result of an Edit tool or a Write tool and makes
// EditViews. An empty structuredPatch with a null originalFile is a
// convention of Claude. It means "this made or replaced a full file, and it
// is not a diff". This code reads that as a usual and correct WholeFile
// state, and not as an incomplete capture.
func ClaudeEditViews(resultJSON json.RawMessage) (EditViews, error) {
	var r claudeEditResult
	if err := json.Unmarshal(resultJSON, &r); err != nil {
		return EditViews{}, err
	}
	v := EditViews{Path: r.FilePath}

	if len(r.StructuredPatch) == 0 && r.OriginalFile == nil {
		v.WholeFile = &WholeFile{Content: r.Content, Overwrite: true}
		return v, nil
	}

	hunks := make([]DiffHunk, 0, len(r.StructuredPatch))
	for _, h := range r.StructuredPatch {
		hunks = append(hunks, DiffHunk{
			OldStart: h.OldStart, OldLines: h.OldLines,
			NewStart: h.NewStart, NewLines: h.NewLines,
			Lines: h.Lines,
		})
	}
	v.Hunks = hunks
	return v, nil
}

// The types below cover each other tool of Claude Code that a live test
// confirmed, and not Edit and Write. Refer to
// planning/agent_cli_interop.md section 1 and vendorpacks/claude.yaml.
//
// They are plain structures for input and results, and not Parse functions.
// ToolCall and ToolResult already keep Args and the result bytes as
// json.RawMessage. These types exist thus a caller can json.Unmarshal into
// something correct, and not into a map[string]any.
//
// Bash and Glob have no relation to a diff or an edit. Therefore this file
// has no EditViews function for them, and that is on purpose.

// ClaudeBashInput and ClaudeBashResult agree with the confirmed shape of
// Bash.
//
// The CLI puts even one call in a unit that it can follow, with
// system/task_started and then system/task_notification, and those carry
// task_id and task_type:"local_bash". This code does not model that, because
// it is a detail of how the stream-json CLI shows the work. That is one level
// above the pair of tool_use and tool_result in the Messages API, and this
// package operates on that pair.
type ClaudeBashInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type ClaudeBashResult struct {
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	Interrupted      bool   `json:"interrupted"`
	IsImage          bool   `json:"isImage"`
	NoOutputExpected bool   `json:"noOutputExpected"`
}

// ClaudeGlobInput/Result match Glob's confirmed shape.
type ClaudeGlobInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"` // optional, defaults to cwd
}

type ClaudeGlobResult struct {
	Filenames       []string `json:"filenames"`
	DurationMs      int      `json:"durationMs"`
	NumFiles        int      `json:"numFiles"`
	Truncated       bool     `json:"truncated"`
	TotalMatches    int      `json:"totalMatches"`
	CountIsComplete bool     `json:"countIsComplete"`
}

// ClaudeWebFetchInput and ClaudeWebFetchResult agree with the confirmed shape
// of WebFetch. This tool gets a page *and makes a summary of it*, because
// prompt is an instruction for the model to extract data. It does not get raw
// HTML. The model writes Result, and Result is not the page.
type ClaudeWebFetchInput struct {
	URL    string `json:"url"`
	Prompt string `json:"prompt"`
}

type ClaudeWebFetchResult struct {
	Bytes      int    `json:"bytes"`
	Code       int    `json:"code"`
	CodeText   string `json:"codeText"`
	Result     string `json:"result"` // model-authored text, not raw HTML
	DurationMs int    `json:"durationMs"`
	URL        string `json:"url"`
}

// ClaudeWebSearchInput and ClaudeWebSearchResult agree with the confirmed
// shape of WebSearch. This tool is hosted, thus it operates on the servers of
// Anthropic. Refer to the Hosted detection in ParseClaudeAssistantParts,
// which uses the srvtoolu_ prefix of the id.
//
// Give Result the bytes of the tool_use_result field BESIDE the message.
// Refer to ExtractToolUseResult. Do not give it message.content[].content,
// because that inner field holds only a flat copy in text of the same data:
//
// 	tool_use_result: {"query":"...", "results":[
// 	  {"tool_use_id":"srvtoolu_...", "content":[{"title":"...","url":"..."}, ...]},
// 	  "## Markdown-formatted synthesized summary...\n..."
// 	], "durationSeconds":6.4014, "searchCount":1}
//
// Corrected 2026-07-26 against a true capture. Results is an array with
// DIFFERENT types of element. First there is exactly one ClaudeWebSearchHit
// object, which holds the search results with structure. Then there is
// exactly one plain string. That string is a summary of those results, in
// markdown, and the model wrote it. An earlier guess in this file was not
// verified, and it assumed one type, []ClaudeWebSearchHit.
//
// DurationSeconds and SearchCount are both true fields, and a person
// confirmed that they are present. An earlier note said that they do not
// exist, and that note was incorrect. This replaces it with the observed
// data: "durationSeconds":6.401404199999999,"searchCount":1.
type ClaudeWebSearchInput struct {
	Query string `json:"query"`
}

type ClaudeWebSearchResult struct {
	Query           string                       `json:"query"`
	Results         []ClaudeWebSearchResultEntry `json:"results"`
	DurationSeconds float64                      `json:"durationSeconds"`
	SearchCount     int                          `json:"searchCount"`
}

// ClaudeWebSearchResultEntry is one element of the Results array, which has
// elements of different types. IsSummary shows which of the two confirmed
// shapes this element is. A caller must examine IsSummary before it reads Hit
// or Summary. Exactly one of those two has a value.
type ClaudeWebSearchResultEntry struct {
	IsSummary bool
	Hit       *ClaudeWebSearchHit
	Summary   string
}

func (e *ClaudeWebSearchResultEntry) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		e.IsSummary = true
		e.Summary = s
		return nil
	}
	var hit ClaudeWebSearchHit
	if err := json.Unmarshal(data, &hit); err != nil {
		return err
	}
	e.Hit = &hit
	return nil
}

func (e ClaudeWebSearchResultEntry) MarshalJSON() ([]byte, error) {
	if e.IsSummary {
		return json.Marshal(e.Summary)
	}
	return json.Marshal(e.Hit)
}

type ClaudeWebSearchHit struct {
	ToolUseID string                      `json:"tool_use_id"`
	Content   []ClaudeWebSearchHitContent `json:"content"`
}

type ClaudeWebSearchHitContent struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// ClaudeNotebookEditInput and ClaudeNotebookEditResult agree with the
// confirmed shape of NotebookEdit. This tool is in the same family as Write,
// because it gives the full content before and after. But it gives a true
// image before and a true image after, and Write uses a null originalFile
// instead. Therefore ClaudeNotebookEditViews puts values in Before and After,
// and not in WholeFile.
type ClaudeNotebookEditInput struct {
	NotebookPath string `json:"notebook_path"`
	NewSource    string `json:"new_source"`
	EditMode     string `json:"edit_mode"` // "insert" observed live
	CellType     string `json:"cell_type"` // "code" observed live
}

type ClaudeNotebookEditResult struct {
	NewSource    string `json:"new_source"`
	CellType     string `json:"cell_type"`
	Language     string `json:"language"`
	EditMode     string `json:"edit_mode"`
	CellID       string `json:"cell_id"`
	Error        string `json:"error,omitempty"`
	NotebookPath string `json:"notebook_path"`
	OriginalFile string `json:"original_file"` // full pre-image
	UpdatedFile  string `json:"updated_file"`  // full post-image
}

// ClaudeNotebookEditViews parses a NotebookEdit result into EditViews,
// natively populating Before/After (both are always full snapshots, per
// the confirmed live shape) rather than WholeFile or Hunks.
func ClaudeNotebookEditViews(resultJSON json.RawMessage) (EditViews, error) {
	var r ClaudeNotebookEditResult
	if err := json.Unmarshal(resultJSON, &r); err != nil {
		return EditViews{}, err
	}
	return EditViews{Path: r.NotebookPath, Before: r.OriginalFile, After: r.UpdatedFile}, nil
}

// ClaudeReadInput/Result: Read is polymorphic — a plain-text path returns
// {type omitted, file text inline}, a .ipynb path returns
// {type:"notebook", file:{filePath, cells:[]}} instead. Modeled as one
// struct with both shapes optional, discriminated by Type; a
// consumer checks Type == "notebook" before trusting NotebookFile.
type ClaudeReadInput struct {
	FilePath string `json:"file_path"`
}

type ClaudeReadResult struct {
	Type         string              `json:"type,omitempty"` // "notebook" for .ipynb; empty/absent for plain text
	Text         string              `json:"text,omitempty"`
	NotebookFile *ClaudeNotebookFile `json:"file,omitempty"`
}

type ClaudeNotebookFile struct {
	FilePath string          `json:"filePath"`
	Cells    json.RawMessage `json:"cells"` // cell structure not captured in the research pass — kept raw rather than guessed
}

// ClaudeTaskInput and ClaudeTaskResult agree with the confirmed shape of
// Task. The name on the wire is "Agent", and not "Task". Refer to
// agent_cli_interop.md section 1.
//
// This is the subagent mechanism of Claude itself. It is the example that the
// Delegate design in ngl_and_adapters.md is built on. It is also the
// mechanism that AGENTS.md tells a front CLI to use when it sends more than
// one independent task at the same time.
//
// The sequence task_started, task_progress, task_updated and
// task_notification is a detail of how the stream-json CLI shows the work. It
// is not part of this structure. ClaudeTaskResult models only the data of the
// final result.
type ClaudeTaskInput struct {
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	SubagentType    string `json:"subagent_type"`
	RunInBackground bool   `json:"run_in_background"`
}

type ClaudeTaskResult struct {
	Summary           string              `json:"summary"`
	TotalTokens       int                 `json:"totalTokens"`
	TotalDurationMs   int                 `json:"totalDurationMs"`
	TotalToolUseCount int                 `json:"totalToolUseCount"`
	ResolvedModel     string              `json:"resolvedModel"`
	ToolStats         ClaudeTaskToolStats `json:"toolStats"`
}

// ClaudeResultEvent agrees with the last "result" line in stream-json.
//
// A live test confirmed PermissionDenials on 2026-07-26, during the research
// pass on permission behaviour. That test ran with no
// --dangerously-skip-permissions flag, thus true refusals could occur. When a
// person refuses any tool call in the run, this event carries
// [{"tool_name":"Bash","tool_use_id":"...","tool_input":{...}}]. That is a
// clean signal with structure which shows "this run needed a permission", and
// no code must read prose. agy has nothing equal to this.
//
// The other fields below are Subtype, IsError, Result and SessionID. They are
// the smallest set that this research pass used. This code keeps the set
// small on purpose. It does not declare other names of fields, such as
// duration_ms or usage, which this pass did not confirm.
type ClaudeResultEvent struct {
	Type              string         `json:"type"` // "result"
	Subtype           string         `json:"subtype"`
	IsError           bool           `json:"is_error"`
	Result            string         `json:"result,omitempty"`
	SessionID         string         `json:"session_id,omitempty"`
	PermissionDenials []ClaudeDenial `json:"permission_denials,omitempty"`
}

// ClaudeDenial is one entry of ClaudeResultEvent's PermissionDenials array.
type ClaudeDenial struct {
	ToolName  string          `json:"tool_name"`
	ToolUseID string          `json:"tool_use_id"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type ClaudeTaskToolStats struct {
	ReadCount      int `json:"readCount"`
	SearchCount    int `json:"searchCount"`
	BashCount      int `json:"bashCount"`
	EditFileCount  int `json:"editFileCount"`
	LinesAdded     int `json:"linesAdded"`
	LinesRemoved   int `json:"linesRemoved"`
	OtherToolCount int `json:"otherToolCount"`
}
