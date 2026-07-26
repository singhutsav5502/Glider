package ngl_test

import (
	"encoding/json"
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

// Shapes below match planning/agent_cli_interop.md §3's captured findings
// verbatim, including the double-encoded argumentsJson field — the one
// genuinely different args-encoding convention among the three vendors.

func TestParseAgyToolCallPart_DoubleEncodedArgs(t *testing.T) {
	raw := []byte(`{
		"sourceMetadata": {
			"tool": {
				"toolCall": {
					"id": "tPSyDgZm",
					"name": "run_command",
					"argumentsJson": "{\"CommandLine\":\"ls -la\",\"Cwd\":\"/repo\",\"toolAction\":\"list\",\"toolSummary\":\"listing files\"}",
					"thinkingSignature": "opaque-sig-bytes",
					"originalName": "run_command"
				}
			}
		}
	}`)
	part, err := ngl.ParseAgyToolCallPart(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if part.Kind != ngl.PartToolCall || part.ToolCall == nil {
		t.Fatalf("got %+v", part)
	}
	if part.ToolCall.ID != "tPSyDgZm" || part.ToolCall.Name != "run_command" {
		t.Fatalf("got ToolCall %+v", part.ToolCall)
	}
	if part.ReasoningToken != "opaque-sig-bytes" {
		t.Fatalf("got ReasoningToken %q", part.ReasoningToken)
	}

	// The core of the double-encoding fix: Args must be a real, directly
	// unmarshalable JSON value, not a string containing JSON that callers
	// would have to double-decode themselves.
	var decodedArgs struct {
		CommandLine string `json:"CommandLine"`
		Cwd         string `json:"Cwd"`
	}
	if err := json.Unmarshal(part.ToolCall.Args, &decodedArgs); err != nil {
		t.Fatalf("ToolCall.Args was not directly unmarshalable JSON: %v (raw: %s)", err, part.ToolCall.Args)
	}
	if decodedArgs.CommandLine != "ls -la" || decodedArgs.Cwd != "/repo" {
		t.Fatalf("got decoded args %+v", decodedArgs)
	}
}

func TestParseAgyTurn_SetsVendorAndRaw(t *testing.T) {
	raw := []byte(`{"sourceMetadata":{"tool":{"toolCall":{"id":"x1","name":"view_file","argumentsJson":"{\"AbsolutePath\":\"math_utils.py\"}"}}}}`)
	turn, err := ngl.ParseAgyTurn(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if turn.Vendor != "agy" {
		t.Fatalf("got vendor %q", turn.Vendor)
	}
	if len(turn.Parts) != 1 || turn.Parts[0].ToolCall.Name != "view_file" {
		t.Fatalf("got parts %+v", turn.Parts)
	}
}

func TestAgySimpleToolArgs_MatchDocumentedShapes(t *testing.T) {
	var view ngl.AgyViewFileArgs
	if err := json.Unmarshal([]byte(`{"AbsolutePath":"math_utils.py"}`), &view); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.AbsolutePath != "math_utils.py" {
		t.Fatalf("got %+v", view)
	}

	var list ngl.AgyListDirArgs
	if err := json.Unmarshal([]byte(`{"DirectoryPath":"/repo/src"}`), &list); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list.DirectoryPath != "/repo/src" {
		t.Fatalf("got %+v", list)
	}

	var run ngl.AgyRunCommandArgs
	if err := json.Unmarshal([]byte(`{"CommandLine":"ls -la","Cwd":"/repo"}`), &run); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if run.CommandLine != "ls -la" || run.Cwd != "/repo" {
		t.Fatalf("got %+v", run)
	}
}

func TestParseAgyToolCallPart_RejectsInvalidArgumentsJSON(t *testing.T) {
	raw := []byte(`{"sourceMetadata":{"tool":{"toolCall":{"id":"x","name":"run_command","argumentsJson":"not valid json"}}}}`)
	_, err := ngl.ParseAgyToolCallPart(raw)
	if err == nil {
		t.Fatalf("expected an error for malformed argumentsJson, got nil")
	}
}

// TestAgyEditViews_ReplaceFileContent matches replace_file_content's
// confirmed args shape exactly.
func TestAgyEditViews_ReplaceFileContent(t *testing.T) {
	args := []byte(`{
		"TargetFile": "math_utils.py",
		"StartLine": 10,
		"EndLine": 12,
		"TargetContent": "def add(a, b):\n    return a + b",
		"ReplacementContent": "def add(a, b, c=0):\n    return a + b + c",
		"Instruction": "add optional third parameter",
		"AllowMultiple": false
	}`)
	views, ok, err := ngl.AgyEditViews("replace_file_content", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true for replace_file_content")
	}
	if views.Path != "math_utils.py" {
		t.Fatalf("got path %q", views.Path)
	}
	if views.RangeReplace == nil {
		t.Fatalf("expected RangeReplace to be populated")
	}
	if views.RangeReplace.StartLine != 10 || views.RangeReplace.EndLine != 12 {
		t.Fatalf("got range %+v", views.RangeReplace)
	}
	if views.Hunks != nil || views.WholeFile != nil {
		t.Fatalf("replace_file_content must not populate Hunks or WholeFile — agy has no unified-diff representation")
	}
}

// TestAgyEditViews_WriteToFile matches write_to_file's confirmed args
// shape exactly — confirmed live as covering both create and overwrite.
func TestAgyEditViews_WriteToFile(t *testing.T) {
	args := []byte(`{
		"CodeContent": "def hello():\n    print('hi')\n",
		"Description": "new helper module",
		"Overwrite": true,
		"TargetFile": "new_module.py",
		"toolAction": "write",
		"toolSummary": "created new_module.py"
	}`)
	views, ok, err := ngl.AgyEditViews("write_to_file", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true for write_to_file")
	}
	if views.WholeFile == nil {
		t.Fatalf("expected WholeFile to be populated")
	}
	if views.WholeFile.Content != "def hello():\n    print('hi')\n" || !views.WholeFile.Overwrite {
		t.Fatalf("got WholeFile %+v", views.WholeFile)
	}
	if views.RangeReplace != nil {
		t.Fatalf("write_to_file must not populate RangeReplace")
	}
}

func TestAgyEditViews_NonEditToolReturnsNotOK(t *testing.T) {
	_, ok, err := ngl.AgyEditViews("run_command", []byte(`{"CommandLine":"ls"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for a non-edit tool — must not guess a diff view that doesn't exist")
	}
}

// TestAgyEditViews_RangeReplaceHasNoDirectDiffRepresentation confirms the
// documented finding directly: agy's edit shapes have no unified-diff/hunk
// representation at all, and asking for one must fail explicitly rather
// than fabricate a plausible-looking diff — no {"range_replace","hunks"}
// converter is registered because it would need the full pre-image file
// content, which a RangeReplace alone never carries.
func TestAgyEditViews_RangeReplaceHasNoDirectDiffRepresentation(t *testing.T) {
	args := []byte(`{"TargetFile":"x.py","StartLine":1,"EndLine":2,"TargetContent":"old","ReplacementContent":"new"}`)
	views, _, err := ngl.AgyEditViews("replace_file_content", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := views.Get("hunks"); ok {
		t.Fatalf("hunks should be unavailable from a bare RangeReplace — no pre-image file content to diff against")
	}
	if _, ok := views.Get("unified_text"); ok {
		t.Fatalf("unified_text should be unavailable from a bare RangeReplace for the same reason")
	}
}
