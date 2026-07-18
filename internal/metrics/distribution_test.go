package metrics_test

import (
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/metrics"
)

func TestComputeDistribution_OriginPassthroughIsCloud(t *testing.T) {
	d := metrics.ComputeDistribution(map[string]int{
		"local":              2,
		"origin_passthrough": 3,
		"cloud":              1,
		"canned":             1,
		"error":              1,
	})
	if d.LocalCount != 2 || d.CloudCount != 4 || d.CannedCount != 1 || d.ErrorCount != 1 {
		t.Fatalf("counts=%+v", d)
	}
	if d.Total != 7 {
		t.Fatalf("total=%d want 7 (errors excluded)", d.Total)
	}
	// 2/7≈28.6, 4/7≈57.1, 1/7≈14.3
	if d.LocalPct < 28 || d.LocalPct > 29 {
		t.Fatalf("local_pct=%v", d.LocalPct)
	}
	if d.CloudPct < 57 || d.CloudPct > 58 {
		t.Fatalf("cloud_pct=%v", d.CloudPct)
	}
	if d.CannedPct < 14 || d.CannedPct > 15 {
		t.Fatalf("canned_pct=%v", d.CannedPct)
	}
}

func TestCollectorGetSnapshot(t *testing.T) {
	c := metrics.NewCollector(nil)
	c.Record(metrics.RequestRecord{Action: "local", Route: "local", Tokens: 100, Latency: time.Millisecond, Reason: "small_offload", Role: "exec"})
	c.Record(metrics.RequestRecord{Action: "origin_passthrough", Route: "cloud", Tokens: 50, Latency: 2 * time.Millisecond, Reason: "must_cloud", Role: "plan"})
	c.Record(metrics.RequestRecord{Action: "canned", Route: "local", Tokens: 10, Latency: time.Millisecond})
	snap := c.GetSnapshot()
	if snap.Distribution.LocalCount != 1 || snap.Distribution.CloudCount != 1 || snap.Distribution.CannedCount != 1 {
		t.Fatalf("dist=%+v", snap.Distribution)
	}
	if snap.TokensSavedEst != 110 { // local + canned tokens
		t.Fatalf("tokens_saved_est=%d", snap.TokensSavedEst)
	}
	if snap.Distribution.Total != 3 {
		t.Fatalf("total=%d", snap.Distribution.Total)
	}
	if snap.Distribution.CloudPct < 33 || snap.Distribution.CloudPct > 34 {
		t.Fatalf("cloud_pct=%v", snap.Distribution.CloudPct)
	}
	if snap.ClassRates["small_offload"] != 1 || snap.ClassRates["must_cloud"] != 1 {
		t.Fatalf("class_rates=%v", snap.ClassRates)
	}
	if snap.RoleRates["exec"] != 1 || snap.RoleRates["plan"] != 1 {
		t.Fatalf("role_rates=%v", snap.RoleRates)
	}
}

func TestHistoryAggregatesDistribution(t *testing.T) {
	dir := t.TempDir()
	store, err := metrics.OpenHistoryStore(dir, "run-dist")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_ = store.Record(metrics.StoredRequest{ID: "1", Action: "local", Route: "local", Tokens: 1})
	_ = store.Record(metrics.StoredRequest{ID: "2", Action: "origin_passthrough", Route: "cloud", Tokens: 1})
	_ = store.Record(metrics.StoredRequest{ID: "3", Action: "canned", Route: "local", Tokens: 1})

	agg, err := store.Aggregates("run-dist")
	if err != nil {
		t.Fatal(err)
	}
	if agg.Session.LocalCount != 1 || agg.Session.CloudCount != 1 || agg.Session.CannedCount != 1 {
		t.Fatalf("session=%+v", agg.Session)
	}
	if agg.Distribution.LocalCount != 1 || agg.Distribution.CloudCount != 1 || agg.Distribution.CannedCount != 1 {
		t.Fatalf("dist=%+v", agg.Distribution)
	}
	if agg.Distribution.Total != 3 {
		t.Fatalf("total=%d", agg.Distribution.Total)
	}
}
