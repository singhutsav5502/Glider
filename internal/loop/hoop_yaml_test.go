package loop_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/loop"
	"gopkg.in/yaml.v3"
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

func TestParallelModeYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	spec := loop.LoopSpec{
		ID: "rt-swarm", Name: "rt", Goal: "g", Route: loop.RouteLocal,
		MaxIterations: 1, Autonomy: loop.AutonomyL2,
		Stages: []loop.StageSpec{
			{Kind: loop.StageContext, ID: "context_seed"},
			{
				Kind: loop.StageActor, ID: "actor", Parallel: 2,
				ParallelMode: loop.ParallelModeSwarm,
				Roles:        []string{"exec", "research"},
				Swarm: &loop.StageSwarmSpec{
					TemplateID:  "tpl",
					Waves:       1,
					WeavePolicy: "role_weighted",
					PreferLocal: true,
					Models:      []string{"local-a", "local-b"},
				},
			},
		},
	}
	if err := loop.WriteHoopYAML(dir, spec); err != nil {
		t.Fatal(err)
	}
	got, err := loop.ReadHoopYAMLFile(filepath.Join(dir, "rt-swarm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var actor *loop.StageSpec
	var sawCtx bool
	for i := range got.Stages {
		if got.Stages[i].Kind == loop.StageContext {
			sawCtx = true
		}
		if got.Stages[i].ID == "actor" {
			actor = &got.Stages[i]
		}
	}
	if !sawCtx {
		t.Fatal("missing context stage after round-trip")
	}
	if actor == nil {
		t.Fatal("missing actor")
	}
	if actor.ParallelMode != loop.ParallelModeSwarm || actor.Parallel != 2 {
		t.Fatalf("actor=%+v", actor)
	}
	if actor.Swarm == nil || actor.Swarm.TemplateID != "tpl" {
		t.Fatalf("swarm=%+v", actor.Swarm)
	}
	if actor.Swarm.WeavePolicy != "role_weighted" || !actor.Swarm.PreferLocal {
		t.Fatalf("swarm knobs=%+v", actor.Swarm)
	}
	if len(actor.Swarm.Models) != 2 || actor.Swarm.Models[0] != "local-a" {
		t.Fatalf("swarm models=%v", actor.Swarm.Models)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "rt-swarm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	_ = doc
	if !strings.Contains(string(raw), "parallel_mode: swarm") {
		t.Fatalf("yaml missing parallel_mode:\n%s", raw)
	}
}

func TestReadParallelSwarmSample(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	spec, err := loop.ReadHoopYAMLFile(filepath.Join(root, "samples", "hoops", "parallel-swarm-mode.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range spec.Stages {
		if s.ID == "swarm_actors" {
			found = true
			if s.ParallelMode != loop.ParallelModeSwarm || s.Parallel != 2 {
				t.Fatalf("%+v", s)
			}
		}
	}
	if !found {
		t.Fatal("swarm_actors stage missing")
	}
}
