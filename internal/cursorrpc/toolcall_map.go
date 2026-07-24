package cursorrpc

import (
	"encoding/json"
	"strings"
)

// Cursor agent.v1 ToolCall oneof field numbers (planning/vendor_ref/agent_v1.proto).
const (
	toolCallFieldShell     = 1
	toolCallFieldDelete    = 3
	toolCallFieldGlob      = 4
	toolCallFieldGrep      = 5
	toolCallFieldRead      = 8
	toolCallFieldEdit      = 12
	toolCallFieldLs        = 13
	toolCallFieldWebSearch = 18
	toolCallFieldFetch     = 24
	toolCallFieldTruncated = 34
	toolCallFieldWebFetch  = 37
)

// ToolNameMapping documents OpenAI / Cursor / Glider name aliases → Cursor wire
// variant + Glider builtin (when the hoop tool loop would run the same intent).
type ToolNameMapping struct {
	CursorVariant string // e.g. "read_tool_call"
	GliderBuiltin string // e.g. "fs_read"; empty if UI-only / no builtin
	WireField     int
}

// CommonToolNameMappings is the opt-in Path B catalog (P1-4).
var CommonToolNameMappings = map[string]ToolNameMapping{
	// Read
	"read":      {CursorVariant: "read_tool_call", GliderBuiltin: "fs_read", WireField: toolCallFieldRead},
	"read_file": {CursorVariant: "read_tool_call", GliderBuiltin: "fs_read", WireField: toolCallFieldRead},
	"Read":      {CursorVariant: "read_tool_call", GliderBuiltin: "fs_read", WireField: toolCallFieldRead},
	"fs_read":   {CursorVariant: "read_tool_call", GliderBuiltin: "fs_read", WireField: toolCallFieldRead},
	// Grep
	"grep":      {CursorVariant: "grep_tool_call", GliderBuiltin: "code_grep", WireField: toolCallFieldGrep},
	"Grep":      {CursorVariant: "grep_tool_call", GliderBuiltin: "code_grep", WireField: toolCallFieldGrep},
	"code_grep": {CursorVariant: "grep_tool_call", GliderBuiltin: "code_grep", WireField: toolCallFieldGrep},
	"ripgrep":   {CursorVariant: "grep_tool_call", GliderBuiltin: "code_grep", WireField: toolCallFieldGrep},
	// Write / edit
	"write":          {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	"Write":          {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	"edit":           {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	"Edit":           {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	"StrReplace":     {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	"search_replace": {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	"fs_write":       {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	// Shell (wire always; Glider execution still gated by orchestration.tools.allow_shell)
	"shell":      {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	"Shell":      {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	"bash":       {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	"shell_exec": {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	// Delete
	"delete":    {CursorVariant: "delete_tool_call", GliderBuiltin: "", WireField: toolCallFieldDelete},
	"Delete":    {CursorVariant: "delete_tool_call", GliderBuiltin: "", WireField: toolCallFieldDelete},
	"fs_delete": {CursorVariant: "delete_tool_call", GliderBuiltin: "", WireField: toolCallFieldDelete},
	// Glob / search
	"glob":      {CursorVariant: "glob_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldGlob},
	"Glob":      {CursorVariant: "glob_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldGlob},
	"fs_search": {CursorVariant: "glob_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldGlob},
	// Ls / list
	"ls":       {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	"Ls":       {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	"list_dir": {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	"fs_list":  {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	// Web
	"web_search": {CursorVariant: "web_search_tool_call", GliderBuiltin: "web_search", WireField: toolCallFieldWebSearch},
	"WebSearch":  {CursorVariant: "web_search_tool_call", GliderBuiltin: "web_search", WireField: toolCallFieldWebSearch},
	"web_fetch":  {CursorVariant: "web_fetch_tool_call", GliderBuiltin: "web_fetch", WireField: toolCallFieldWebFetch},
	"WebFetch":   {CursorVariant: "web_fetch_tool_call", GliderBuiltin: "web_fetch", WireField: toolCallFieldWebFetch},
	"http_fetch": {CursorVariant: "fetch_tool_call", GliderBuiltin: "http_fetch", WireField: toolCallFieldFetch},
	"fetch":      {CursorVariant: "fetch_tool_call", GliderBuiltin: "http_fetch", WireField: toolCallFieldFetch},
	"Fetch":      {CursorVariant: "fetch_tool_call", GliderBuiltin: "http_fetch", WireField: toolCallFieldFetch},
}

// LookupToolNameMapping resolves a tool name (case-sensitive first, then fold).
func LookupToolNameMapping(name string) (ToolNameMapping, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolNameMapping{}, false
	}
	if m, ok := CommonToolNameMappings[name]; ok {
		return m, true
	}
	lower := strings.ToLower(name)
	for k, m := range CommonToolNameMappings {
		if strings.ToLower(k) == lower {
			return m, true
		}
	}
	return ToolNameMapping{}, false
}

// MapToolNameToGliderBuiltin returns the Glider builtin for a Cursor/OpenAI tool name.
func MapToolNameToGliderBuiltin(name string) (string, bool) {
	m, ok := LookupToolNameMapping(name)
	if !ok || m.GliderBuiltin == "" {
		return "", false
	}
	return m.GliderBuiltin, true
}

// EncodeMappedToolCallWire builds ToolCall{<variant>: {args: …}} from an OpenAI
// function name + JSON arguments. Unknown names → TruncatedToolCall (field 34).
// callID is optional (embedded in Shell/Grep/Ls/Delete/Web* args when present).
func EncodeMappedToolCallWire(name, argsJSON, callID string) []byte {
	m, ok := LookupToolNameMapping(name)
	if !ok {
		return EncodeTruncatedToolCallWire()
	}
	args := encodeCursorToolArgs(m.WireField, argsJSON, callID)
	inner := protoBytesField(1, args) // XxxToolCall.args = 1
	return protoBytesField(m.WireField, inner)
}

func encodeCursorToolArgs(wireField int, argsJSON, callID string) []byte {
	fields := parseToolArgMap(argsJSON)
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := fields[k]; ok {
				return v
			}
		}
		return ""
	}
	switch wireField {
	case toolCallFieldRead:
		// ReadToolArgs: path=1
		path := str("path", "file", "file_path", "target_file")
		return protoBytesField(1, []byte(path))
	case toolCallFieldGrep:
		// GrepArgs: pattern=1, path=2, glob=3, tool_call_id=14
		out := protoBytesField(1, []byte(str("pattern", "query", "q", "regex")))
		if p := str("path", "dir", "directory"); p != "" {
			out = append(out, protoBytesField(2, []byte(p))...)
		}
		if g := str("glob", "include"); g != "" {
			out = append(out, protoBytesField(3, []byte(g))...)
		}
		if callID != "" {
			out = append(out, protoBytesField(14, []byte(callID))...)
		}
		return out
	case toolCallFieldEdit:
		// EditArgs: path=1, stream_content=6
		path := str("path", "file", "file_path", "target_file")
		out := protoBytesField(1, []byte(path))
		if c := str("contents", "content", "new_string", "stream_content", "text"); c != "" {
			out = append(out, protoBytesField(6, []byte(c))...)
		}
		return out
	case toolCallFieldShell:
		// ShellArgs: command=1, working_directory=2, tool_call_id=4
		cmd := str("command", "cmd", "input")
		out := protoBytesField(1, []byte(cmd))
		if wd := str("working_directory", "cwd", "workdir"); wd != "" {
			out = append(out, protoBytesField(2, []byte(wd))...)
		}
		if callID != "" {
			out = append(out, protoBytesField(4, []byte(callID))...)
		}
		return out
	case toolCallFieldDelete:
		path := str("path", "file", "file_path")
		out := protoBytesField(1, []byte(path))
		if callID != "" {
			out = append(out, protoBytesField(2, []byte(callID))...)
		}
		return out
	case toolCallFieldGlob:
		// GlobToolArgs: target_directory=1, glob_pattern=2
		var out []byte
		if d := str("target_directory", "path", "dir"); d != "" {
			out = append(out, protoBytesField(1, []byte(d))...)
		}
		pat := str("glob_pattern", "pattern", "glob", "query")
		out = append(out, protoBytesField(2, []byte(pat))...)
		return out
	case toolCallFieldLs:
		path := str("path", "dir", "directory")
		if path == "" {
			path = "."
		}
		out := protoBytesField(1, []byte(path))
		if callID != "" {
			out = append(out, protoBytesField(3, []byte(callID))...)
		}
		return out
	case toolCallFieldWebSearch:
		term := str("search_term", "query", "q", "input")
		out := protoBytesField(1, []byte(term))
		if callID != "" {
			out = append(out, protoBytesField(2, []byte(callID))...)
		}
		return out
	case toolCallFieldWebFetch, toolCallFieldFetch:
		url := str("url", "uri", "href")
		out := protoBytesField(1, []byte(url))
		if callID != "" {
			out = append(out, protoBytesField(2, []byte(callID))...)
		}
		return out
	default:
		return nil
	}
}

func parseToolArgMap(argsJSON string) map[string]string {
	out := map[string]string{}
	argsJSON = strings.TrimSpace(argsJSON)
	if argsJSON == "" || argsJSON == "{}" {
		return out
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return out
	}
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64, bool:
			b, err := json.Marshal(t)
			if err == nil {
				out[k] = string(b)
			}
		default:
			b, err := json.Marshal(t)
			if err == nil {
				out[k] = string(b)
			}
		}
	}
	return out
}

// ToolCallWireVariant peeks the ToolCall oneof field number from a wire blob.
// Returns 0 when unparseable; 34 for TruncatedToolCall.
func ToolCallWireVariant(toolWire []byte) int {
	if len(toolWire) == 0 {
		return 0
	}
	key, n := readVarint(toolWire)
	if n <= 0 {
		return 0
	}
	if int(key&7) != 2 {
		return 0
	}
	return int(key >> 3)
}
