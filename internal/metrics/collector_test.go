package metrics_test

import (
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/metrics"
)

// TestRecord_DelegateAction_PublishesToRequestLog is the direct regression
// test for the 2026-07-30 fix: a delegate call's RequestRecord must reach
// the Overview request log (History + Bus), not silently get downgraded
// to a routeCounts-only IncAction bump the way a genuinely non-request-log
// action (e.g. "decrypt", "blind_tunnel") correctly does. Before
// IsRequestLogAction included "delegate", this test would have seen zero
// events on the bus even though Record itself never errored or panicked —
// exactly the kind of silent gap a "did it compile and not crash" check
// alone would have missed.
func TestRecord_DelegateAction_PublishesToRequestLog(t *testing.T) {
	bus := metrics.NewBus()
	ch := bus.Subscribe(4)
	defer bus.Unsubscribe(ch)

	c := metrics.NewCollector(bus)
	c.Record(metrics.RequestRecord{
		ID:     "delegate_test",
		Mode:   "mitm",
		Action: "delegate",
		Route:  "delegate",
		Model:  "agy",
		Rule:   "delegate:default",
	})

	select {
	case ev := <-ch:
		data, ok := ev.Data.(metrics.RequestEventData)
		if !ok {
			t.Fatalf("got event data of type %T, want RequestEventData", ev.Data)
		}
		if data.Action != "delegate" || data.Model != "agy" {
			t.Fatalf("got %+v", data)
		}
	case <-time.After(time.Second):
		t.Fatal("no event published for a delegate RequestRecord — it was silently downgraded to a counter-only bump")
	}

	counts := c.GetRouteCounts()
	if counts["action:delegate"] != 1 {
		t.Fatalf("route counts = %+v, want action:delegate = 1", counts)
	}
}

// T4.3.1
func TestRouteCounts(t *testing.T) {
	c := metrics.NewCollector(nil)
	for i := 0; i < 5; i++ {
		c.Record(metrics.RequestRecord{Route: "local", Tokens: 1})
	}
	for i := 0; i < 3; i++ {
		c.Record(metrics.RequestRecord{Route: "cloud", Tokens: 1})
	}
	got := c.GetRouteCounts()
	if got["local"] != 5 || got["cloud"] != 3 {
		t.Fatalf("got=%v", got)
	}
}

// T4.3.2
func TestTokenStats(t *testing.T) {
	c := metrics.NewCollector(nil)
	for _, n := range []int{2000, 5000, 1000} {
		c.Record(metrics.RequestRecord{Route: "local", Tokens: n})
	}
	s := c.GetTokenStats()
	if s.Total != 8000 || s.Avg != 2666 || s.Min != 1000 || s.Max != 5000 {
		t.Fatalf("stats=%+v", s)
	}
}

// T4.3.3
func TestCostSavings(t *testing.T) {
	c := metrics.NewCollector(nil)
	c.SetCloudCostPerRequest(0.10)
	for i := 0; i < 5; i++ {
		c.Record(metrics.RequestRecord{Route: "local", Tokens: 100})
	}
	s := c.GetCostSavings()
	if s.EstimatedCloudCost != 0.50 || s.ActualCost != 0 || s.Savings != 0.50 {
		t.Fatalf("savings=%+v", s)
	}
}

// T4.3.4
func TestLatencyPercentiles(t *testing.T) {
	c := metrics.NewCollector(nil)
	for i := 1; i <= 100; i++ {
		c.Record(metrics.RequestRecord{Route: "local", Tokens: 1, Latency: time.Duration(i) * time.Millisecond})
	}
	p := c.GetLatencyPercentiles()
	if p.P50 < 40*time.Millisecond || p.P50 > 60*time.Millisecond {
		t.Fatalf("p50=%v", p.P50)
	}
	if p.P90 < 85*time.Millisecond || p.P90 > 95*time.Millisecond {
		t.Fatalf("p90=%v", p.P90)
	}
	if p.P99 < 95*time.Millisecond {
		t.Fatalf("p99=%v", p.P99)
	}
}
