package cursorrpc

import (
	"encoding/json"
	"strings"
)

// Cursor agent.v1 ToolCall oneof field numbers (planning/vendor_ref/agent_v1.proto).
// Pin: vendor_ref extract — extend only from that schema / observed frames.
const (
	toolCallFieldShell            = 1
	toolCallFieldDelete           = 3
	toolCallFieldGlob             = 4
	toolCallFieldGrep             = 5
	toolCallFieldRead             = 8
	toolCallFieldUpdateTodos      = 9
	toolCallFieldReadTodos        = 10
	toolCallFieldEdit             = 12
	toolCallFieldLs               = 13
	toolCallFieldReadLints        = 14
	toolCallFieldMcp              = 15
	toolCallFieldSemSearch        = 16
	toolCallFieldCreatePlan       = 17
	toolCallFieldWebSearch        = 18
	toolCallFieldTask             = 19
	toolCallFieldListMcpResources = 20
	toolCallFieldReadMcpResource  = 21
	toolCallFieldApplyAgentDiff   = 22
	toolCallFieldAskQuestion      = 23
	toolCallFieldFetch            = 24
	toolCallFieldSwitchMode       = 25
	toolCallFieldExaSearch        = 26
	toolCallFieldExaFetch         = 27
	toolCallFieldGenerateImage    = 28
	toolCallFieldWriteShellStdin  = 31
	toolCallFieldReflect          = 32
	toolCallFieldTruncated        = 34
	toolCallFieldWebFetch         = 37
)

// ToolNameMapping documents OpenAI / Cursor / Glider name aliases → Cursor wire
// variant + Glider builtin (when the hoop tool loop would run the same intent).
type ToolNameMapping struct {
	CursorVariant string // e.g. "read_tool_call"
	GliderBuiltin string // e.g. "fs_read"; empty if UI-only / no builtin
	WireField     int
}

// CommonToolNameMappings is the opt-in Path B catalog.
// Unknown names fall back to TruncatedToolCall (field 34).
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
	"ApplyPatch":     {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	"fs_write":       {CursorVariant: "edit_tool_call", GliderBuiltin: "fs_write", WireField: toolCallFieldEdit},
	// Shell (wire always; Glider execution still gated by orchestration.tools.allow_shell)
	"shell":            {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	"Shell":            {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	"bash":             {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	"ShellExec":        {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	"shell_exec":       {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	"run_terminal_cmd": {CursorVariant: "shell_tool_call", GliderBuiltin: "shell_exec", WireField: toolCallFieldShell},
	// Delete
	"delete":      {CursorVariant: "delete_tool_call", GliderBuiltin: "", WireField: toolCallFieldDelete},
	"Delete":      {CursorVariant: "delete_tool_call", GliderBuiltin: "", WireField: toolCallFieldDelete},
	"DeleteFile":  {CursorVariant: "delete_tool_call", GliderBuiltin: "", WireField: toolCallFieldDelete},
	"fs_delete":   {CursorVariant: "delete_tool_call", GliderBuiltin: "", WireField: toolCallFieldDelete},
	"delete_file": {CursorVariant: "delete_tool_call", GliderBuiltin: "", WireField: toolCallFieldDelete},
	// Glob / search
	"glob":        {CursorVariant: "glob_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldGlob},
	"Glob":        {CursorVariant: "glob_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldGlob},
	"file_search": {CursorVariant: "glob_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldGlob},
	"fs_search":   {CursorVariant: "glob_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldGlob},
	// Ls / list
	"ls":       {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	"Ls":       {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	"list_dir": {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	"ListDir":  {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	"fs_list":  {CursorVariant: "ls_tool_call", GliderBuiltin: "fs_list", WireField: toolCallFieldLs},
	// Web
	"web_search": {CursorVariant: "web_search_tool_call", GliderBuiltin: "web_search", WireField: toolCallFieldWebSearch},
	"WebSearch":  {CursorVariant: "web_search_tool_call", GliderBuiltin: "web_search", WireField: toolCallFieldWebSearch},
	"web_fetch":  {CursorVariant: "web_fetch_tool_call", GliderBuiltin: "web_fetch", WireField: toolCallFieldWebFetch},
	"WebFetch":   {CursorVariant: "web_fetch_tool_call", GliderBuiltin: "web_fetch", WireField: toolCallFieldWebFetch},
	"http_fetch": {CursorVariant: "fetch_tool_call", GliderBuiltin: "http_fetch", WireField: toolCallFieldFetch},
	"fetch":      {CursorVariant: "fetch_tool_call", GliderBuiltin: "http_fetch", WireField: toolCallFieldFetch},
	"Fetch":      {CursorVariant: "fetch_tool_call", GliderBuiltin: "http_fetch", WireField: toolCallFieldFetch},
	// Todos (Cursor UI chrome; no Glider builtin)
	"TodoWrite":    {CursorVariant: "update_todos_tool_call", GliderBuiltin: "", WireField: toolCallFieldUpdateTodos},
	"todo_write":   {CursorVariant: "update_todos_tool_call", GliderBuiltin: "", WireField: toolCallFieldUpdateTodos},
	"update_todos": {CursorVariant: "update_todos_tool_call", GliderBuiltin: "", WireField: toolCallFieldUpdateTodos},
	"UpdateTodos":  {CursorVariant: "update_todos_tool_call", GliderBuiltin: "", WireField: toolCallFieldUpdateTodos},
	"TodoRead":     {CursorVariant: "read_todos_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadTodos},
	"todo_read":    {CursorVariant: "read_todos_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadTodos},
	"read_todos":   {CursorVariant: "read_todos_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadTodos},
	"ReadTodos":    {CursorVariant: "read_todos_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadTodos},
	// Lints
	"ReadLints":  {CursorVariant: "read_lints_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadLints},
	"read_lints": {CursorVariant: "read_lints_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadLints},
	"readLints":  {CursorVariant: "read_lints_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadLints},
	// Semantic search
	"SemSearch":       {CursorVariant: "sem_search_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldSemSearch},
	"sem_search":      {CursorVariant: "sem_search_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldSemSearch},
	"codebase_search": {CursorVariant: "sem_search_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldSemSearch},
	"SemanticSearch":  {CursorVariant: "sem_search_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldSemSearch},
	"semantic_search": {CursorVariant: "sem_search_tool_call", GliderBuiltin: "fs_search", WireField: toolCallFieldSemSearch},
	// MCP
	"CallMcpTool":        {CursorVariant: "mcp_tool_call", GliderBuiltin: "", WireField: toolCallFieldMcp},
	"call_mcp_tool":      {CursorVariant: "mcp_tool_call", GliderBuiltin: "", WireField: toolCallFieldMcp},
	"mcp":                {CursorVariant: "mcp_tool_call", GliderBuiltin: "", WireField: toolCallFieldMcp},
	"Mcp":                {CursorVariant: "mcp_tool_call", GliderBuiltin: "", WireField: toolCallFieldMcp},
	"ListMcpResources":   {CursorVariant: "list_mcp_resources_tool_call", GliderBuiltin: "", WireField: toolCallFieldListMcpResources},
	"list_mcp_resources": {CursorVariant: "list_mcp_resources_tool_call", GliderBuiltin: "", WireField: toolCallFieldListMcpResources},
	"ReadMcpResource":    {CursorVariant: "read_mcp_resource_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadMcpResource},
	"read_mcp_resource":  {CursorVariant: "read_mcp_resource_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadMcpResource},
	"FetchMcpResource":   {CursorVariant: "read_mcp_resource_tool_call", GliderBuiltin: "", WireField: toolCallFieldReadMcpResource},
	// Plan / task / mode (Cursor UI)
	"CreatePlan":       {CursorVariant: "create_plan_tool_call", GliderBuiltin: "", WireField: toolCallFieldCreatePlan},
	"create_plan":      {CursorVariant: "create_plan_tool_call", GliderBuiltin: "", WireField: toolCallFieldCreatePlan},
	"Task":             {CursorVariant: "task_tool_call", GliderBuiltin: "", WireField: toolCallFieldTask},
	"task":             {CursorVariant: "task_tool_call", GliderBuiltin: "", WireField: toolCallFieldTask},
	"SwitchMode":       {CursorVariant: "switch_mode_tool_call", GliderBuiltin: "", WireField: toolCallFieldSwitchMode},
	"switch_mode":      {CursorVariant: "switch_mode_tool_call", GliderBuiltin: "", WireField: toolCallFieldSwitchMode},
	"AskQuestion":      {CursorVariant: "ask_question_tool_call", GliderBuiltin: "", WireField: toolCallFieldAskQuestion},
	"ask_question":     {CursorVariant: "ask_question_tool_call", GliderBuiltin: "", WireField: toolCallFieldAskQuestion},
	"ApplyAgentDiff":   {CursorVariant: "apply_agent_diff_tool_call", GliderBuiltin: "", WireField: toolCallFieldApplyAgentDiff},
	"apply_agent_diff": {CursorVariant: "apply_agent_diff_tool_call", GliderBuiltin: "", WireField: toolCallFieldApplyAgentDiff},
	// Exa (Cursor hosted search/fetch)
	"ExaSearch":  {CursorVariant: "exa_search_tool_call", GliderBuiltin: "web_search", WireField: toolCallFieldExaSearch},
	"exa_search": {CursorVariant: "exa_search_tool_call", GliderBuiltin: "web_search", WireField: toolCallFieldExaSearch},
	"ExaFetch":   {CursorVariant: "exa_fetch_tool_call", GliderBuiltin: "web_fetch", WireField: toolCallFieldExaFetch},
	"exa_fetch":  {CursorVariant: "exa_fetch_tool_call", GliderBuiltin: "web_fetch", WireField: toolCallFieldExaFetch},
	// Image / shell stdin / reflect
	"GenerateImage":     {CursorVariant: "generate_image_tool_call", GliderBuiltin: "", WireField: toolCallFieldGenerateImage},
	"generate_image":    {CursorVariant: "generate_image_tool_call", GliderBuiltin: "", WireField: toolCallFieldGenerateImage},
	"WriteShellStdin":   {CursorVariant: "write_shell_stdin_tool_call", GliderBuiltin: "", WireField: toolCallFieldWriteShellStdin},
	"write_shell_stdin": {CursorVariant: "write_shell_stdin_tool_call", GliderBuiltin: "", WireField: toolCallFieldWriteShellStdin},
	"Reflect":           {CursorVariant: "reflect_tool_call", GliderBuiltin: "", WireField: toolCallFieldReflect},
	"reflect":           {CursorVariant: "reflect_tool_call", GliderBuiltin: "", WireField: toolCallFieldReflect},
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

// MappedToolCallInventory returns a stable summary of wire variants currently mapped.
func MappedToolCallInventory() []ToolNameMapping {
	seen := map[int]ToolNameMapping{}
	for _, m := range CommonToolNameMappings {
		if _, ok := seen[m.WireField]; !ok {
			seen[m.WireField] = m
		}
	}
	order := []int{
		toolCallFieldShell, toolCallFieldDelete, toolCallFieldGlob, toolCallFieldGrep,
		toolCallFieldRead, toolCallFieldUpdateTodos, toolCallFieldReadTodos, toolCallFieldEdit,
		toolCallFieldLs, toolCallFieldReadLints, toolCallFieldMcp, toolCallFieldSemSearch,
		toolCallFieldCreatePlan, toolCallFieldWebSearch, toolCallFieldTask,
		toolCallFieldListMcpResources, toolCallFieldReadMcpResource, toolCallFieldApplyAgentDiff,
		toolCallFieldAskQuestion, toolCallFieldFetch, toolCallFieldSwitchMode,
		toolCallFieldExaSearch, toolCallFieldExaFetch, toolCallFieldGenerateImage,
		toolCallFieldWriteShellStdin, toolCallFieldReflect, toolCallFieldWebFetch,
	}
	out := make([]ToolNameMapping, 0, len(order))
	for _, f := range order {
		if m, ok := seen[f]; ok {
			out = append(out, m)
		}
	}
	return out
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
	case toolCallFieldUpdateTodos:
		// UpdateTodosArgs: repeated TodoItem todos=1, bool merge=2
		var out []byte
		for _, item := range encodeTodoItems(argsJSON) {
			out = append(out, protoBytesField(1, item)...)
		}
		if mergeTrue(fields["merge"]) {
			out = append(out, protoVarintField(2, 1)...)
		}
		return out
	case toolCallFieldReadTodos:
		// ReadTodosArgs: filters optional — empty args is valid
		return nil
	case toolCallFieldReadLints:
		// ReadLintsToolArgs: repeated string paths=1
		var out []byte
		for _, p := range stringListFromArgs(argsJSON, fields, "paths", "path", "files") {
			out = append(out, protoBytesField(1, []byte(p))...)
		}
		return out
	case toolCallFieldMcp:
		// McpArgs: name=1, tool_call_id=3, provider_identifier=4, tool_name=5
		// (map args=2 omitted — protobuf.Value map not reconstructed from JSON here)
		name := str("name", "tool", "tool_name")
		toolName := str("tool_name", "tool")
		provider := str("provider_identifier", "server", "server_name", "provider")
		out := protoBytesField(1, []byte(name))
		id := callID
		if id == "" {
			id = str("tool_call_id", "call_id")
		}
		if id != "" {
			out = append(out, protoBytesField(3, []byte(id))...)
		}
		if provider != "" {
			out = append(out, protoBytesField(4, []byte(provider))...)
		}
		if toolName != "" && toolName != name {
			out = append(out, protoBytesField(5, []byte(toolName))...)
		} else if toolName != "" {
			out = append(out, protoBytesField(5, []byte(toolName))...)
		}
		return out
	case toolCallFieldSemSearch:
		// SemSearchToolArgs: query=1, repeated target_directories=2, explanation=3
		out := protoBytesField(1, []byte(str("query", "search_term", "q", "input")))
		for _, d := range stringListFromArgs(argsJSON, fields, "target_directories", "target_directory", "path", "paths") {
			out = append(out, protoBytesField(2, []byte(d))...)
		}
		if e := str("explanation"); e != "" {
			out = append(out, protoBytesField(3, []byte(e))...)
		}
		return out
	case toolCallFieldCreatePlan:
		// CreatePlanArgs: plan=1, overview=3, name=4
		var out []byte
		if p := str("plan", "content", "text"); p != "" {
			out = append(out, protoBytesField(1, []byte(p))...)
		}
		if o := str("overview", "summary"); o != "" {
			out = append(out, protoBytesField(3, []byte(o))...)
		}
		if n := str("name", "title"); n != "" {
			out = append(out, protoBytesField(4, []byte(n))...)
		}
		return out
	case toolCallFieldTask:
		// TaskArgs: description=1, prompt=2
		var out []byte
		if d := str("description", "title", "name"); d != "" {
			out = append(out, protoBytesField(1, []byte(d))...)
		}
		if p := str("prompt", "input", "task"); p != "" {
			out = append(out, protoBytesField(2, []byte(p))...)
		}
		return out
	case toolCallFieldListMcpResources:
		// ListMcpResourcesExecArgs: optional server=1
		if s := str("server", "server_name", "provider"); s != "" {
			return protoBytesField(1, []byte(s))
		}
		return nil
	case toolCallFieldReadMcpResource:
		// ReadMcpResourceExecArgs: server=1, uri=2, download_path=3
		var out []byte
		if s := str("server", "server_name", "provider"); s != "" {
			out = append(out, protoBytesField(1, []byte(s))...)
		}
		out = append(out, protoBytesField(2, []byte(str("uri", "url", "href")))...)
		if d := str("download_path", "path"); d != "" {
			out = append(out, protoBytesField(3, []byte(d))...)
		}
		return out
	case toolCallFieldApplyAgentDiff:
		return protoBytesField(1, []byte(str("agent_id", "id")))
	case toolCallFieldAskQuestion:
		// AskQuestionArgs: title=1 (questions nested — omit unless simple)
		title := str("title", "prompt", "question")
		return protoBytesField(1, []byte(title))
	case toolCallFieldSwitchMode:
		// SwitchModeArgs: target_mode_id=1, explanation=2, tool_call_id=3
		out := protoBytesField(1, []byte(str("target_mode_id", "mode", "mode_id")))
		if e := str("explanation"); e != "" {
			out = append(out, protoBytesField(2, []byte(e))...)
		}
		id := callID
		if id == "" {
			id = str("tool_call_id")
		}
		if id != "" {
			out = append(out, protoBytesField(3, []byte(id))...)
		}
		return out
	case toolCallFieldExaSearch:
		// ExaSearchArgs: query=1, type=2, num_results=3, tool_call_id=4
		out := protoBytesField(1, []byte(str("query", "search_term", "q", "input")))
		if typ := str("type"); typ != "" {
			out = append(out, protoBytesField(2, []byte(typ))...)
		}
		id := callID
		if id == "" {
			id = str("tool_call_id")
		}
		if id != "" {
			out = append(out, protoBytesField(4, []byte(id))...)
		}
		return out
	case toolCallFieldExaFetch:
		// ExaFetchArgs: repeated ids=1, tool_call_id=2
		var out []byte
		for _, id := range stringListFromArgs(argsJSON, fields, "ids", "id") {
			out = append(out, protoBytesField(1, []byte(id))...)
		}
		cid := callID
		if cid == "" {
			cid = str("tool_call_id")
		}
		if cid != "" {
			out = append(out, protoBytesField(2, []byte(cid))...)
		}
		return out
	case toolCallFieldGenerateImage:
		// GenerateImageArgs: description=1, file_path=2
		out := protoBytesField(1, []byte(str("description", "prompt", "input")))
		if p := str("file_path", "path", "filename"); p != "" {
			out = append(out, protoBytesField(2, []byte(p))...)
		}
		return out
	case toolCallFieldWriteShellStdin:
		// WriteShellStdinArgs: shell_id=1 (uint32), chars=2
		var out []byte
		if id := str("shell_id", "id"); id != "" {
			if n, ok := parseUint(id); ok {
				out = append(out, protoVarintField(1, n)...)
			}
		}
		out = append(out, protoBytesField(2, []byte(str("chars", "input", "text", "data")))...)
		return out
	case toolCallFieldReflect:
		// ReflectArgs: scalar strings 1..5, tool_call_id=6
		var out []byte
		for i, key := range []string{
			"unexpected_action_outcomes", "relevant_instructions", "scenario_analysis",
			"critical_synthesis", "next_steps",
		} {
			if v := str(key); v != "" {
				out = append(out, protoBytesField(i+1, []byte(v))...)
			}
		}
		id := callID
		if id == "" {
			id = str("tool_call_id")
		}
		if id != "" {
			out = append(out, protoBytesField(6, []byte(id))...)
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

func mergeTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

func parseUint(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}

func stringListFromArgs(argsJSON string, flat map[string]string, keys ...string) []string {
	argsJSON = strings.TrimSpace(argsJSON)
	if argsJSON != "" && argsJSON != "{}" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(argsJSON), &raw); err == nil {
			for _, k := range keys {
				v, ok := raw[k]
				if !ok {
					continue
				}
				switch t := v.(type) {
				case []any:
					out := make([]string, 0, len(t))
					for _, el := range t {
						if s, ok := el.(string); ok && s != "" {
							out = append(out, s)
						}
					}
					if len(out) > 0 {
						return out
					}
				case string:
					if t != "" {
						return []string{t}
					}
				}
			}
		}
	}
	for _, k := range keys {
		if v := strings.TrimSpace(flat[k]); v != "" {
			// JSON array stringified into flat map — try parse
			if strings.HasPrefix(v, "[") {
				var arr []string
				if json.Unmarshal([]byte(v), &arr) == nil && len(arr) > 0 {
					return arr
				}
			}
			return []string{v}
		}
	}
	return nil
}

func encodeTodoItems(argsJSON string) [][]byte {
	argsJSON = strings.TrimSpace(argsJSON)
	if argsJSON == "" || argsJSON == "{}" {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil
	}
	arr, ok := raw["todos"].([]any)
	if !ok {
		arr, _ = raw["items"].([]any)
	}
	if len(arr) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(arr))
	for _, el := range arr {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		var item []byte
		if id, _ := m["id"].(string); id != "" {
			item = append(item, protoBytesField(1, []byte(id))...)
		}
		content, _ := m["content"].(string)
		if content == "" {
			content, _ = m["text"].(string)
		}
		if content != "" {
			item = append(item, protoBytesField(2, []byte(content))...)
		}
		if st := todoStatusWire(m["status"]); st > 0 {
			item = append(item, protoVarintField(3, st)...)
		}
		if len(item) > 0 {
			out = append(out, item)
		}
	}
	return out
}

func todoStatusWire(v any) uint64 {
	switch t := v.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "pending":
			return 1
		case "in_progress", "in-progress", "inprogress":
			return 2
		case "completed", "done":
			return 3
		case "cancelled", "canceled":
			return 4
		}
	case float64:
		if t >= 1 && t <= 4 {
			return uint64(t)
		}
	}
	return 0
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
