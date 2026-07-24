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
		{"Delete", "", false}, // mapped to Cursor wire, no Glider builtin
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

func TestLookupToolNameMappingCaseFold(t *testing.T) {
	m, ok := cursorrpc.LookupToolNameMapping("READ_FILE")
	if !ok || m.GliderBuiltin != "fs_read" {
		t.Fatalf("%+v ok=%v", m, ok)
	}
}
