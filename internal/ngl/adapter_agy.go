package ngl

import (
	"encoding/json"
	"fmt"
)

// agyToolCallEnvelope matches the confirmed live capture
// (planning/agent_cli_interop.md §3): sourceMetadata.tool.toolCall, with
// args as a *stringified* JSON blob (ArgumentsJSON), not a nested object —
// the one genuinely different encoding among the three vendors traced.
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

// ParseAgyToolCallPart decodes agy's tool-call envelope into a Part.
// Args is populated from the *decoded* argumentsJson content — always a
// real JSON value once it reaches ToolCall.Args, never a string containing
// JSON (the double-encoding is an agy wire-format detail, not something
// every downstream consumer should have to know about and re-parse).
// thinkingSignature becomes ReasoningToken — opaque, passthrough only,
// same treatment as Claude's thinking.signature.
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

// ParseAgyTurn assembles a Turn from one agy tool-call envelope — the
// Turn-level entry point wrapping ParseAgyToolCallPart. Unlike
// ParseClaudeTurn (many Parts from one content-block array) and
// ParseCursorAgentTurn (one Part from one stream-json line), agy's
// natural unit really is one tool call per envelope (one row in the
// steps table, per agent_cli_interop.md §3's SQLite-backed capture) — a
// real per-vendor granularity difference this function doesn't try to
// paper over.
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
// ok=false for any tool name that isn't edit-shaped, rather than guessing.
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
// universal fallback whenever a dedicated fs tool it doesn't actually
// have would otherwise be needed (agent_cli_interop.md §3).
type AgyRunCommandArgs struct {
	CommandLine string `json:"CommandLine"`
	Cwd         string `json:"Cwd"`
}
