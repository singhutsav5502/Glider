package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glider-ai/glider/internal/mcp"
)

func TestStandardBuiltins(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc Hello() {}\n"), 0o644)
	r := NewRegistry(Options{Workspace: dir, AllowShell: false})
	names := map[string]bool{}
	for _, s := range r.Catalog(context.Background()) {
		names[s.Name] = true
	}
	for _, n := range []string{"fs_read", "code_grep", "git_status", "datetime", "calculator", "context_query"} {
		if !names[n] {
			t.Fatalf("missing %s in catalog", n)
		}
	}
	res, err := r.Invoke(context.Background(), Ref{Name: "fs_list", Kind: KindBuiltin}, ".")
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	res, err = r.Invoke(context.Background(), Ref{Name: "code_grep", Kind: KindBuiltin}, "Hello")
	if err != nil || !res.OK || res.Output == "" {
		t.Fatalf("%+v err=%v", res, err)
	}
	res, err = r.Invoke(context.Background(), Ref{Name: "calculator", Kind: KindBuiltin}, "3*7")
	if err != nil || res.Output != "21" {
		t.Fatalf("%+v err=%v", res, err)
	}
	res, err = r.Invoke(context.Background(), Ref{Name: "datetime", Kind: KindBuiltin}, "")
	if err != nil || !res.OK {
		t.Fatalf("%+v", res)
	}
}

func TestMCPViaRegistry(t *testing.T) {
	mgr := mcp.NewManager()
	_, err := mgr.Connect(context.Background(), mcp.DefaultGitHubConfig())
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(Options{MCP: mgr})
	res, err := r.Invoke(context.Background(), Ref{Name: "get_me", Kind: KindMCP, Server: "github"}, "")
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	tools, err := mgr.ListTools(context.Background(), "github")
	if err != nil || len(tools) == 0 {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
}
