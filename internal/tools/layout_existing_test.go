package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayoutExistingAndBind(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(Options{Workspace: root})
	lay, err := reg.BindExisting("turn1", "projects/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if lay.RelWork != "projects/demo" || lay.RelOut != "projects/demo/out" {
		t.Fatalf("%+v", lay)
	}
	if got := reg.ScopeRel("a.txt"); got != "projects/demo/a.txt" {
		t.Fatalf("ScopeRel=%q", got)
	}
	if _, err := LayoutExisting(root, "t", "..", ""); err == nil {
		t.Fatal("expected escape reject")
	}
}
