package mitm_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
)

func TestClassifyPath(t *testing.T) {
	cases := []struct {
		path string
		want mitm.PathKind
	}{
		{"/v1/chat/completions", mitm.PathOpenAICompat},
		{"/openai/v1/chat/completions", mitm.PathOpenAICompat},
		{"/v1/responses", mitm.PathOpenAICompat},
		{"/aiserver.v1.BidiService/BidiAppend", mitm.PathAgentRPC},
		{"/aiserver.v1.BidiService/BidiPoll", mitm.PathAgentRPC},
		{"/aiserver.v1.ChatService/StreamUnifiedChatWithTools", mitm.PathAgentRPC},
		{"/agent.v1.AgentService/RunSSE", mitm.PathAgentRPC},
		{"/aiserver.v1.AiService/StreamChat", mitm.PathAgentRPC},
		{"/aiserver.v1.AiService/ReportClientNumericMetrics", mitm.PathCursorControl},
		{"/aiserver.v1.DashboardService/GetEffectiveUserPlugins", mitm.PathCursorControl},
		{"/aiserver.v1.AnalyticsService/Report", mitm.PathCursorControl},
		{"/aiserver.v1.BackgroundComposerService/ListBackgroundComposers", mitm.PathCursorControl},
		{"/aiserver.v1.SomeFutureService/DoThing", mitm.PathCursorControl},
		{"/healthz", mitm.PathOther},
		{"", mitm.PathOther},
	}
	for _, tc := range cases {
		got := mitm.ClassifyPath(tc.path)
		if got != tc.want {
			t.Errorf("ClassifyPath(%q)=%v want %v", tc.path, got, tc.want)
		}
		if mitm.IsLLMPath(tc.path) != (tc.want == mitm.PathOpenAICompat) {
			t.Errorf("IsLLMPath(%q) inconsistent with kind %v", tc.path, tc.want)
		}
	}
}

func TestPathKindString(t *testing.T) {
	if mitm.PathAgentRPC.String() != "agent_rpc" {
		t.Fatal(mitm.PathAgentRPC.String())
	}
	if mitm.PathOpenAICompat.String() != "openai_compat" {
		t.Fatal(mitm.PathOpenAICompat.String())
	}
}

func TestInterceptorAgentRPCOpaqueMetrics(t *testing.T) {
	c := metrics.NewCollector(nil)
	inter := &mitm.Interceptor{Harness: stubHarness{}, Metrics: c}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend",
		strings.NewReader("\x00binary"))
	req.Header.Set("Content-Type", "application/connect+proto")
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("opaque agent rpc must not be handled")
	}
	counts := c.GetRouteCounts()
	if counts["action:agent_rpc_opaque"] != 1 {
		t.Fatalf("counts=%v want agent_rpc_opaque=1", counts)
	}
}

func TestInterceptorControlSkipMetrics(t *testing.T) {
	c := metrics.NewCollector(nil)
	inter := &mitm.Interceptor{Harness: stubHarness{}, Metrics: c}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"https://api2.cursor.sh/aiserver.v1.DashboardService/GetEffectiveUserPlugins",
		strings.NewReader(`{}`))
	handled, err := inter.TryHandle(rr, req)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("control must not be handled")
	}
	if c.GetRouteCounts()["action:skip_control"] != 1 {
		t.Fatalf("counts=%v", c.GetRouteCounts())
	}
}
