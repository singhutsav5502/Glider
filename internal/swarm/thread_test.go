package swarm_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/swarm"
)

func TestThreadStorePersist(t *testing.T) {
	dir := t.TempDir()
	ts := swarm.NewThreadStore(dir)
	wave := swarm.WaveRecord{
		Index:  0,
		Prompt: "goal",
		Merged: contextkit.Episode{Summary: "wave0", ID: "m0"},
		Results: []swarm.ResultView{
			{WorkerID: "a", Summary: "ok"},
		},
	}
	st, err := ts.AppendWave("thr-1", "turn-1", "do thing", wave)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Waves) != 1 {
		t.Fatalf("waves=%d", len(st.Waves))
	}
	loaded, err := ts.Load("thr-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal != "do thing" || loaded.TurnID != "turn-1" {
		t.Fatalf("%+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(dir, "thr-1.json")); err != nil {
		t.Fatal(err)
	}
	_ = ts.SetMerged("thr-1", contextkit.Episode{Summary: "woven"}, "summary")
	loaded2, _ := ts.Load("thr-1")
	if loaded2.MergedSummary != "summary" {
		t.Fatalf("%+v", loaded2)
	}
}

func TestWeaveWaves(t *testing.T) {
	ep := swarm.WeaveWaves(
		[]contextkit.Episode{
			{Summary: "research findings", Tokens: 10, Role: "research"},
			{Summary: "exec patch applied", Tokens: 20, Role: "exec"},
		},
		[][]swarm.Result{
			{{WorkerID: "r1", Episode: contextkit.Episode{Summary: "r1"}}},
			{{WorkerID: "e1", Episode: contextkit.Episode{Summary: "e1", Tokens: 5}}},
		},
	)
	if ep.Reason != "weave" {
		t.Fatalf("reason=%s", ep.Reason)
	}
	if !strings.Contains(ep.Summary, "wave-0") || !strings.Contains(ep.Summary, "wave-1") {
		t.Fatalf("summary=%s", ep.Summary)
	}
}

type memGraph struct {
	q    string
	outs []string
	n    int
}

func (m *memGraph) Query(turnID, q string, limit int) string { return m.q }
func (m *memGraph) WaveOutputs(turnID string, waveIndex int, limit int) []string {
	return m.outs
}
func (m *memGraph) RecordThreadWave(turnID, threadID string, waveIndex int, mergedID, mergedSummary string, workers []swarm.WaveWorkerOut) {
	m.n++
	m.outs = append(m.outs, mergedSummary)
}
func (m *memGraph) RecordEpisodeFact(turnID, episodeID, label, summary string) {}

func TestRunWavesSeedsPrior(t *testing.T) {
	dir := t.TempDir()
	g := &memGraph{}
	var prompts []string
	r := &swarm.Runner{
		WorkerFn: func(ctx context.Context, role swarm.Role, model, prompt string) (contextkit.Episode, error) {
			prompts = append(prompts, prompt)
			return contextkit.Episode{Summary: "out-" + string(role), Tokens: 4, Role: string(role)}, nil
		},
		GraphCtx: g,
		Threads:  swarm.NewThreadStore(dir),
		Opts:     swarm.Options{MaxWorkers: 2},
	}
	r.SetEnabled(true)
	resp, err := r.RunWaves(context.Background(), swarm.RunWavesRequest{
		RunRequest: swarm.RunRequest{
			Prompt:     "build feature",
			Roles:      []string{"plan", "exec"},
			MaxWorkers: 2,
			TurnID:     "t-waves",
		},
		Waves:    2,
		ThreadID: "thread-waves",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.Episode.Reason != "weave" {
		t.Fatalf("%+v", resp)
	}
	if g.n < 2 {
		t.Fatalf("graph writes=%d", g.n)
	}
	if len(prompts) < 3 {
		// wave0: 2 workers, wave1: 2 workers
		t.Fatalf("prompts=%d", len(prompts))
	}
	// Second wave prompts should mention prior_wave
	foundPrior := false
	for _, p := range prompts[2:] {
		if strings.Contains(p, "prior_wave") {
			foundPrior = true
			break
		}
	}
	if !foundPrior {
		t.Fatalf("expected prior_wave seed in later prompts: %v", prompts)
	}
	st, err := r.Threads.Load("thread-waves")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Waves) != 2 {
		t.Fatalf("persisted waves=%d", len(st.Waves))
	}
}
