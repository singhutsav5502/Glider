package metrics_test

import (
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/metrics"
)

func TestRecordPublishesRichRequestEvent(t *testing.T) {
	bus := metrics.NewBus()
	ch := bus.Subscribe(4)
	defer bus.Unsubscribe(ch)

	c := metrics.NewCollector(bus)
	c.Record(metrics.RequestRecord{
		ID:            "r1",
		Mode:          "mitm",
		Action:        "origin_passthrough",
		Route:         "cloud",
		Model:         "gpt-4o",
		OriginalModel: "claude",
		Host:          "api2.cursor.sh",
		Path:          "/v1/chat/completions",
		Rule:          "Context Overflow",
		Tokens:        9000,
		Latency:       12 * time.Millisecond,
	})

	select {
	case ev := <-ch:
		if ev.Type != metrics.EventRequest {
			t.Fatalf("type=%v", ev.Type)
		}
		data, ok := ev.Data.(metrics.RequestEventData)
		if !ok {
			t.Fatalf("data type %T", ev.Data)
		}
		if data.Mode != "mitm" || data.Action != "origin_passthrough" || data.Host != "api2.cursor.sh" {
			t.Fatalf("%+v", data)
		}
		if data.Rule != "Context Overflow" || data.Tokens != 9000 {
			t.Fatalf("%+v", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
	}

	counts := c.GetRouteCounts()
	if counts["action:origin_passthrough"] != 1 || counts["mode:mitm"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestIncActionDoesNotPublishRequestEvent(t *testing.T) {
	bus := metrics.NewBus()
	ch := bus.Subscribe(4)
	defer bus.Unsubscribe(ch)

	c := metrics.NewCollector(bus)
	c.IncAction("mitm", "decrypt")
	c.IncAction("mitm", "skip")

	select {
	case ev := <-ch:
		t.Fatalf("unexpected request event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	counts := c.GetRouteCounts()
	if counts["action:decrypt"] != 1 || counts["action:skip"] != 1 || counts["mode:mitm"] != 2 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestRecordRejectsInformationalAsRequestLog(t *testing.T) {
	bus := metrics.NewBus()
	ch := bus.Subscribe(4)
	defer bus.Unsubscribe(ch)

	c := metrics.NewCollector(bus)
	c.Record(metrics.RequestRecord{
		Mode: "mitm", Action: "decrypt", Route: "decrypt", Host: "api2.cursor.sh",
	})

	select {
	case ev := <-ch:
		t.Fatalf("decrypt must not publish request event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if c.GetRouteCounts()["action:decrypt"] != 1 {
		t.Fatalf("counts=%v", c.GetRouteCounts())
	}
	stats := c.GetTokenStats()
	if stats.Total != 0 || stats.Avg != 0 {
		t.Fatalf("decrypt must not affect token stats: %+v", stats)
	}
}

func TestIsRequestLogAction(t *testing.T) {
	for _, a := range []string{"local", "cloud", "origin_passthrough", "canned", "error"} {
		if !metrics.IsRequestLogAction(a) {
			t.Fatalf("%q should be request-log", a)
		}
	}
	for _, a := range []string{"decrypt", "blind_tunnel", "skip", ""} {
		if metrics.IsRequestLogAction(a) {
			t.Fatalf("%q should not be request-log", a)
		}
	}
}
