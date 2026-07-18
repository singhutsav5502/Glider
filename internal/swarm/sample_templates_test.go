package swarm_test

import (
  "os"
  "path/filepath"
  "testing"
  "github.com/glider-ai/glider/internal/swarm"
)

func TestLoadSampleSwarms(t *testing.T) {
  root := filepath.Clean(filepath.Join("..", ".."))
  dir := t.TempDir()
  store := swarm.NewTemplateStore(dir)
  matches, err := filepath.Glob(filepath.Join(root, "samples", "swarms", "*.yaml"))
  if err != nil { t.Fatal(err) }
  if len(matches) == 0 { t.Fatal("no samples") }
  for _, f := range matches {
    b, err := os.ReadFile(f)
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o644); err != nil {
      t.Fatal(err)
    }
  }
  list, err := store.List()
  if err != nil { t.Fatal(err) }
  for _, tpl := range list {
    t.Logf("id=%s roles=%v max=%d prompt=%d", tpl.ID, tpl.Roles, tpl.MaxWorkers, len(tpl.Prompt))
    if len(tpl.Roles) < 2 {
      t.Errorf("%s: expected roles, got %v", tpl.ID, tpl.Roles)
    }
    if len(tpl.Prompt) < 20 {
      t.Errorf("%s: empty prompt", tpl.ID)
    }
  }
}
