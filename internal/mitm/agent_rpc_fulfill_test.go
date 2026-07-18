package mitm_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/cursorrpc"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
)

func TestInterceptorBidiWouldFulfillLocal(t *testing.T) {
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
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         pc,
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	body := buildContextEnvelopeBidi([]byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"ping-glider-test"}]}]}`))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/proto")

	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("BidiAppend must still passthrough (answer is on RunSSE)")
	}
	counts := c.GetRouteCounts()
	if counts["action:bidi_fulfill_armed"] < 1 && counts["action:would_fulfill_local"] < 1 {
		t.Fatalf("counts=%v want bidi_fulfill_armed", counts)
	}
	if counts["action:bidi_extract"] < 1 {
		t.Fatalf("counts=%v want bidi_extract", counts)
	}
	if counts["action:agent_rpc_opaque"] < 1 {
		t.Fatalf("counts=%v want agent_rpc_opaque", counts)
	}
}

func TestInterceptorRunSSELocalFulfill(t *testing.T) {
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
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         pc,
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	reqID := "00000000-0000-4000-8000-000000000042"
	bidiBody := buildContextEnvelopeBidi([]byte(`{"type":"text","text":"ping-fulfill"}`))
	rr1 := httptest.NewRecorder()
	bidiReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(bidiBody)))
	bidiReq.Header.Set("Content-Type", "application/proto")
	bidiReq.Header.Set("X-Request-Id", reqID)
	handled, err := inter.TryHandle(rr1, bidiReq)
	if err != nil || handled {
		t.Fatalf("bidi handled=%v err=%v", handled, err)
	}

	runBody := buildRunSSERequest(reqID)
	rr2 := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(runBody))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", reqID)
	handled, err = inter.TryHandle(rr2, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected RunSSE local fulfill")
	}
	if exec.n != 1 {
		t.Fatalf("executor=%d", exec.n)
	}
	if ct := rr2.Header().Get("Content-Type"); ct != "application/connect+proto" {
		t.Fatalf("ct=%s", ct)
	}
	if rr2.Body.Len() < 10 {
		t.Fatal("empty runsse body")
	}
	counts := c.GetRouteCounts()
	if counts["action:runsse_local"] < 1 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestInterceptorRunSSEToolLoopSkips(t *testing.T) {
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
	hub := mitm.NewAgentFulfillHub()
	c := metrics.NewCollector(nil)
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}
	reqID := "11111111-1111-4111-8111-111111111111"
	hub.ArmLocal(reqID, &mitm.AgentFulfillOffer{
		Local:   true,
		Request: &backend.CompletionRequest{Model: "x", Messages: []backend.Message{{Role: "user", Content: "x"}}},
	})
	runBody := buildRunSSERequest(reqID)
	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(runBody))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", reqID)
	runReq.Header.Set("X-Parent-Agent-Tool-Call-Id", "call-abc")
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("tool-loop RunSSE must origin passthrough")
	}
	counts := c.GetRouteCounts()
	if counts["action:runsse_skip_tool_loop"] < 1 {
		t.Fatalf("counts=%v want runsse_skip_tool_loop", counts)
	}
}

func TestInterceptorToolFollowupWouldLocal(t *testing.T) {
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
	hub := mitm.NewAgentFulfillHub()
	hub.OpenTurnFamily("parent-root", mitm.StickyCloud, "decide_cloud")
	c := metrics.NewCollector(nil)
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
		ToolFollowup: config.ToolFollowupConfig{
			Enabled:            true,
			LocalToolAllowlist: []string{"read_file", "grep"},
			CloudToolDenylist:  []string{"Shell", "Write"},
		},
	}
	reqID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	runBody := buildRunSSERequest(reqID)
	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(runBody))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", reqID)
	runReq.Header.Set("X-Parent-Agent-Tool-Call-Id", "call-read")
	runReq.Header.Set("X-Glider-Tool-Name", "read_file")
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("Path B tool followup must still origin until codec ready")
	}
	counts := c.GetRouteCounts()
	if counts["action:tool_followup_would_local"] < 1 {
		t.Fatalf("counts=%v want tool_followup_would_local", counts)
	}
	if counts["action:runsse_local"] > 0 {
		t.Fatalf("counts=%v must not local-fulfill child RunSSE yet", counts)
	}
}

func TestInterceptorToolFollowupDenylistOrigin(t *testing.T) {
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
	hub := mitm.NewAgentFulfillHub()
	hub.OpenTurnFamily("parent-root", mitm.StickyCloud, "decide_cloud")
	c := metrics.NewCollector(nil)
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
		ToolFollowup: config.ToolFollowupConfig{
			Enabled:            true,
			LocalToolAllowlist: []string{"read_file", "grep"},
			CloudToolDenylist:  []string{"Shell", "Write"},
		},
	}
	reqID := "dddddddd-dddd-4ddd-8ddd-dddddddddd01"
	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(reqID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", reqID)
	runReq.Header.Set("X-Parent-Agent-Tool-Call-Id", "call-shell")
	runReq.Header.Set("X-Glider-Tool-Name", "Shell")
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("denylisted tool must origin")
	}
	counts := c.GetRouteCounts()
	if counts["action:tool_followup_origin"] < 1 {
		t.Fatalf("counts=%v want tool_followup_origin for Shell", counts)
	}
	if counts["action:tool_followup_would_local"] > 0 {
		t.Fatalf("counts=%v Shell must not would_local", counts)
	}
}

func TestShouldStickyCloudOriginUsesGraphLookup(t *testing.T) {
	hub := mitm.NewAgentFulfillHub()
	g := contextgraph.New("")
	hub.Graph = g
	g.Append(contextgraph.Event{
		Kind:      contextgraph.EventTurnOpened,
		TurnID:    "graph-root",
		RequestID: "graph-root",
		Actor:     "cloud",
		Attrs:     map[string]string{"route": "cloud", "source": "explicit_cloud", "root_request_id": "graph-root"},
	})
	// No OpenTurnFamily — sticky must come from contextgraph ResolveCloudSticky.
	root, src, ok := hub.ShouldStickyCloudOrigin("write a final summary of what was done", nil, nil)
	if !ok || root != "graph-root" {
		t.Fatalf("want graph sticky cloud root=graph-root ok=true, got root=%q src=%q ok=%v", root, src, ok)
	}
	_, _, ok = hub.ShouldStickyCloudOrigin("rename foo to bar in this file now", nil, nil)
	if ok {
		t.Fatal("new user turn must not inherit graph cloud sticky")
	}
}

func TestInterceptorDecideCloudBindsSummarySticky(t *testing.T) {
	// Heavy prompt without /cloud → DecideLocal cloud opens turn family; summary sticks.
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{
			Enabled:    true,
			LocalModel: "codellama:7b",
		},
		Rules: []config.RuleConfig{
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	cloudID := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	heavy := []byte(`{"type":"text","text":"architect the module boundaries and redesign the package"}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(heavy, cloudID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi1); err != nil {
		t.Fatal(err)
	}

	sumID := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	summary := []byte(`{"type":"text","text":"summarize the reply for the user"}`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(summary, sumID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi2); err != nil {
		t.Fatal(err)
	}

	counts := c.GetRouteCounts()
	if counts["action:bidi_decide_cloud_family"] < 1 {
		t.Fatalf("counts=%v want bidi_decide_cloud_family from DecideLocal cloud", counts)
	}
	if counts["action:bidi_sticky_cloud"] < 1 {
		t.Fatalf("counts=%v want bidi_sticky_cloud for summary after decide_cloud", counts)
	}
	if counts["action:bidi_fulfill_armed"] > 0 {
		t.Fatalf("counts=%v decide_cloud family must not arm local on summarize", counts)
	}

	// Next real user message re-decides (may go local).
	nextID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	nextBody := []byte(`{"type":"text","text":"rename foo to bar please"}`)
	bidi3 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(nextBody, nextID))))
	bidi3.Header.Set("Content-Type", "application/proto")
	_, _ = inter.TryHandle(httptest.NewRecorder(), bidi3)
	counts = c.GetRouteCounts()
	if counts["action:bidi_fulfill_armed"] < 1 {
		t.Fatalf("counts=%v next user msg should re-decide local", counts)
	}
}

type failExecutor struct{}

func (failExecutor) Execute(ctx context.Context, decision *backend.RoutingDecision, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	return nil, errors.New("ollama unreachable")
}

func TestInterceptorRunSSECannedOnCompleteError(t *testing.T) {
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
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: failExecutor{}, Graph: contextgraph.New("")}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	hub.Graph = contextgraph.New("")
	inter := &mitm.Interceptor{
		Harness:         pc,
		Metrics:         c,
		AgentRPCFulfill: true,
		CannedOnError:   true,
		CannedText:      "pong-canned-test",
		FulfillHub:      hub,
	}

	reqID := "22222222-2222-4222-8222-222222222222"
	hub.ArmLocal(reqID, &mitm.AgentFulfillOffer{
		Local:   true,
		Request: &backend.CompletionRequest{Model: "codellama:7b", Messages: []backend.Message{{Role: "user", Content: "ping"}}},
		UserText: "ping",
	})
	runBody := buildRunSSERequest(reqID)
	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(runBody))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", reqID)
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected canned RunSSE fulfill when CompleteLocal fails")
	}
	counts := c.GetRouteCounts()
	if counts["action:runsse_canned"] < 1 || counts["action:runsse_local"] < 1 {
		t.Fatalf("counts=%v want runsse_canned+runsse_local", counts)
	}
	insp := cursorrpc.InspectRunSSEResponseBody(rr.Body.Bytes(), 200, "application/connect+proto")
	found := false
	for _, f := range insp.Frames {
		if f.Kind == "text_delta" && f.TextHint == "pong-canned-test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("canned text missing in frames=%+v", insp.Frames)
	}
}

func TestInterceptorRunSSECompleteErrorNoCannedFallsThrough(t *testing.T) {
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
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: failExecutor{}},
		Metrics:         metrics.NewCollector(nil),
		AgentRPCFulfill: true,
		CannedOnError:   false,
		FulfillHub:      hub,
	}
	reqID := "33333333-3333-4333-8333-333333333333"
	hub.ArmLocal(reqID, &mitm.AgentFulfillOffer{
		Local:   true,
		Request: &backend.CompletionRequest{Model: "x", Messages: []backend.Message{{Role: "user", Content: "x"}}},
	})
	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(reqID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", reqID)
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("without canned flag must fail-soft to origin")
	}
}

func TestInterceptorRunSSECloudCommandNoCanned(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{
			Enabled:            true,
			LocalModel:         "codellama:7b",
			SmallLocalPriority: 200, // inverted — must still lose to /cloud
		},
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: exec},
		Metrics:         c,
		AgentRPCFulfill: true,
		CannedOnError:   true, // even with canned on, /cloud must not local-fulfill
		FulfillHub:      hub,
	}

	reqID := "44444444-4444-4444-8444-444444444444"
	// TipTap can bury /cloud after other text nodes joined with newlines.
	tipTap := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Composer"},{"type":"text","text":"/cloud hello from cursor"}]}]}`)
	bidiBody := buildContextEnvelopeBidi(tipTap)
	rr1 := httptest.NewRecorder()
	bidiReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(bidiBody)))
	bidiReq.Header.Set("Content-Type", "application/proto")
	bidiReq.Header.Set("X-Request-Id", reqID)
	handled, err := inter.TryHandle(rr1, bidiReq)
	if err != nil || handled {
		t.Fatalf("bidi handled=%v err=%v", handled, err)
	}
	counts := c.GetRouteCounts()
	if counts["action:bidi_decide_passthrough"] < 1 {
		t.Fatalf("counts=%v want bidi_decide_passthrough for /cloud", counts)
	}
	if counts["action:bidi_fulfill_armed"] > 0 {
		t.Fatalf("counts=%v /cloud must not arm local fulfill", counts)
	}

	rr2 := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(reqID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", reqID)
	handled, err = inter.TryHandle(rr2, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("/cloud RunSSE must origin-passthrough (no canned, no local)")
	}
	if exec.n != 0 {
		t.Fatalf("executor should not run for /cloud, got %d", exec.n)
	}
	counts = c.GetRouteCounts()
	if counts["action:runsse_canned"] > 0 || counts["action:runsse_local"] > 0 {
		t.Fatalf("counts=%v want no runsse_canned/local for /cloud", counts)
	}
}

func TestInterceptorRunSSECloudHardForceNoDecideLocal(t *testing.T) {
	// Always-local engine — /cloud hard-force in MITM must ArmOrigin before DecideLocal.
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
	c := metrics.NewCollector(nil)
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: exec},
		Metrics:         c,
		AgentRPCFulfill: true,
		CannedOnError:   true,
		CannedText:      "should-not-appear",
		FulfillHub:      mitm.NewAgentFulfillHub(),
	}
	reqID := "55555555-5555-5555-8555-555555555555"
	tipTap := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"/cloud force origin"}]}]}`)
	rr1 := httptest.NewRecorder()
	bidiReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidi(tipTap))))
	bidiReq.Header.Set("Content-Type", "application/proto")
	bidiReq.Header.Set("X-Request-Id", reqID)
	_, _ = inter.TryHandle(rr1, bidiReq)
	counts := c.GetRouteCounts()
	if counts["action:bidi_cloud_override"] < 1 {
		t.Fatalf("counts=%v want bidi_cloud_override", counts)
	}
	if counts["action:bidi_fulfill_armed"] > 0 {
		t.Fatalf("must not arm local: %v", counts)
	}

	rr2 := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(reqID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", reqID)
	handled, err := inter.TryHandle(rr2, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("expected origin passthrough")
	}
	if exec.n != 0 {
		t.Fatalf("executor ran %d times", exec.n)
	}
	if strings.Contains(rr2.Body.String(), "should-not-appear") {
		t.Fatal("canned text leaked into response")
	}
}

func TestInterceptorStickyCloudInheritsSummary(t *testing.T) {
	// After /cloud, a reply-summary follow-on inherits origin; not conversation-wide.
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		TaskClassifier: config.TaskClassifierConfig{
			Enabled:            true,
			LocalModel:         "codellama:7b",
			SmallLocalPriority: 200,
		},
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	cloudID := "66666666-6666-4666-8666-666666666666"
	tipTap := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"/cloud do the hard refactor"}]}]}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(tipTap, cloudID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	bidi1.Header.Set("X-Request-Id", cloudID)
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi1); err != nil {
		t.Fatal(err)
	}

	sumID := "77777777-7777-4777-8777-777777777777"
	summary := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"summarize the reply for the user"}]}]}`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(summary, sumID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	bidi2.Header.Set("X-Request-Id", sumID)
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi2); err != nil {
		t.Fatal(err)
	}

	counts := c.GetRouteCounts()
	if counts["action:bidi_sticky_cloud"] < 1 {
		t.Fatalf("counts=%v want bidi_sticky_cloud for summary follow-on", counts)
	}
	if counts["action:bidi_fulfill_armed"] > 0 {
		t.Fatalf("counts=%v turn-family cloud must not arm local on summarize", counts)
	}
	if counts["action:origin_passthrough"] < 2 {
		t.Fatalf("counts=%v want Overview origin_passthrough for cloud + follow-on", counts)
	}

	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(sumID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", sumID)
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("turn-family cloud summary RunSSE must origin-passthrough")
	}
}

func TestInterceptorNextUserMessageRedeecidesAfterCloud(t *testing.T) {
	// Next real user message after /cloud must NOT inherit cloud — classifier may go local.
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	cloudID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01"
	cloudBody := []byte(`{"type":"text","text":"/cloud architect the module"}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(cloudBody, cloudID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	_, _ = inter.TryHandle(httptest.NewRecorder(), bidi1)

	nextID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa02"
	nextBody := []byte(`{"type":"text","text":"rename foo to bar please"}`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(nextBody, nextID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	_, _ = inter.TryHandle(httptest.NewRecorder(), bidi2)

	counts := c.GetRouteCounts()
	if counts["action:bidi_sticky_cloud"] > 0 {
		t.Fatalf("counts=%v next user msg must not inherit turn-family cloud", counts)
	}
	if counts["action:bidi_fulfill_armed"] < 1 {
		t.Fatalf("counts=%v next user msg should re-decide local", counts)
	}
}

func TestInterceptorCloudFamilyBlocksSubagentLocal(t *testing.T) {
	// Regression: /cloud parent is origin, but Task subagent BidiAppend ("Say hi via
	// subagent") used to re-decide Small Context Local and pollute the UI.
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	exec := &countingExecutor{}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: exec},
		Metrics:         c,
		AgentRPCFulfill: true,
		CannedOnError:   true,
		FulfillHub:      hub,
	}

	cloudID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb01"
	cloudBody := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"/cloud say hi through a subagent"}]}]}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(cloudBody, cloudID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	bidi1.Header.Set("X-Request-Id", cloudID)
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi1); err != nil {
		t.Fatal(err)
	}

	childID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb02"
	childBody := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Say hi via subagent"}]}]}`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(childBody, childID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	bidi2.Header.Set("X-Request-Id", childID)
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi2); err != nil {
		t.Fatal(err)
	}

	counts := c.GetRouteCounts()
	if counts["action:bidi_cloud_override"] < 1 {
		t.Fatalf("counts=%v want bidi_cloud_override", counts)
	}
	if counts["action:bidi_sticky_cloud_child"] < 1 {
		t.Fatalf("counts=%v want bidi_sticky_cloud_child for subagent", counts)
	}
	if counts["action:bidi_fulfill_armed"] > 0 {
		t.Fatalf("counts=%v subagent must not arm local during /cloud family", counts)
	}

	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(childID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", childID)
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("subagent RunSSE during /cloud family must origin-passthrough")
	}
	if exec.n != 0 {
		t.Fatalf("executor must not run for cloud-family subagent, got %d", exec.n)
	}
	counts = c.GetRouteCounts()
	if counts["action:runsse_local"] > 0 || counts["action:runsse_canned"] > 0 {
		t.Fatalf("counts=%v want no runsse_local/canned for subagent under /cloud", counts)
	}
}

func TestInterceptorStickyLocalInheritsSummaryOnly(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Local", Priority: 100,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/local"}},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name: "Always Cloud", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	localID := "88888888-8888-4888-8888-888888888888"
	tipTap := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"/local rename foo"}]}]}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(tipTap, localID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi1); err != nil {
		t.Fatal(err)
	}

	sumID := "99999999-9999-4999-8999-999999999999"
	summary := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"generate a title for this chat"}]}]}`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(summary, sumID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi2); err != nil {
		t.Fatal(err)
	}

	counts := c.GetRouteCounts()
	if counts["action:bidi_local_override"] < 1 {
		t.Fatalf("counts=%v want bidi_local_override", counts)
	}
	if counts["action:bidi_sticky_local"] < 1 {
		t.Fatalf("counts=%v want bidi_sticky_local for title follow-on", counts)
	}
	if counts["action:bidi_fulfill_armed"] < 2 {
		t.Fatalf("counts=%v want both /local + title armed", counts)
	}
}

func TestInterceptorExplicitLocalBeatsStickyCloud(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Local", Priority: 100,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/local"}},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	cloudID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	cloudBody := []byte(`{"type":"text","text":"/cloud stay on origin"}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(cloudBody, cloudID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	_, _ = inter.TryHandle(httptest.NewRecorder(), bidi1)

	localID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	localBody := []byte(`{"type":"text","text":"/local now go local"}`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(localBody, localID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	_, _ = inter.TryHandle(httptest.NewRecorder(), bidi2)

	counts := c.GetRouteCounts()
	if counts["action:bidi_local_override"] < 1 {
		t.Fatalf("counts=%v /local must beat prior /cloud family", counts)
	}
	if counts["action:bidi_fulfill_armed"] < 1 {
		t.Fatalf("counts=%v want local arm after /local", counts)
	}
}

func TestHubTurnFamilyTTL(t *testing.T) {
	h := mitm.NewAgentFulfillHub()
	h.OpenTurnFamily("root-1", mitm.StickyCloud, "test")
	mode, root, src, ok := h.LookupTurnFamily()
	if !ok || mode != mitm.StickyCloud || root != "root-1" || src != "test" {
		t.Fatalf("got mode=%v root=%q src=%q ok=%v", mode, root, src, ok)
	}
	if _, _, ok := h.InheritTurnFollowOn("rename foo to bar"); ok {
		t.Fatal("real user text must not inherit turn family")
	}
	mode, _, ok = h.InheritTurnFollowOn("summarize the reply for the user")
	if !ok || mode != mitm.StickyCloud {
		t.Fatalf("summary should inherit cloud, got mode=%v ok=%v", mode, ok)
	}
	if !mitm.IsTurnFollowOn("generate a title for this chat") {
		t.Fatal("title prompt should be turn follow-on")
	}
	if !mitm.IsTurnFollowOn("write a final summary of what was done") {
		t.Fatal("final summary should be turn follow-on")
	}
	if !mitm.IsTurnFollowOn("one-sentence executive summary for completed_subtitle") {
		t.Fatal("executive/completed_subtitle should be turn follow-on")
	}
	if !mitm.IsSubagentOrChildTurn("Say hi via subagent", nil, nil) {
		t.Fatal("expected subagent prompt detection")
	}
	if mitm.IsSubagentOrChildTurn("rename foo to bar please", nil, nil) {
		t.Fatal("real user msg must not look like subagent")
	}
}

func TestInterceptorPostCloudFinalSummaryStaysOrigin(t *testing.T) {
	// Regression: final-summary / title chrome after /cloud must not runsse_local.
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	hub.Graph = contextgraph.New("")
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	cloudID := "f1111111-1111-4111-8111-111111111111"
	tipTap := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"/cloud architect the module"}]}]}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(tipTap, cloudID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	bidi1.Header.Set("X-Request-Id", cloudID)
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi1); err != nil {
		t.Fatal(err)
	}

	// Simulate parent RunSSE in-flight past wall TTL concern.
	hub.BeginParentRun(cloudID)

	sumID := "f2222222-2222-4222-8222-222222222222"
	summary := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"write a final summary / one-sentence wrap-up for the user"}]}]}`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(summary, sumID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	bidi2.Header.Set("X-Request-Id", sumID)
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi2); err != nil {
		t.Fatal(err)
	}

	counts := c.GetRouteCounts()
	if counts["action:bidi_sticky_cloud"] < 1 {
		t.Fatalf("counts=%v want bidi_sticky_cloud for final summary", counts)
	}
	if counts["action:bidi_fulfill_armed"] > 0 {
		t.Fatalf("counts=%v must not arm local on final summary", counts)
	}

	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(sumID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", sumID)
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("final summary RunSSE must origin-passthrough, not local")
	}
	counts = c.GetRouteCounts()
	if counts["action:runsse_local"] > 0 {
		t.Fatalf("counts=%v want no runsse_local for post-cloud summary", counts)
	}

	hub.EndParentRun(cloudID)
	view, ok := hub.Graph.Turn(cloudID)
	if !ok || view.Route != "cloud" {
		t.Fatalf("contextgraph turn missing or not cloud: ok=%v route=%q", ok, view.Route)
	}
}

func TestInterceptorToolResultCrumbStaysCloud(t *testing.T) {
	// Mid-turn tool-result pack with short TipTap crumb ("Hi!") used to re-decide
	// Small Context Local and replace StickyCloud — must stay origin.
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := metrics.NewCollector(nil)
	hub := mitm.NewAgentFulfillHub()
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	cloudID := "e1111111-1111-4111-8111-111111111111"
	tipTap := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"/cloud say hi through a subagent"}]}]}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(tipTap, cloudID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	_, _ = inter.TryHandle(httptest.NewRecorder(), bidi1)

	contID := "e2222222-2222-4222-8222-222222222222"
	// Short crumb + embedded tool-call id (as in live dumps after subagent returns).
	crumb := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"Hi!"}]}]}` +
		`call-906a8546-c3b5-4a0e-b117-21ae5cbd659a-57`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(crumb, contID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	_, _ = inter.TryHandle(httptest.NewRecorder(), bidi2)

	counts := c.GetRouteCounts()
	if counts["action:bidi_fulfill_armed"] > 0 {
		t.Fatalf("counts=%v tool-result crumb must not arm local under StickyCloud", counts)
	}
	if counts["action:bidi_sticky_cloud_child"] < 1 && counts["action:bidi_sticky_cloud"] < 1 {
		t.Fatalf("counts=%v want sticky cloud for tool-result crumb", counts)
	}

	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(contID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("tool-result continuation RunSSE must origin")
	}
	counts = c.GetRouteCounts()
	if counts["action:runsse_local"] > 0 {
		t.Fatalf("counts=%v no runsse_local for mid-cloud tool crumb", counts)
	}
}

func TestStickyCloudNotDowngradedByDecideLocal(t *testing.T) {
	h := mitm.NewAgentFulfillHub()
	h.OpenTurnFamily("root", mitm.StickyCloud, "explicit_cloud")
	h.OpenTurnFamily("other", mitm.StickyLocal, "decide_local")
	mode, root, _, ok := h.LookupTurnFamily()
	if !ok || mode != mitm.StickyCloud || root != "root" {
		t.Fatalf("StickyLocal must not downgrade live StickyCloud: mode=%v root=%q ok=%v", mode, root, ok)
	}
}

// TestFinalSummarySticksViaGraphAfterTTLMapCleared is the key leak fix: TTL map
// wiped while parent RunSSE is still open on the context graph — final summary
// with a short TipTap crumb must stay origin (no runsse_local).
func TestFinalSummarySticksViaGraphAfterTTLMapCleared(t *testing.T) {
	engine, err := router.NewEngineFromConfig(config.RoutingConfig{
		Rules: []config.RuleConfig{
			{
				Name: "Explicit Cloud", Priority: 99,
				Trigger: config.TriggerConfig{Type: "explicit", Commands: []string{"/cloud"}},
				Action:  config.ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
			},
			{
				Name: "Always Local", Priority: 0,
				Trigger: config.TriggerConfig{Type: "always"},
				Action:  config.ActionConfig{Target: "local", Model: "codellama:7b"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := metrics.NewCollector(nil)
	g := contextgraph.New("")
	hub := mitm.NewAgentFulfillHub()
	hub.Graph = g
	inter := &mitm.Interceptor{
		Harness:         &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}, Graph: g},
		Metrics:         c,
		AgentRPCFulfill: true,
		FulfillHub:      hub,
	}

	cloudID := "g1111111-1111-4111-8111-111111111111"
	tipTap := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"/cloud design the API"}]}]}`)
	bidi1 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(tipTap, cloudID))))
	bidi1.Header.Set("Content-Type", "application/proto")
	bidi1.Header.Set("X-Request-Id", cloudID)
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi1); err != nil {
		t.Fatal(err)
	}
	hub.BeginParentRun(cloudID)
	// Wipe TTL map; graph still has RunSSEOpen keeping cloud live.
	hub.ResetFamilyForTest()

	sumID := "g2222222-2222-4222-8222-222222222222"
	// TipTap carries the chrome summary prompt (wire body hex-encodes TipTap, so
	// trailing ASCII outside TipTap is not visible to IsTurnFollowOnBody).
	summary := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"write a final summary / one-sentence wrap-up for completed_subtitle"}]}]}`)
	bidi2 := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(buildContextEnvelopeBidiWithID(summary, sumID))))
	bidi2.Header.Set("Content-Type", "application/proto")
	bidi2.Header.Set("X-Request-Id", sumID)
	if _, err := inter.TryHandle(httptest.NewRecorder(), bidi2); err != nil {
		t.Fatal(err)
	}

	counts := c.GetRouteCounts()
	if counts["action:bidi_fulfill_armed"] > 0 {
		t.Fatalf("counts=%v graph sticky must not arm local after TTL wipe", counts)
	}
	if counts["action:bidi_sticky_cloud"] < 1 && counts["action:bidi_sticky_cloud_child"] < 1 {
		t.Fatalf("counts=%v want sticky cloud via graph correlation", counts)
	}

	rr := httptest.NewRecorder()
	runReq := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/agent.v1.AgentService/RunSSE",
		bytes.NewReader(buildRunSSERequest(sumID)))
	runReq.Header.Set("Content-Type", "application/connect+proto")
	runReq.Header.Set("X-Request-Id", sumID)
	handled, err := inter.TryHandle(rr, runReq)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("final summary RunSSE must origin after graph sticky")
	}
	counts = c.GetRouteCounts()
	if counts["action:runsse_local"] > 0 {
		t.Fatalf("counts=%v no runsse_local", counts)
	}
	if g.TurnIDForRequest(sumID) != cloudID {
		t.Fatalf("summary request not bound into cloud turn: %q", g.TurnIDForRequest(sumID))
	}
	hub.EndParentRun(cloudID)
}

func buildRunSSERequest(reqID string) []byte {
	// Connect frame flags=0 wrapping protobuf field 1 = UUID string.
	inner := append([]byte{0x0a, byte(len(reqID))}, []byte(reqID)...)
	frame := []byte{0x00, 0x00, 0x00, 0x00, byte(len(inner))}
	return append(frame, inner...)
}

func TestInterceptorBidiFulfillFlagOffNoWouldFulfill(t *testing.T) {
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
	pc := &orchestrator.PipelineCompleter{Router: engine, Executor: &countingExecutor{}}
	c := metrics.NewCollector(nil)
	inter := &mitm.Interceptor{Harness: pc, Metrics: c, AgentRPCFulfill: false}

	body := buildContextEnvelopeBidi([]byte(`{"type":"text","text":"no-flag"}`))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/proto")
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("passthrough")
	}
	counts := c.GetRouteCounts()
	if counts["action:would_fulfill_local"] != 0 {
		t.Fatalf("flag off must not would_fulfill: %v", counts)
	}
}

func buildContextEnvelopeBidi(tiptap []byte) []byte {
	return buildContextEnvelopeBidiWithID(tiptap, "00000000-0000-4000-8000-000000000042")
}

func buildContextEnvelopeBidiWithID(tiptap []byte, reqID string) []byte {
	var nested []byte
	meta := make([]byte, 64)
	for i := range meta {
		meta[i] = 'm'
	}
	nested = append(nested, 0x0a, byte(len(meta)))
	nested = append(nested, meta...)
	// field 2 = tipTap
	nested = append(nested, protoLD(2, tiptap)...)
	label := []byte("system_prompt")
	nested = append(nested, 0x72, byte(len(label)))
	nested = append(nested, label...)
	for len(nested) < 300 {
		pad := []byte("xxxxxxxx")
		nested = append(nested, 0x0a, byte(len(pad)))
		nested = append(nested, pad...)
	}
	inner := protoLD(1, nested)
	innerHex := hex.EncodeToString(inner)
	body := protoLD(1, []byte(innerHex))
	if reqID == "" {
		reqID = "00000000-0000-4000-8000-000000000042"
	}
	nid := append([]byte{0x0a, byte(len(reqID))}, reqID...)
	body = append(body, 0x12, byte(len(nid)))
	body = append(body, nid...)
	return body
}

func protoLD(field int, payload []byte) []byte {
	tag := byte(field<<3 | 2)
	n := len(payload)
	out := []byte{tag}
	for n >= 0x80 {
		out = append(out, byte(n)|0x80)
		n >>= 7
	}
	out = append(out, byte(n))
	return append(out, payload...)
}
