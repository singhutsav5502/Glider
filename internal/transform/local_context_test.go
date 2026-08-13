package transform_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/transform"
)

func TestBoundLocalContext_LatestTurnDropsHistory(t *testing.T) {
	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "old turn one"},
			{Role: "assistant", Content: "old reply"},
			{Role: "user", Content: "old turn two"},
			{Role: "assistant", Content: "more history"},
			{Role: "user", Content: "rename foo to bar"},
		},
	}
	out := transform.BoundLocalContext(req, config.TransformConfig{
		LocalContext:        "latest_turn",
		LocalSystemMaxChars: 4000,
	})
	if len(out.Messages) != 2 {
		t.Fatalf("got %d messages: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[0].Role != "system" || out.Messages[1].Content != "rename foo to bar" {
		t.Fatalf("unexpected bound context: %+v", out.Messages)
	}
}

func TestBoundLocalContext_KeepsToolLoopTail(t *testing.T) {
	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "ancient"},
			{Role: "assistant", Content: "ancient reply"},
			{Role: "user", Content: "read the file"},
			{Role: "assistant", Content: "", ToolCalls: []byte(`[{"id":"1","function":{"name":"read_file"}}]`)},
			{Role: "tool", Content: "file contents here", ToolCallID: "1"},
		},
	}
	out := transform.BoundLocalContext(req, config.TransformConfig{LocalContext: "latest_turn"})
	if len(out.Messages) != 4 {
		t.Fatalf("want system+user+assistant+tool (4), got %d: %+v", len(out.Messages), out.Messages)
	}
	if out.Messages[1].Content != "read the file" {
		t.Fatalf("tool loop should start at latest user, got %+v", out.Messages)
	}
}

func TestBoundLocalContext_BoundsSystem(t *testing.T) {
	big := strings.Repeat("S", 500)
	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "system", Content: big},
			{Role: "user", Content: "hi"},
		},
	}
	out := transform.BoundLocalContext(req, config.TransformConfig{
		LocalContext:        "latest_turn",
		LocalSystemMaxChars: 40,
	})
	if len(out.Messages[0].Content) > 40 {
		t.Fatalf("system not bounded: %d chars", len(out.Messages[0].Content))
	}
}

func TestBoundLocalContext_FullIsNoOp(t *testing.T) {
	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "a"},
			{Role: "user", Content: "b"},
		},
	}
	out := transform.BoundLocalContext(req, config.TransformConfig{LocalContext: "full"})
	if len(out.Messages) != 2 {
		t.Fatalf("full mode should keep history, got %d", len(out.Messages))
	}
}

func TestBoundLocalContext_SingleUserNoOpShape(t *testing.T) {
	// Path B extract shape.
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "fix typo"}},
	}
	out := transform.BoundLocalContext(req, config.TransformConfig{LocalContext: "latest_turn"})
	if len(out.Messages) != 1 || out.Messages[0].Content != "fix typo" {
		t.Fatalf("Path B shape changed: %+v", out.Messages)
	}
}

func TestInjectEpisodeContext_PrependsPreamble(t *testing.T) {
	req := &backend.CompletionRequest{
		Messages: []backend.Message{{Role: "user", Content: "continue"}},
	}
	eps := []contextkit.Episode{{Summary: "did the rename"}, {Summary: "fixed tests"}}
	out := transform.InjectEpisodeContext(req, eps, config.TransformConfig{
		LocalEpisodeCount:    2,
		LocalEpisodeMaxChars: 800,
	})
	if len(out.Messages) != 2 || out.Messages[0].Role != "system" {
		t.Fatalf("want system+user, got %+v", out.Messages)
	}
	if !strings.Contains(out.Messages[0].Content, "did the rename") {
		t.Fatalf("missing episode: %q", out.Messages[0].Content)
	}
	// Bound after inject still keeps latest user.
	bound := transform.BoundLocalContext(out, config.TransformConfig{LocalContext: "latest_turn"})
	if len(bound.Messages) < 2 || bound.Messages[len(bound.Messages)-1].Content != "continue" {
		t.Fatalf("bound lost user: %+v", bound.Messages)
	}
}
