package ngl

import (
	"encoding/json"
	"fmt"
)

// agyToolCallEnvelope agrees with the live capture that a person confirmed.
// Refer to planning/agent_cli_interop.md §3. The path is
// sourceMetadata.tool.toolCall.
//
// The args are a *text* with JSON inside, in ArgumentsJSON. They are not an
// object inside the structure. Of the three vendors that a person traced, this
// is the one encoding that is truly different.
type agyToolCallEnvelope struct {
	SourceMetadata struct {
		Tool struct {
			ToolCall struct {
				ID                string `json:"id"`
				Name              string `json:"name"`
				ArgumentsJSON     string `json:"argumentsJson"` // double-encoded: a string containing JSON, not a JSON object
				ThinkingSignature string `json:"thinkingSignature"`
				OriginalName      string `json:"originalName"`
			} `json:"toolCall"`
		} `json:"tool"`
	} `json:"sourceMetadata"`
}

// ParseAgyToolCallPart changes the tool-call envelope of agy into a Part.
//
// The code takes Args from the *decoded* content of argumentsJson. When the
// value arrives at ToolCall.Args, it is always a true JSON value. It is never a
// text with JSON inside. The double encoding is a detail of the wire format of
// agy, and each consumer after this point must not know it and must not parse
// it again.
//
// thinkingSignature becomes ReasoningToken. It has no structure, and the code
// only sends it through. This is the same treatment as thinking.signature of
// Claude.
func ParseAgyToolCallPart(raw json.RawMessage) (Part, error) {
	var env agyToolCallEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Part{}, err
	}
	tc := env.SourceMetadata.Tool.ToolCall
	if tc.Name == "" {
		return Part{}, fmt.Errorf("ngl: no toolCall found in agy envelope")
	}
	if !json.Valid([]byte(tc.ArgumentsJSON)) {
		return Part{}, fmt.Errorf("ngl: agy argumentsJson is not valid JSON: %q", tc.ArgumentsJSON)
	}
	return Part{
		Kind: PartToolCall,
		ToolCall: &ToolCall{
			ID:   tc.ID,
			Name: tc.Name,
			Args: json.RawMessage(tc.ArgumentsJSON),
		},
		ReasoningToken: tc.ThinkingSignature,
	}, nil
}

// ParseAgyTurn makes a Turn from one tool-call envelope of agy. It is the entry
// point at the level of a Turn, and it uses ParseAgyToolCallPart.
//
// The unit of agy is truly one tool call for each envelope, which is one row in
// the steps table. Refer to the capture with SQLite in
// agent_cli_interop.md §3.
//
// ParseClaudeTurn is different: it makes many Parts from one array of content
// blocks. ParseCursorAgentTurn is also different: it makes one Part from one
// line of stream-json.
//
// This is a true difference in the unit of each vendor, and this function does
// not hide it.
func ParseAgyTurn(raw json.RawMessage) (Turn, error) {
	part, err := ParseAgyToolCallPart(raw)
	if err != nil {
		return Turn{}, err
	}
	return Turn{Vendor: "agy", Raw: raw, Parts: []Part{part}}, nil
}

// agyReplaceFileContentArgs matches replace_file_content's confirmed args
// shape — structurally closer to an LSP TextEdit{range, newText} plus an
// optimistic-concurrency check than to a diff.
type agyReplaceFileContentArgs struct {
	TargetFile         string `json:"TargetFile"`
	StartLine          int    `json:"StartLine"`
	EndLine            int    `json:"EndLine"`
	TargetContent      string `json:"TargetContent"`      // expected old lines, for verification
	ReplacementContent string `json:"ReplacementContent"` // new lines
}

// agyWriteToFileArgs matches write_to_file's confirmed args shape —
// confirmed as the tool for both creating new files and overwriting
// existing ones; no distinct create_file was ever actually invoked.
type agyWriteToFileArgs struct {
	TargetFile  string `json:"TargetFile"`
	CodeContent string `json:"CodeContent"`
	Overwrite   bool   `json:"Overwrite"`
}

// AgyEditViews dispatches on toolName to the right args shape — agy is the
// vendor that proved a single canonical EditTool type is wrong (two
// structurally different edit shapes, confirmed live from one vendor).
// ok=false for any tool name that is not edit-shaped, rather than guessing.
func AgyEditViews(toolName string, argsJSON json.RawMessage) (views EditViews, ok bool, err error) {
	switch toolName {
	case "replace_file_content":
		var a agyReplaceFileContentArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return EditViews{}, false, err
		}
		return EditViews{
			Path: a.TargetFile,
			RangeReplace: &RangeReplace{
				StartLine: a.StartLine, EndLine: a.EndLine,
				OldSnapshot: a.TargetContent, New: a.ReplacementContent,
			},
		}, true, nil
	case "write_to_file":
		var a agyWriteToFileArgs
		if err := json.Unmarshal(argsJSON, &a); err != nil {
			return EditViews{}, false, err
		}
		return EditViews{
			Path:      a.TargetFile,
			WholeFile: &WholeFile{Content: a.CodeContent, Overwrite: a.Overwrite},
		}, true, nil
	default:
		return EditViews{}, false, nil
	}
}

// AgyViewFileArgs/ListDirArgs/RunCommandArgs match the three other
// confirmed-live agy tools (planning/agent_cli_interop.md §3,
// vendorpacks/agy.yaml). None have an edit/diff angle, so — same as
// Claude's Bash/Glob — there is deliberately no EditViews producer for
// any of these.
type AgyViewFileArgs struct {
	AbsolutePath string `json:"AbsolutePath"`
}

// AgyListDirArgs matches list_dir's confirmed args — the live wire name,
// not the wire-catalog's "list_directory" (never actually invoked; see
// vendorpacks/agy.yaml's confirmed:false entry for that name).
type AgyListDirArgs struct {
	DirectoryPath string `json:"DirectoryPath"`
}

// AgyRunCommandArgs matches run_command's confirmed args — agy's
// universal fallback whenever a dedicated fs tool it does not actually
// have would otherwise be needed (agent_cli_interop.md §3).
type AgyRunCommandArgs struct {
	CommandLine string `json:"CommandLine"`
	Cwd         string `json:"Cwd"`
}
