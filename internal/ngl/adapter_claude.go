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

// ParseClaudeAssistantParts turns an assistant message's content-block
// array into Parts — text blocks become PartUserText (best-effort reuse of
// that Kind for "plain text authored by this turn's speaker", not
// specifically human input, since assistant messages don't have their own
// dedicated Kind yet) and tool_use blocks become PartToolCall.
//
// Hosted is set from the tool_use id's prefix: Anthropic's own
// WebSearch/WebFetch-class hosted tools (executed server-side on
// Anthropic's infra, not locally) use a distinct `srvtoolu_` id, vs.
// every client-side tool's `toolu_` (agent_cli_interop.md §1). This is a
// real, load-bearing distinction for delegation: a hosted tool call has no
// local process to spin up, so it can never be delegated to a headless
// session the way a local tool call can — the id prefix is Claude's own
// signal for exactly that fact, not an NGL invention.
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

// ParseClaudeTurn assembles a full Turn from an assistant message's raw
// content-block array — the Turn-level entry point wrapping
// ParseClaudeAssistantParts, keeping raw bytes on the result per Turn's
// own contract (always re-emittable to the same vendor untouched).
func ParseClaudeTurn(raw json.RawMessage) (Turn, error) {
	parts, err := ParseClaudeAssistantParts(raw)
	if err != nil {
		return Turn{}, err
	}
	return Turn{Vendor: "claude", Raw: raw, Parts: parts}, nil
}

// claudeToolResultBlock is a tool_result block, submitted back as part of a
// role="user" message — the exact shape ngl.go's scaffold-stripping fix
// (see that file's package doc) exists to keep separate from genuine human text.
type claudeToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error,omitempty"`
	// Content can be a bare string or a nested block array in the general
	// case; Claude's own tool_result payloads observed live are plain
	// strings or the structured edit-result shape handled separately by
	// ClaudeEditViews — ParseClaudeToolResult only needs the plain-text form.
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

// claudeUserToolResultEvent matches the FULL outer envelope of a
// stream-json "user" line that's actually carrying a tool result back —
// confirmed live 2026-07-26 (real captured session, ANTHROPIC_BASE_URL
// pointed at a local test harness). This is the structural fact
// ClaudeEditViews/ClaudeWebSearchResult were always implicitly written
// against but never had a name for: the *rich, typed* result data
// (filePath+structuredPatch for Edit, query+results for WebSearch, ...)
// lives in a top-level tool_use_result field SIBLING to "message", not
// inside message.content[].content — that inner field only ever carries a
// flattened, best-effort textual summary of the same data (see
// ClaudeWebSearchResult's doc comment for a concrete side-by-side
// example). ParseClaudeToolResult (above) is the right function for that
// inner textual field; ExtractToolUseResult (below) is the right function
// for this one.
//
// Confirmed live for two tool kinds so far (ToolSearch's deferred-tool
// lookup and WebSearch); Edit/Write/Bash weren't independently
// re-captured in this pass (this capture's scratch session never
// triggered them), so their use of this same sibling-field convention is
// inferred from Claude Code's own internal consistency, not independently
// reconfirmed — worth a fresh capture before leaning on it for something
// high-stakes.
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
	// ToolUseResult's shape genuinely varies by tool AND by outcome — kept
	// raw rather than typed at this layer. Confirmed live to take at least
	// three shapes: a tool-specific result object (e.g.
	// ClaudeWebSearchResult), a ToolSearch lazy-load object
	// (ClaudeToolSearchResult), and — on a permission denial — a bare JSON
	// string ("Error: Claude requested permissions to use WebSearch, but
	// you haven't granted it yet.") with no object wrapper at all. A caller
	// must try json.Unmarshal into the specific expected struct and treat a
	// failure as "check for the bare-string denial case" rather than a hard
	// parse error.
	ToolUseResult json.RawMessage `json:"tool_use_result,omitempty"`
}

// ExtractToolUseResult pulls the tool_use_id (from the paired
// message.content[0] tool_result block) and the sibling tool_use_result
// field's raw bytes out of one stream-json "user" event. ok=false when
// this event carries no tool_use_result at all (an ordinary user chat
// turn, or a tool result whose CLI happened not to attach one).
// deniedText is set (with resultJSON left nil) for the confirmed bare-
// string permission-denial shape, so a caller can distinguish "there is
// no rich result because this call was never allowed to run" from "there
// is no rich result because this event isn't a tool result."
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

// ClaudeToolSearchResult matches the confirmed live shape of a
// ToolSearch lazy-load lookup's tool_use_result — the mechanism behind
// Claude Code's "select:ToolName" deferred-tool loading (this codebase's
// own ToolSearch tool, from the other side of the wire). The paired
// message.content[0] block is {"type":"tool_reference","tool_name":"..."}
// rather than a normal text/content tool_result, a distinct shape worth
// knowing about if parsing that block too, not just this sibling field.
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

// claudeEditResult covers both Edit's and Write's result shapes — Write
// reuses Edit's schema exactly, distinguished only by an empty
// structuredPatch + null originalFile (planning/agent_cli_interop.md §1's
// "Whole-file write representation" finding).
type claudeEditResult struct {
	FilePath        string       `json:"filePath"`
	Content         string       `json:"content"`      // populated for Write
	OriginalFile    *string      `json:"originalFile"` // null for Write, non-null for Edit
	StructuredPatch []claudeHunk `json:"structuredPatch"`
}

// ClaudeEditViews parses an Edit or Write tool result into EditViews.
// Empty structuredPatch + null originalFile is Claude's own convention for
// "this was a create/overwrite, not a diff" — treated as a normal,
// well-formed WholeFile state, not a partial capture.
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

// The types below cover every other confirmed-live Claude Code tool
// (planning/agent_cli_interop.md §1, vendorpacks/claude.yaml) beyond
// Edit/Write — kept as plain typed input/result structs rather than
// bespoke Parse functions, since Args/result bytes are already
// json.RawMessage on ToolCall/ToolResult and these types exist so a
// caller can json.Unmarshal into something accurate instead of
// map[string]any. Bash and Glob have no diff/edit angle at all, so there
// is deliberately no EditViews producer for them.

// ClaudeBashInput/Result match Bash's confirmed shape. Wrapped in an async
// trackable unit even for one call (system/task_started ->
// system/task_notification carrying task_id/task_type:"local_bash") at
// the stream-json CLI layer — not modeled here, since that's a CLI
// presentation detail one layer above the Messages API tool_use/tool_result
// pair this package works with.
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

// ClaudeWebFetchInput/Result match WebFetch's confirmed shape — this is
// fetch-*and-summarize* (prompt is a model-extraction instruction), not
// raw HTML retrieval; Result is model-authored text, not the page itself.
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

// ClaudeWebSearchInput/Result match WebSearch's confirmed shape — hosted
// (server-side on Anthropic's infra, see ParseClaudeAssistantParts'
// Hosted detection via the srvtoolu_ id prefix). Result must be fed the
// SIBLING tool_use_result field's bytes (see ExtractToolUseResult), not
// message.content[].content — that inner field only ever carries a
// flattened textual echo of the same data:
//
//	tool_use_result: {"query":"...", "results":[
//	  {"tool_use_id":"srvtoolu_...", "content":[{"title":"...","url":"..."}, ...]},
//	  "## Markdown-formatted synthesized summary...\n..."
//	], "durationSeconds":6.4014, "searchCount":1}
//
// Corrected 2026-07-26 against a real capture: Results is a genuinely
// HETEROGENEOUS array — exactly one ClaudeWebSearchHit object (the
// structured search hits) followed by exactly one plain string (a
// model-synthesized, markdown-formatted summary of those hits) — not a
// homogeneous []ClaudeWebSearchHit as an earlier, unverified guess in
// this file assumed. DurationSeconds and SearchCount both ARE real,
// confirmed present fields (an earlier note claiming they don't exist was
// itself wrong — this replaces that claim with the actually-observed
// data: "durationSeconds":6.401404199999999,"searchCount":1).
type ClaudeWebSearchInput struct {
	Query string `json:"query"`
}

type ClaudeWebSearchResult struct {
	Query           string                       `json:"query"`
	Results         []ClaudeWebSearchResultEntry `json:"results"`
	DurationSeconds float64                      `json:"durationSeconds"`
	SearchCount     int                          `json:"searchCount"`
}

// ClaudeWebSearchResultEntry is one element of the heterogeneous Results
// array. IsSummary discriminates which of the two confirmed shapes this
// entry actually is — a caller must check it before reading Hit or
// Summary; exactly one of them is populated.
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

// ClaudeNotebookEditInput/Result match NotebookEdit's confirmed shape —
// same family as Write (full before/after content), but with genuine
// pre-image + post-image both present (unlike Write's null originalFile
// convention), so ClaudeNotebookEditViews populates Before/After rather
// than WholeFile.
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

// ClaudeTaskInput/Result match Task's confirmed shape — wire name is
// actually "Agent", not "Task" (agent_cli_interop.md §1); this is Claude's
// own subagent-delegation primitive, the concrete precedent
// ngl_and_adapters.md's Delegate design is built on. The async
// lifecycle (task_started -> task_progress -> task_updated ->
// task_notification) is a stream-json CLI presentation detail, not part
// of this struct — ClaudeTaskResult models the final result payload only.
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

// ClaudeResultEvent matches the terminal "result" stream-json line.
// PermissionDenials is confirmed live 2026-07-26 (permission-behavior
// research pass, run without --dangerously-skip-permissions to let genuine
// denials occur): whenever any tool call in the run was denied, this event
// carries [{"tool_name":"Bash","tool_use_id":"...","tool_input":{...}}] —
// a clean, structured "this run needed permission" signal, no prose-
// parsing required (contrast agy, which has nothing like this). Other
// fields below (Subtype/IsError/Result/SessionID) are the minimal set this
// research pass actually exercised — kept deliberately small rather than
// asserting additional field names (duration_ms, usage, etc.) this pass
// didn't independently confirm.
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
