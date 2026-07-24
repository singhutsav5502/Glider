package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestHTTPSessionHeadersAndRetry(t *testing.T) {
	var mu sync.Mutex
	var seenToolsets []string
	var seenSessions []string
	var seenProto []string
	var listFails int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenToolsets = append(seenToolsets, r.Header.Get("X-MCP-Toolsets"))
		seenSessions = append(seenSessions, r.Header.Get("Mcp-Session-Id"))
		seenProto = append(seenProto, r.Header.Get("MCP-Protocol-Version"))
		mu.Unlock()

		var req jsonRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			// Hosted quirks: some edges emit lowercase session header.
			w.Header().Set("mcp-session-id", "sess-hosted-1")
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustJSON(map[string]any{
					"protocolVersion": protocolVersion,
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "mock"},
				}),
			})
		case "tools/list":
			mu.Lock()
			fail := listFails == 0
			if fail {
				listFails++
			}
			mu.Unlock()
			if fail {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid session"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "sess-hosted-1")
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: mustJSON(map[string]any{
					"tools": []Tool{{Name: "get_me"}},
				}),
			})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(map[string]any{})})
		}
	}))
	defer srv.Close()

	m := NewManager()
	ctx := context.Background()
	_, err := m.Connect(ctx, ServerConfig{
		ID: "github-mock", Transport: TransportHTTP, URL: srv.URL,
		Auth:     AuthConfig{Kind: AuthNone},
		Toolsets: []string{"repos", "issues"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := m.ListTools(ctx, "github-mock")
	if err != nil || len(tools) != 1 || tools[0].Name != "get_me" {
		t.Fatalf("tools=%v err=%v", tools, err)
	}

	mu.Lock()
	defer mu.Unlock()
	foundToolsets := false
	foundSessionOnFollowUp := false
	foundProto := false
	for i, ts := range seenToolsets {
		if ts == "repos,issues" {
			foundToolsets = true
		}
		if seenProto[i] == protocolVersion {
			foundProto = true
		}
		if i > 0 && seenSessions[i] == "sess-hosted-1" {
			foundSessionOnFollowUp = true
		}
	}
	if !foundToolsets {
		t.Fatalf("expected X-MCP-Toolsets=repos,issues; got %v", seenToolsets)
	}
	if !foundProto {
		t.Fatalf("expected MCP-Protocol-Version; got %v", seenProto)
	}
	if !foundSessionOnFollowUp {
		t.Fatalf("expected Mcp-Session-Id on follow-up; sessions=%v", seenSessions)
	}
	if listFails != 1 {
		t.Fatalf("expected one session-fail then retry; listFails=%d", listFails)
	}
}

func TestIsSessionOrAuthRetryable(t *testing.T) {
	if !isSessionOrAuthRetryable(401, "") {
		t.Fatal("401")
	}
	if !isSessionOrAuthRetryable(400, "Missing Mcp-Session-Id") {
		t.Fatal("400 session")
	}
	if isSessionOrAuthRetryable(500, "boom") {
		t.Fatal("500 should not retry")
	}
}

func TestStatusAndListToolsOrCatalog(t *testing.T) {
	m := NewManager()
	if err := m.Configure(DefaultGitHubConfig(), DefaultGitHubStdioConfig()); err != nil {
		t.Fatal(err)
	}
	sts := m.Status(context.Background())
	if len(sts) != 2 {
		t.Fatalf("status len=%d", len(sts))
	}
	var gh *ServerStatus
	for i := range sts {
		if sts[i].ID == "github" {
			gh = &sts[i]
		}
	}
	if gh == nil || !gh.IsGitHub || gh.Connected {
		t.Fatalf("github status=%+v", gh)
	}
	tools, source, err := m.ListToolsOrCatalog(context.Background(), "github")
	if err != nil || source != "catalog" || len(tools) == 0 {
		t.Fatalf("tools source=%s n=%d err=%v", source, len(tools), err)
	}
	snap := m.GitHubStatusSnapshot(context.Background())
	if snap.RemoteURL != GitHubRemoteURL {
		t.Fatalf("%+v", snap)
	}
}
