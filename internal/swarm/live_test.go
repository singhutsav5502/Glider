package swarm

import "testing"

func TestLiveProgressFanOut(t *testing.T) {
	r := &Runner{}
	r.Enabled.Store(true)
	turn := "swarm-test-1"
	r.beginLive(turn, turn, []LiveWorkerView{
		{WorkerID: "plan-0", Role: "plan", Status: "pending"},
		{WorkerID: "exec-0", Role: "exec", Status: "pending"},
	}, 1)
	r.setLiveWorker(turn, "plan-0", "plan", "running", "", "")
	v := r.LiveProgress(turn)
	if v == nil || v.Status != "running" {
		t.Fatalf("expected running view, got %+v", v)
	}
	if v.Workers[0].Status != "running" {
		t.Fatalf("plan worker status=%q", v.Workers[0].Status)
	}
	r.setLiveWorker(turn, "plan-0", "plan", "ok", "planned", "")
	r.setLiveWorker(turn, "exec-0", "exec", "fail", "", "boom")
	r.finishLive(turn, false, DecisionRouteView{Current: "merge"})
	v = r.LiveProgress(turn)
	if v.Status != "failed" || v.Phase != "done" {
		t.Fatalf("finish: %+v", v)
	}
	if v.Workers[1].Err != "boom" {
		t.Fatalf("exec err=%q", v.Workers[1].Err)
	}
}
