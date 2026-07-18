package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsInlineToken(t *testing.T) {
	cfg := DefaultGitHubConfig()
	cfg.Auth.Token = "ghp_secret"
	if err := ValidateServerConfig(&cfg); err == nil {
		t.Fatal("expected reject inline token")
	}
}

func TestGitHubConfigDefaults(t *testing.T) {
	cfg := DefaultGitHubConfig()
	if cfg.URL != GitHubRemoteURL {
		t.Fatalf("url=%s", cfg.URL)
	}
	if cfg.Auth.TokenEnv == "" {
		t.Fatal("token_env empty")
	}
	if err := ValidateServerConfig(&cfg); err != nil {
		t.Fatal(err)
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

func TestStdioServeRoundTripPipes(t *testing.T) {
	clientToServerR, clientToServerW := io.Pipe()
	serverToClientR, serverToClientW := io.Pipe()
	s := NewLocalServer()
	s.SetStdio(clientToServerR, serverToClientW)
	_ = s.RegisterTool(Tool{Name: "echo", Description: "echo"}, func(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
		return CallResult{Content: "live:" + name + ":" + string(args)}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(ctx) }()

	write := func(v any) {
		b, _ := json.Marshal(v)
		if _, err := clientToServerW.Write(append(b, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	read := func() jsonRPCResponse {
		sc := bufio.NewScanner(serverToClientR)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		if !sc.Scan() {
			t.Fatal("no response")
		}
		var resp jsonRPCResponse
		if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	write(jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: mustJSON(defaultInitializeParams())})
	if resp := read(); resp.Error != nil {
		t.Fatalf("init: %v", resp.Error)
	}
	write(jsonRPCNotification{JSONRPC: "2.0", Method: "notifications/initialized"})
	write(jsonRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list", Params: mustJSON(map[string]any{})})
	resp := read()
	var listed struct {
		Tools []Tool `json:"tools"`
	}
	_ = json.Unmarshal(resp.Result, &listed)
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "echo" {
		t.Fatalf("%+v", listed)
	}
	write(jsonRPCRequest{JSONRPC: "2.0", ID: 3, Method: "tools/call", Params: mustJSON(map[string]any{
		"name": "echo", "arguments": map[string]any{"msg": "hi"},
	})})
	resp = read()
	text, isErr, err := parseContentText(resp.Result)
	if err != nil || isErr || !strings.Contains(text, "live:echo") || strings.Contains(text, "stub") {
		t.Fatalf("content=%q err=%v", text, err)
	}

	cancel()
	_ = clientToServerW.Close()
	_ = serverToClientW.Close()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}
}

func TestHTTPRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "test-sess")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustJSON(map[string]any{
					"protocolVersion": protocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "mock"},
				}),
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustJSON(map[string]any{
					"tools": []Tool{{Name: "ping", Description: "pong"}},
				}),
			})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustJSON(map[string]any{
					"content": []map[string]string{{"type": "text", "text": "live-pong"}},
				}),
			})
		default:
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(map[string]any{})})
		}
	}))
	defer srv.Close()

	m := NewManager()
	ctx := context.Background()
	_, err := m.Connect(ctx, ServerConfig{
		ID: "http-mock", Transport: TransportHTTP, URL: srv.URL,
		Auth: AuthConfig{Kind: AuthNone},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := m.ListTools(ctx, "http-mock")
	if err != nil || len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("%v err=%v", tools, err)
	}
	cr, err := m.CallTool(ctx, "http-mock", "ping", nil)
	if err != nil || cr.Content != "live-pong" {
		t.Fatalf("%+v err=%v", cr, err)
	}
	if strings.Contains(cr.Content, "stub") {
		t.Fatal("stub leak")
	}
}

func TestConfigureStores(t *testing.T) {
	m := NewManager()
	cfg := DefaultGitHubStdioConfig()
	cfg.Auth.Token = ""
	if err := m.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	got, ok := m.Config("github-stdio")
	if !ok || got.Command != "docker" {
		t.Fatalf("%+v", got)
	}
	_ = GitHubInstallNotes
}
