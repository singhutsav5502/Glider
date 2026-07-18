package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// httpSession is a live MCP session over Streamable HTTP (or JSON POST fallback).
type httpSession struct {
	id       string
	serverID string
	url      string
	auth     AuthConfig
	client   *http.Client
	session  string // MCP-Session-Id

	mu      sync.Mutex
	nextID  atomic.Int64
	notify  func(Notification)
	closed  atomic.Bool
}

func startHTTPSession(ctx context.Context, cfg ServerConfig, notify func(Notification)) (*httpSession, error) {
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return nil, fmt.Errorf("mcp http: url required")
	}
	auth, err := ResolveAuth(cfg.Auth)
	if err != nil && cfg.Auth.Kind != AuthNone && cfg.Auth.Kind != "" {
		return nil, err
	}
	s := &httpSession{
		id:       cfg.ID + "-http",
		serverID: cfg.ID,
		url:      url,
		auth:     auth,
		client:   &http.Client{Timeout: 120 * time.Second},
		notify:   notify,
	}
	if err := s.handshake(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *httpSession) ID() string       { return s.id }
func (s *httpSession) ServerID() string { return s.serverID }

func (s *httpSession) Close(context.Context) error {
	s.closed.Store(true)
	return nil
}

func (s *httpSession) handshake(ctx context.Context) error {
	_, err := s.call(ctx, "initialize", defaultInitializeParams())
	if err != nil {
		return fmt.Errorf("mcp http initialize: %w", err)
	}
	// notifications/initialized — best-effort
	_ = s.postNotification(ctx, "notifications/initialized", map[string]any{})
	return nil
}

func (s *httpSession) setAuthHeaders(req *http.Request) {
	h := AuthorizationHeader(s.auth)
	if h == "" {
		return
	}
	name := s.auth.HeaderName
	if name == "" {
		name = "Authorization"
	}
	if s.auth.Kind == AuthHeader {
		req.Header.Set(name, s.auth.Token)
		return
	}
	req.Header.Set(name, h)
}

func (s *httpSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("mcp http: session closed")
	}
	id := s.nextID.Add(1)
	payload, err := encodeRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	s.setAuthHeaders(req)
	if s.session != "" {
		req.Header.Set("Mcp-Session-Id", s.session)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.mu.Lock()
		s.session = sid
		s.mu.Unlock()
	}

	ct := resp.Header.Get("Content-Type")
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	if strings.Contains(ct, "text/event-stream") {
		return parseSSEJSONRPC(body, id)
	}
	var rpc jsonRPCResponse
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, fmt.Errorf("mcp http decode: %w", err)
	}
	if rpc.Error != nil {
		return nil, rpc.Error
	}
	return rpc.Result, nil
}

func (s *httpSession) postNotification(ctx context.Context, method string, params any) error {
	payload, err := encodeNotification(method, params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	s.setAuthHeaders(req)
	if s.session != "" {
		req.Header.Set("Mcp-Session-Id", s.session)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func parseSSEJSONRPC(body []byte, wantID any) (json.RawMessage, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var dataBuf strings.Builder
	flush := func() (json.RawMessage, bool, error) {
		if dataBuf.Len() == 0 {
			return nil, false, nil
		}
		raw := []byte(dataBuf.String())
		dataBuf.Reset()
		var rpc jsonRPCResponse
		if err := json.Unmarshal(raw, &rpc); err != nil {
			return nil, false, nil
		}
		if rpc.ID != nil && fmt.Sprint(rpc.ID) != fmt.Sprint(wantID) {
			return nil, false, nil
		}
		if rpc.Error != nil {
			return nil, true, rpc.Error
		}
		return rpc.Result, true, nil
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			dataBuf.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		if line == "" {
			if res, ok, err := flush(); ok {
				return res, err
			}
		}
	}
	if res, ok, err := flush(); ok {
		return res, err
	}
	return nil, fmt.Errorf("mcp http sse: no json-rpc result")
}

func (s *httpSession) listTools(ctx context.Context) ([]Tool, error) {
	raw, err := s.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

func (s *httpSession) callTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	params := map[string]any{"name": name, "arguments": map[string]any{}}
	if len(args) > 0 && string(args) != "null" {
		var m any
		if err := json.Unmarshal(args, &m); err == nil {
			params["arguments"] = m
		}
	}
	raw, err := s.call(ctx, "tools/call", params)
	if err != nil {
		return CallResult{}, err
	}
	text, isErr, perr := parseContentText(raw)
	if perr != nil {
		return CallResult{Raw: raw}, perr
	}
	return CallResult{Content: text, IsError: isErr, Raw: raw}, nil
}

func (s *httpSession) listResources(ctx context.Context) ([]Resource, error) {
	raw, err := s.call(ctx, "resources/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Resources []Resource `json:"resources"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Resources, nil
}

func (s *httpSession) listPrompts(ctx context.Context) ([]Prompt, error) {
	raw, err := s.call(ctx, "prompts/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Prompts []Prompt `json:"prompts"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Prompts, nil
}

func truncateStr(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
