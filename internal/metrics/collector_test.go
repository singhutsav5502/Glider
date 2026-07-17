package metrics_test

import (
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/metrics"
)

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
