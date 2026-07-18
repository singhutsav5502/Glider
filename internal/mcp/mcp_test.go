package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestGitHubConfigAndStubCall(t *testing.T) {
	m := NewManager()
	cfg := DefaultGitHubConfig()
	if cfg.URL != GitHubRemoteURL {
		t.Fatalf("url=%s", cfg.URL)
	}
	_, err := m.Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := m.ListTools(context.Background(), "github")
	if err != nil || len(tools) < 2 {
		t.Fatalf("%v err=%v", tools, err)
	}
	cr, err := m.CallTool(context.Background(), "github", "get_me", nil)
	if err != nil || cr.Content == "" {
		t.Fatalf("%+v err=%v", cr, err)
	}
}

func TestLocalServer(t *testing.T) {
	s := NewLocalServer()
	err := s.RegisterTool(Tool{Name: "echo"}, func(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
		return CallResult{Content: "ok:" + name}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cr, err := s.CallLocal(context.Background(), "echo", nil)
	if err != nil || cr.Content != "ok:echo" {
		t.Fatalf("%+v err=%v", cr, err)
	}
}
