package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutForRunEnsureAndScopeRel(t *testing.T) {
	root := t.TempDir()
	layout := LayoutForRun(root, "hoop-abc")
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(layout.WorkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(layout.OutDir); err != nil {
		t.Fatal(err)
	}
	if layout.WorkRel != "runs/hoop-abc/work" {
		t.Fatalf("work_rel=%q", layout.WorkRel)
	}
	if layout.OutRel != "runs/hoop-abc/out" {
		t.Fatalf("out_rel=%q", layout.OutRel)
	}

	bare := layout.ScopeRel("notes.txt", RelWork)
	if bare != "runs/hoop-abc/work/notes.txt" {
		t.Fatalf("bare scope=%q", bare)
	}
	out := layout.ScopeRel("report.md", RelOut)
	if out != "runs/hoop-abc/out/report.md" {
		t.Fatalf("out scope=%q", out)
	}
	// Already scoped paths stay put.
	if got := layout.ScopeRel(layout.WorkRel+"/x", RelWork); got != layout.WorkRel+"/x" {
		t.Fatalf("idempotent work=%q", got)
	}
	hint := layout.PromptHint()
	if !strings.Contains(hint, "work") || !strings.Contains(hint, "out") {
		t.Fatalf("hint=%q", hint)
	}
}

func TestEnsureRunLayoutAndArtifactWrite(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(Options{Workspace: root})
	layout, err := r.EnsureRunLayout("run-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRunLayout(context.Background(), layout)

	res, err := r.Invoke(ctx, Ref{Name: "fs_write", Kind: KindBuiltin}, "scratch.txt\nhello")
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	workFile := filepath.Join(layout.WorkDir, "scratch.txt")
	if b, err := os.ReadFile(workFile); err != nil || string(b) != "hello" {
		t.Fatalf("work file=%v err=%v", string(b), err)
	}

	res, err = r.Invoke(ctx, Ref{Name: "artifact_write", Kind: KindBuiltin}, "")
	// Use JSON args via Call path — Invoke passes nil args; exercise text form.
	res, err = r.Invoke(ctx, Ref{Name: "artifact_write", Kind: KindBuiltin}, "kind=out final.md\ndeliverable")
	if err != nil || !res.OK {
		t.Fatalf("artifact_write %+v err=%v", res, err)
	}
	outFile := filepath.Join(layout.OutDir, "final.md")
	if b, err := os.ReadFile(outFile); err != nil || string(b) != "deliverable" {
		t.Fatalf("out file=%q err=%v", string(b), err)
	}
}

func TestBindExistingAndPathEscape(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "projects", "demo")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(Options{Workspace: root})
	layout, err := r.BindExisting("bind-1", "projects/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if layout.Mode != "existing" {
		t.Fatalf("mode=%q", layout.Mode)
	}
	if layout.WorkDir != existing {
		t.Fatalf("work=%q want %q", layout.WorkDir, existing)
	}
	wantOut := filepath.Join(existing, "out")
	if layout.OutDir != wantOut {
		t.Fatalf("out=%q want %q", layout.OutDir, wantOut)
	}
	ctx := WithRunLayout(context.Background(), layout)
	res, err := r.Invoke(ctx, Ref{Name: "fs_write", Kind: KindBuiltin}, "a.txt\nx")
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(existing, "a.txt")); err != nil {
		t.Fatal(err)
	}

	_, err = r.BindExisting("bad", "../outside", "")
	if err == nil {
		t.Fatal("expected path escape rejection")
	}
	_, err = LayoutExisting(root, "bad", filepath.Join(root, "..", "escape"), "")
	if err == nil {
		t.Fatal("expected absolute escape rejection")
	}
}

func TestGitCloneStillScopesUnderWork(t *testing.T) {
	root := t.TempDir()
	r := NewRegistry(Options{Workspace: root})
	layout, err := r.EnsureRunLayout("clone-run")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRunLayout(context.Background(), layout)
	// Without network: path resolution for dest must land under work (escape check).
	dest, err := resolveToolPath(ctx, root, "audit-target", RelWork)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dest, layout.WorkDir) {
		t.Fatalf("clone dest %q not under work %q", dest, layout.WorkDir)
	}
	// Existing-mode bind must reject .. escape (unlike bare safeJoin clamp).
	if _, err := LayoutExisting(root, "bad", "../../etc", ""); err == nil {
		t.Fatal("expected LayoutExisting escape reject")
	}
}
