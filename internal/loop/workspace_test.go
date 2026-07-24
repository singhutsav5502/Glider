package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/tools"
)

func TestWorkspaceStageExistingModeAndEscape(t *testing.T) {
	root := t.TempDir()
	demo := filepath.Join(root, "projects", "demo")
	if err := os.MkdirAll(demo, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(tools.Options{Workspace: root})
	mgr := NewManager(NewStore(t.TempDir()), nil, nil, RunnerConfig{})
	mgr.Tools = reg

	st := CreateState(LoopSpec{ID: "ws-hoop", Goal: "bind", Prompt: "bind"})
	if err := mgr.bindDefaultWorkspace(st); err != nil {
		t.Fatal(err)
	}
	if st.Workspace.WorkRel != "runs/ws-hoop/work" {
		t.Fatalf("default work_rel=%q", st.Workspace.WorkRel)
	}

	layout, err := mgr.applyWorkspaceStage(st, StageSpec{
		Kind:          StageWorkspace,
		WorkspaceMode: WorkspaceModeExisting,
		WorkspacePath: "projects/demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Mode != "existing" || layout.WorkDir != demo {
		t.Fatalf("layout=%+v", layout)
	}
	if st.Workspace.OutRel != "projects/demo/out" {
		t.Fatalf("out_rel=%q", st.Workspace.OutRel)
	}

	ctx := mgr.toolContext(context.Background(), st)
	res, err := reg.Invoke(ctx, tools.Ref{Name: "artifact_write", Kind: tools.KindBuiltin}, "kind=out note.txt\nok")
	if err != nil || !res.OK {
		t.Fatalf("%+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(demo, "out", "note.txt")); err != nil {
		t.Fatal(err)
	}

	_, err = mgr.applyWorkspaceStage(st, StageSpec{
		Kind:          StageWorkspace,
		WorkspaceMode: WorkspaceModeExisting,
		WorkspacePath: "../escape",
	})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("want escape error, got %v", err)
	}
}

func TestStageWorkspaceNormalize(t *testing.T) {
	s := StageSpec{Kind: StageWorkspace, WorkspaceMode: WorkspaceModeExisting}
	if err := s.Normalize(); err == nil {
		t.Fatal("expected workspace_path required")
	}
	s.WorkspacePath = "projects/x"
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	kinds := Catalog().Kinds
	found := false
	for _, k := range kinds {
		if k == StageWorkspace {
			found = true
		}
	}
	if !found {
		t.Fatalf("catalog missing workspace: %v", kinds)
	}
}
