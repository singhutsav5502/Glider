package dashboard_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/dashboard"
	"github.com/glider-ai/glider/internal/loop"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/tools"
)

func TestWorkspaceAPIBoundToRun(t *testing.T) {
	dir := t.TempDir()
	wsRoot := filepath.Join(dir, "workspace")
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("server:\n  proxy_port: 8080\n  dashboard_port: 8081\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := config.NewProvider(loaded, path)
	srv := dashboard.New(":0", metrics.NewBus(), &dashboard.FileConfigStore{Provider: p, Path: path},
		&dashboard.RegistryModelController{Registry: backend.NewRegistry()})
	mgr := loop.NewManager(loop.NewStore(filepath.Join(dir, "loops")), nil, nil, loop.RunnerConfig{})
	mgr.Tools = tools.NewRegistry(tools.Options{Workspace: wsRoot})
	srv.Loops = mgr

	st, err := mgr.Create(loop.LoopSpec{ID: "ws-api", Goal: "x", Prompt: "x", MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	layout, err := mgr.Tools.EnsureRunLayout(st.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	st.Workspace.FromToolsLayout(layout)
	_ = mgr.Store.Save(st)
	_ = os.WriteFile(filepath.Join(layout.WorkDir, "a.txt"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(layout.OutDir, "b.txt"), []byte("2"), 0o644)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/workspace?run=ws-api")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["work_dir"] == "" || got["out_dir"] == "" {
		t.Fatalf("missing dirs: %s", body)
	}
	if got["work_rel"] != "runs/ws-api/work" {
		t.Fatalf("work_rel=%v", got["work_rel"])
	}
}
