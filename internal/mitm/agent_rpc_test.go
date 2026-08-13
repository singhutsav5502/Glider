package mitm_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
)

func TestInterceptorAgentRPCLocalFulfill(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Always Local", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	inter := &mitm.Interceptor{Harness: pc, Metrics: metrics.NewCollector(nil)}

	raw := chatRequestWire("gpt-4o", "say hi")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.AiService/StreamChat",
		bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/proto")
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected local fulfill for StreamChat")
	}
	if exec.n != 1 {
		t.Fatalf("executor=%d", exec.n)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/connect+proto" {
		t.Fatalf("content-type=%q", ct)
	}
	if rr.Body.Len() < 5 {
		t.Fatal("empty connect body")
	}
}

func TestInterceptorAgentRPCOriginPassthrough(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Always Cloud", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	c := metrics.NewCollector(nil)
	inter := &mitm.Interceptor{Harness: pc, Metrics: c}

	raw := chatRequestWire("gpt-4o", "cloud please")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.AiService/StreamChat",
		bytes.NewReader(raw))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("cloud decision must passthrough")
	}
	if exec.n != 0 {
		t.Fatal("executor must not run")
	}
	if c.GetRouteCounts()["action:agent_rpc_passthrough"] != 1 {
		t.Fatalf("counts=%v", c.GetRouteCounts())
	}
	got := make([]byte, len(raw)+1)
	n, _ := req.Body.Read(got)
	if n != len(raw) {
		t.Fatalf("body restore n=%d want %d", n, len(raw))
	}
}

// TestInterceptorAgentServiceRun_DoesNotBlockOnBidiKeepalives is the direct
// regression test for the real, live-confirmed root cause behind a night
// of chased http2-stream-closed symptoms (2026-07-29, found via an
// isolated tools/wirecapture HTTP/2 frame trace): agent.v1.AgentService/Run
// is a genuine bidi-streaming RPC, and cursor-agent's real client sends
// small periodic keepalive envelopes on its own request stream for up to
// ~30s before actually closing it. handleAgentRPC's io.ReadAll(r.Body)
// used to block for that whole window before this handler — or origin
// passthrough below it — could respond at all, long enough for the real
// client to give up and reset the stream first. This mirrors the fix
// already applied and live-confirmed for DelegateHandler
// (internal/mitm/delegate_handler.go), applied here to the plain,
// non-delegate passthrough path this handler covers.
func TestInterceptorAgentServiceRun_DoesNotBlockOnBidiKeepalives(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{{
			Name: "Always Cloud", Priority: 0,
			Trigger: config.TriggerConfig{Type: "always"},
			Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{}
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: exec}
	inter := &mitm.Interceptor{Harness: pc, Metrics: metrics.NewCollector(nil)}

	// First envelope: opaque bytes, matching how a real AgentService/Run
	// request looks to this handler (DecodeChatRequest's default case
	// returns nil, nil for it — not one of the decodable aiserver.v1
	// shapes), so this exercises the exact same fall-through-to-origin
	// path a real, undelegated cursor-agent completion takes.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x03, 'f', 'o', 'o'})
		time.Sleep(2 * time.Second) // simulates cursor-agent's real periodic keepalive envelope
		_, _ = pw.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x02, ':', 0x00})
		pw.Close()
	}()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://agentn.global.api5.cursor.sh/agent.v1.AgentService/Run", pr)
	req.Header.Set("Content-Type", "application/connect+proto")

	done := make(chan struct{})
	var handled bool
	var handleErr error
	go func() {
		handled, handleErr = inter.TryHandle(rr, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("TryHandle did not return promptly for AgentService/Run — it must not block waiting for the client's later keepalive envelopes")
	}
	if handleErr != nil {
		t.Fatalf("unexpected error: %v", handleErr)
	}
	if handled {
		t.Fatal("expected handled=false — an opaque, undecodable body falls through to origin passthrough, exactly like a real cursor-agent completion")
	}
}
