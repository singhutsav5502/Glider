package swarm_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/swarm"
)

func TestDecomposeSubTasks(t *testing.T) {
	plan := `
1. Inspect contextgraph QueryOpts
2. Run FanOut with conflict callouts
3. Resume durable thread after restart
`
	sts := swarm.DecomposeSubTasks(plan, 4)
	if len(sts) != 3 {
		t.Fatalf("got %d: %+v", len(sts), sts)
	}
	if !strings.Contains(sts[0].Prompt, "Inspect") {
		t.Fatalf("%+v", sts[0])
	}
}

func TestWeavePolicies(t *testing.T) {
	merges := []contextkit.Episode{
		{Summary: "research says GO ship", Role: "research", Tokens: 10},
		{Summary: "qa says NO-GO block release", Role: "qa", Tokens: 12},
	}
	results := [][]swarm.Result{
		{{WorkerID: "r1", Role: swarm.RoleResearch, Episode: contextkit.Episode{Summary: "approve ship"}}},
		{{WorkerID: "q1", Role: "qa", Episode: contextkit.Episode{Summary: "reject block"}}},
	}
	concat := swarm.ApplyWeavePolicy(swarm.WeaveConcatenate, merges, results)
	if !strings.Contains(concat.Summary, "concatenate") {
		t.Fatalf("%s", concat.Summary)
	}
	weighted := swarm.ApplyWeavePolicy(swarm.WeaveRoleWeighted, merges, results)
	if !strings.Contains(weighted.Summary, "role_weighted") {
		t.Fatalf("%s", weighted.Summary)
	}
	conflict := swarm.ApplyWeavePolicy(swarm.WeaveConflictCallout, merges, results)
	if !strings.Contains(conflict.Summary, "conflict") {
		t.Fatalf("%s", conflict.Summary)
	}
}

func TestThreadListAndResumeMeta(t *testing.T) {
	dir := t.TempDir()
	ts := swarm.NewThreadStore(dir)
	_, err := ts.AppendWave("t-list", "turn", "goal", swarm.WaveRecord{Index: 0, Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	_ = ts.MarkResumable("t-list", swarm.WeaveConflictCallout, []string{"do a", "do b"})
	list, err := ts.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "t-list" {
		t.Fatalf("%+v", list)
	}
	if list[0].Status != "resumable" {
		t.Fatalf("status=%s", list[0].Status)
	}
}
