package mitm_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	aiserverv1 "github.com/everestmz/cursor-rpc/cursor/gen/aiserver/v1"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
	"google.golang.org/protobuf/proto"
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

	model := "gpt-4o"
	msg := &aiserverv1.GetChatRequest{
		ModelDetails: &aiserverv1.ModelDetails{ModelName: &model},
		Conversation: []*aiserverv1.ConversationMessage{
			{Text: "say hi", Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN},
		},
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

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

	model := "gpt-4o"
	msg := &aiserverv1.GetChatRequest{
		ModelDetails: &aiserverv1.ModelDetails{ModelName: &model},
		Conversation: []*aiserverv1.ConversationMessage{
			{Text: "cloud please", Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN},
		},
	}
	raw, _ := proto.Marshal(msg)
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
