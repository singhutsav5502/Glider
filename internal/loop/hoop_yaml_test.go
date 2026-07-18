package loop_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/glider-ai/glider/internal/loop"
)

func TestReadSampleHoopYAML(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	// internal/loop → repo root
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(root, "samples", "hoops", "hello-critic.yaml")
	spec, err := loop.ReadHoopYAMLFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != "hello-critic" {
		t.Fatalf("id=%q", spec.ID)
	}
	if spec.Route != loop.RouteLocal {
		t.Fatalf("route=%q", spec.Route)
	}
	if len(spec.Stages) < 3 {
		t.Fatalf("stages=%d", len(spec.Stages))
	}
	if spec.MaxIterations != 2 {
		t.Fatalf("max_iterations=%d", spec.MaxIterations)
	}
}
