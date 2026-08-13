package cursorrpc_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/cursorrpc"
)

func TestMapToolNameToGliderBuiltin(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"read_file", "fs_read", true},
		{"Read", "fs_read", true},
		{"grep", "code_grep", true},
		{"fs_write", "fs_write", true},
		{"Shell", "shell_exec", true},
		{"list_dir", "fs_list", true},
		{"Glob", "fs_search", true},
		{"web_search", "web_search", true},
		{"codebase_search", "fs_search", true},
		{"ExaSearch", "web_search", true},
		{"Delete", "", false}, // mapped to Cursor wire, no Glider builtin
		{"TodoWrite", "", false},
		{"CallMcpTool", "", false},
		{"unknown_tool_xyz", "", false},
	}
	for _, c := range cases {
		got, ok := cursorrpc.MapToolNameToGliderBuiltin(c.in)
		if ok != c.ok || got != c.want {
			t.Fatalf("%q: got (%q,%v) want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestEncodeMappedToolCallWireVariants(t *testing.T) {
	cases := []struct {
		name, args string
		wantField  int
	}{
		{"read_file", `{"path":"main.go"}`, 8},
		{"grep", `{"pattern":"TODO","path":"."}`, 5},
		{"Write", `{"path":"a.go","contents":"x"}`, 12},
		{"shell_exec", `{"command":"go test"}`, 1},
		{"Delete", `{"path":"tmp"}`, 3},
		{"Glob", `{"glob_pattern":"**/*.go"}`, 4},
		{"ls", `{"path":"."}`, 13},
		{"web_search", `{"query":"glider"}`, 18},
		{"web_fetch", `{"url":"https://example.com"}`, 37},
		{"http_fetch", `{"url":"https://example.com"}`, 24},
		{"TodoWrite", `{"merge":true,"todos":[{"id":"1","content":"do","status":"pending"}]}`, 9},
		{"TodoRead", `{}`, 10},
		{"ReadLints", `{"paths":["a.go","b.go"]}`, 14},
		{"CallMcpTool", `{"name":"get_me","server":"github"}`, 15},
		{"codebase_search", `{"query":"auth","target_directories":["internal/"]}`, 16},
		{"CreatePlan", `{"name":"ship","plan":"steps"}`, 17},
		{"Task", `{"description":"explore","prompt":"find bugs"}`, 19},
		{"ListMcpResources", `{"server":"github"}`, 20},
		{"ReadMcpResource", `{"server":"github","uri":"res://x"}`, 21},
		{"ApplyAgentDiff", `{"agent_id":"a1"}`, 22},
		{"AskQuestion", `{"title":"Pick one"}`, 23},
		{"SwitchMode", `{"target_mode_id":"plan"}`, 25},
		{"ExaSearch", `{"query":"mcp"}`, 26},
		{"ExaFetch", `{"ids":["id1"]}`, 27},
		{"GenerateImage", `{"description":"logo"}`, 28},
		{"WriteShellStdin", `{"shell_id":"3","chars":"y\n"}`, 31},
		{"Reflect", `{"next_steps":"retry"}`, 32},
		{"mystery", `{}`, 34},
	}
	for _, c := range cases {
		wire := cursorrpc.EncodeMappedToolCallWire(c.name, c.args, "call_1")
		got := cursorrpc.ToolCallWireVariant(wire)
		if got != c.wantField {
			t.Fatalf("%s: field=%d want=%d hex=%x", c.name, got, c.wantField, wire)
		}
	}
}

func TestEncodeMappedToolCallEmbedsPath(t *testing.T) {
	wire := cursorrpc.EncodeMappedToolCallWire("read", `{"path":"internal/foo.go"}`, "")
	if cursorrpc.ToolCallWireVariant(wire) != 8 {
		t.Fatalf("want read field 8, got %d", cursorrpc.ToolCallWireVariant(wire))
	}
	if !strings.Contains(string(wire), "internal/foo.go") {
		t.Fatalf("path missing in wire: %x", wire)
	}
}

func TestEncodeSemSearchAndTodosEmbedScalars(t *testing.T) {
	sem := cursorrpc.EncodeMappedToolCallWire("SemSearch", `{"query":"session retry"}`, "")
	if !strings.Contains(string(sem), "session retry") {
		t.Fatalf("sem query missing: %x", sem)
	}
	todo := cursorrpc.EncodeMappedToolCallWire("TodoWrite", `{"todos":[{"id":"t1","content":"ship map"}]}`, "")
	if !strings.Contains(string(todo), "ship map") || !strings.Contains(string(todo), "t1") {
		t.Fatalf("todo fields missing: %x", todo)
	}
}

func TestLookupToolNameMappingCaseFold(t *testing.T) {
	m, ok := cursorrpc.LookupToolNameMapping("READ_FILE")
	if !ok || m.GliderBuiltin != "fs_read" {
		t.Fatalf("%+v ok=%v", m, ok)
	}
	m2, ok := cursorrpc.LookupToolNameMapping("CODEBASE_SEARCH")
	if !ok || m2.WireField != 16 {
		t.Fatalf("%+v ok=%v", m2, ok)
	}
}

func TestMappedToolCallInventoryCoversExtendedSet(t *testing.T) {
	inv := cursorrpc.MappedToolCallInventory()
	want := map[string]bool{
		"read_tool_call": true, "grep_tool_call": true, "update_todos_tool_call": true,
		"read_lints_tool_call": true, "mcp_tool_call": true, "sem_search_tool_call": true,
		"task_tool_call": true, "switch_mode_tool_call": true, "exa_search_tool_call": true,
	}
	got := map[string]bool{}
	for _, m := range inv {
		got[m.CursorVariant] = true
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("inventory missing %s; got %#v", k, got)
		}
	}
}
