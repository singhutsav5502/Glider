package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	toolsets []string
	extra    map[string]string
	client   *http.Client
	session  string // MCP-Session-Id

	mu     sync.Mutex
	nextID atomic.Int64
	notify func(Notification)
	closed atomic.Bool
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
		toolsets: append([]string(nil), cfg.Toolsets...),
		extra:    copyStringMap(cfg.Auth.Extra),
		client:   &http.Client{Timeout: 120 * time.Second},
		notify:   notify,
	}
	if err := s.handshake(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func copyStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *httpSession) ID() string       { return s.id }
func (s *httpSession) ServerID() string { return s.serverID }

func (s *httpSession) Close(context.Context) error {
	s.closed.Store(true)
	return nil
}

func (s *httpSession) handshake(ctx context.Context) error {
	_, err := s.callOnce(ctx, "initialize", defaultInitializeParams(), false)
	if err != nil && isAuthFailureErr(err) {
		// PAT paste / device flow may land after process start but before Connect.
		s.clearSession()
		s.refreshAuth()
		_, err = s.callOnce(ctx, "initialize", defaultInitializeParams(), false)
	}
	if err != nil {
		return fmt.Errorf("mcp http initialize: %w", classifyAuthErr(err))
	}
	// notifications/initialized — best-effort
	_ = s.postNotification(ctx, "notifications/initialized", map[string]any{})
	return nil
}

func (s *httpSession) setAuthHeaders(req *http.Request) {
	h := AuthorizationHeader(s.auth)
	if h != "" {
		name := s.auth.HeaderName
		if name == "" {
			name = "Authorization"
		}
		if s.auth.Kind == AuthHeader {
			req.Header.Set(name, s.auth.Token)
		} else {
			req.Header.Set(name, h)
		}
	}
	for k, v := range s.extra {
		k = strings.TrimSpace(k)
		if k == "" || strings.TrimSpace(v) == "" {
			continue
		}
		// Extra must not replace the Authorization header that this code just set,
		// unless a person names that header directly.
		if strings.EqualFold(k, "Authorization") && req.Header.Get("Authorization") != "" {
			continue
		}
		req.Header.Set(k, v)
	}
}

func (s *httpSession) setSessionAndProtocolHeaders(req *http.Request) {
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if len(s.toolsets) > 0 {
		req.Header.Set("X-MCP-Toolsets", strings.Join(s.toolsets, ","))
	}
	s.mu.Lock()
	sid := s.session
	s.mu.Unlock()
	if sid != "" {
		// Spec uses Mcp-Session-Id; some gateways normalize case — set canonical.
		req.Header.Set("Mcp-Session-Id", sid)
	}
}

func captureSessionID(h http.Header) string {
	if h == nil {
		return ""
	}
	for _, k := range []string{"Mcp-Session-Id", "mcp-session-id", "MCP-Session-Id"} {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func (s *httpSession) storeSessionID(sid string) {
	if sid == "" {
		return
	}
	s.mu.Lock()
	s.session = sid
	s.mu.Unlock()
}

func (s *httpSession) clearSession() {
	s.mu.Lock()
	s.session = ""
	s.mu.Unlock()
}

// refreshAuth re-resolves token from env/credential file (PAT paste / device flow
// may land after Connect). Hydrates ~/.glider/credentials/github_token into env
// first so UI-pasted PATs are visible without restart. For hosted github, prefer
// the credential file over a stale process env (paste rotates the file).
// Best-effort; keeps prior auth on failure.
func (s *httpSession) refreshAuth() {
	_, _ = HydrateGitHubTokenFromStore()
	hostedGitHub := s.serverID == "github" || strings.Contains(strings.ToLower(s.url), "githubcopilot.com")
	if hostedGitHub {
		if tok, err := LoadGitHubTokenFile(); err == nil {
			if tok = strings.TrimSpace(tok); tok != "" {
				_ = os.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", tok)
			}
		}
	}
	if s.auth.TokenEnv == "" && s.auth.Kind != AuthEnv {
		// Still try GitHub env aliases when this is the hosted github session.
		if hostedGitHub {
			env := ResolveGitHubTokenEnv()
			if env == "" {
				return
			}
			resolved, err := ResolveAuth(AuthConfig{Kind: AuthEnv, TokenEnv: env})
			if err != nil || resolved.Token == "" {
				return
			}
			s.auth = resolved
		}
		return
	}
	cfg := s.auth
	if cfg.Kind == AuthBearer && cfg.TokenEnv != "" {
		cfg.Kind = AuthEnv
		cfg.Token = ""
	}
	resolved, err := ResolveAuth(cfg)
	if err != nil || resolved.Token == "" {
		return
	}
	s.auth = resolved
}

func isSessionOrAuthRetryable(status int, body string) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	if status == http.StatusBadRequest || status == http.StatusNotFound {
		lower := strings.ToLower(body)
		for _, needle := range []string{
			"session", "mcp-session", "initialize", "not initialized",
			"expired", "invalid token", "unauthorized", "forbidden",
			"authentication", "access denied",
		} {
			if strings.Contains(lower, needle) {
				return true
			}
		}
	}
	return false
}

func isAuthFailureErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"mcp http 401", "mcp http 403",
		"unauthorized", "forbidden", "invalid token", "authentication",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// classifyAuthErr wraps HTTP auth failures so dashboard/status surfaces a clear signal.
func classifyAuthErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "mcp http 401"), strings.Contains(lower, "unauthorized"):
		return fmt.Errorf("authentication failed (401) — check PAT / Copilot entitlement: %w", err)
	case strings.Contains(lower, "mcp http 403"), strings.Contains(lower, "forbidden"):
		return fmt.Errorf("forbidden (403) — token may lack Copilot MCP access or toolset scope: %w", err)
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "deadline exceeded"):
		return fmt.Errorf("timeout talking to hosted MCP — check network / VPN: %w", err)
	default:
		return err
	}
}

func (s *httpSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.callOnce(ctx, method, params, true)
}

func (s *httpSession) callOnce(ctx context.Context, method string, params any, allowRetry bool) (json.RawMessage, error) {
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
	s.setSessionAndProtocolHeaders(req)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if sid := captureSessionID(resp.Header); sid != "" {
		s.storeSessionID(sid)
	}

	ct := resp.Header.Get("Content-Type")
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		msg := truncateStr(string(body), 500)
		if allowRetry && method != "initialize" && isSessionOrAuthRetryable(resp.StatusCode, msg) {
			s.clearSession()
			s.refreshAuth()
			if herr := s.handshake(ctx); herr == nil {
				return s.callOnce(ctx, method, params, false)
			}
		}
		return nil, classifyAuthErr(fmt.Errorf("mcp http %d: %s", resp.StatusCode, msg))
	}

	if strings.Contains(ct, "text/event-stream") {
		return parseSSEJSONRPC(body, id)
	}
	var rpc jsonRPCResponse
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, fmt.Errorf("mcp http decode: %w", err)
	}
	if rpc.Error != nil {
		// JSON-RPC auth/session errors: retry once with re-handshake.
		if allowRetry && method != "initialize" && isSessionOrAuthRetryable(0, rpc.Error.Error()) {
			s.clearSession()
			s.refreshAuth()
			if herr := s.handshake(ctx); herr == nil {
				return s.callOnce(ctx, method, params, false)
			}
		}
		return nil, classifyAuthErr(rpc.Error)
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
	s.setSessionAndProtocolHeaders(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if sid := captureSessionID(resp.Header); sid != "" {
		s.storeSessionID(sid)
	}
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
