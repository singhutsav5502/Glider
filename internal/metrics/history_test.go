package metrics_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/metrics"
)

func TestHistoryStoreSessionGrouping(t *testing.T) {
	dir := t.TempDir()
	store, err := metrics.OpenHistoryStore(filepath.Join(dir, "hist"), "run-test-1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Record(metrics.StoredRequest{
		ID: "r1", Route: "local", Action: "local", Model: "m", Tokens: 10, LatencyMs: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(metrics.StoredRequest{
		ID: "r2", Route: "cloud", Action: "cloud", Model: "g", Tokens: 20, LatencyMs: 15,
	}); err != nil {
		t.Fatal(err)
	}

	sessions, err := store.ListSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%v err=%v", sessions, err)
	}
	if sessions[0].RequestCount != 2 || sessions[0].LocalCount != 1 || sessions[0].CloudCount != 1 {
		t.Fatalf("agg=%+v", sessions[0])
	}

	reqs, err := store.ListRequests("run-test-1", 10)
	if err != nil || len(reqs) != 2 {
		t.Fatalf("reqs=%v err=%v", reqs, err)
	}
	if reqs[0].ID != "r2" {
		t.Fatalf("expected newest first, got %s", reqs[0].ID)
	}

	agg, err := store.Aggregates("run-test-1")
	if err != nil {
		t.Fatal(err)
	}
	if agg.AvgLatencyMs < 9 || agg.AvgLatencyMs > 11 {
		t.Fatalf("avg=%v", agg.AvgLatencyMs)
	}
}

func TestCollectorWritesHistory(t *testing.T) {
	dir := t.TempDir()
	store, err := metrics.OpenHistoryStore(filepath.Join(dir, "hist"), "run-c")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	bus := metrics.NewBus()
	c := metrics.NewCollector(bus)
	c.SetHistory(store)
	c.Record(metrics.RequestRecord{
		ID: "x", Route: "local", Model: "m", Tokens: 3, Latency: 2 * time.Millisecond,
	})
	reqs, err := store.ListRequests("run-c", 10)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("reqs=%v err=%v", reqs, err)
	}
	if c.SessionID() != "run-c" {
		t.Fatalf("session=%s", c.SessionID())
	}
}
