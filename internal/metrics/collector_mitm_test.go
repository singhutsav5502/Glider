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
