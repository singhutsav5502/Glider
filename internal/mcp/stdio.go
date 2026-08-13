package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/glider-ai/glider/internal/procutil"
	"github.com/glider-ai/glider/internal/safego"
)

// stdioSession is a live MCP session over a subprocess stdin/stdout.
type stdioSession struct {
	id       string
	serverID string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser

	mu       sync.Mutex
	nextID   atomic.Int64
	pending  map[any]chan jsonRPCResponse
	notify   func(Notification)
	closed   atomic.Bool
	readDone chan struct{}
}

func startStdioSession(ctx context.Context, cfg ServerConfig, notify func(Notification)) (*stdioSession, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("mcp stdio: command required")
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	procutil.HideWindow(cmd)
	env := os.Environ()
	// Inject resolved auth token into env for docker -e TOKEN patterns.
	if auth, err := ResolveAuth(cfg.Auth); err == nil && auth.Token != "" && auth.TokenEnv != "" {
		env = append(env, auth.TokenEnv+"="+auth.Token)
		// Also set common GitHub env aliases so official image picks them up.
		for _, alias := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
			if auth.TokenEnv != alias {
				env = append(env, alias+"="+auth.Token)
			}
		}
	}
	for _, e := range cfg.Env {
		env = append(env, e)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("mcp stdio start: %w", err)
	}

	s := &stdioSession{
		id:       cfg.ID + "-stdio",
		serverID: cfg.ID,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
		pending:  make(map[any]chan jsonRPCResponse),
		notify:   notify,
		readDone: make(chan struct{}),
	}
	safego.Go("mcp-stdio-drainStderr:"+cfg.ID, nil, s.drainStderr)
	safego.Go("mcp-stdio-readLoop:"+cfg.ID, nil, s.readLoop)

	if err := s.handshake(ctx); err != nil {
		_ = s.Close(context.Background())
		return nil, err
	}
	return s, nil
}

func (s *stdioSession) ID() string       { return s.id }
func (s *stdioSession) ServerID() string { return s.serverID }

func (s *stdioSession) Close(context.Context) error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	_ = s.stdin.Close()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	<-s.readDone
	return nil
}

func (s *stdioSession) drainStderr() {
	sc := bufio.NewScanner(s.stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		// Logging only; stderr is not protocol.
		_ = sc.Text()
	}
}

func (s *stdioSession) readLoop() {
	defer close(s.readDone)
	buf := &bytes.Buffer{}
	for {
		line, err := readJSONLine(s.stdout, buf)
		if err != nil {
			s.failPending(err)
			return
		}
		s.dispatch(line)
	}
}

func (s *stdioSession) failPending(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.pending {
		ch <- jsonRPCResponse{Error: &jsonRPCError{Code: -32000, Message: err.Error()}}
		delete(s.pending, id)
	}
}

func (s *stdioSession) dispatch(line []byte) {
	// Notification (no id) vs response.
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Error  *jsonRPCError   `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return
	}
	if probe.Method != "" && (len(probe.ID) == 0 || string(probe.ID) == "null") {
		var n Notification
		_ = json.Unmarshal(line, &struct {
			Method *string          `json:"method"`
			Params *json.RawMessage `json:"params"`
		}{Method: &n.Method, Params: &n.Params})
		n.Method = probe.Method
		var full jsonRPCNotification
		if json.Unmarshal(line, &full) == nil {
			n.Params = full.Params
		}
		if s.notify != nil {
			s.notify(n)
		}
		return
	}
	var resp jsonRPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return
	}
	s.mu.Lock()
	ch := s.pending[resp.ID]
	delete(s.pending, resp.ID)
	s.mu.Unlock()
	if ch != nil {
		ch <- resp
	}
}

func (s *stdioSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("mcp stdio: session closed")
	}
	id := s.nextID.Add(1)
	payload, err := encodeRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan jsonRPCResponse, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()

	s.mu.Lock()
	_, werr := s.stdin.Write(append(payload, '\n'))
	s.mu.Unlock()
	if werr != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, werr
	}

	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (s *stdioSession) notifyInitialized(ctx context.Context) error {
	payload, err := encodeNotification("notifications/initialized", map[string]any{})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.stdin.Write(append(payload, '\n'))
	return err
}

func (s *stdioSession) handshake(ctx context.Context) error {
	_, err := s.call(ctx, "initialize", defaultInitializeParams())
	if err != nil {
		return fmt.Errorf("mcp initialize: %w", err)
	}
	if err := s.notifyInitialized(ctx); err != nil {
		return fmt.Errorf("mcp initialized notify: %w", err)
	}
	return nil
}

func (s *stdioSession) listTools(ctx context.Context) ([]Tool, error) {
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

func (s *stdioSession) callTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	params := map[string]any{"name": name}
	if len(args) > 0 && string(args) != "null" {
		var m any
		if err := json.Unmarshal(args, &m); err == nil {
			params["arguments"] = m
		} else {
			params["arguments"] = map[string]any{}
		}
	} else {
		params["arguments"] = map[string]any{}
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

func (s *stdioSession) listResources(ctx context.Context) ([]Resource, error) {
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

func (s *stdioSession) listPrompts(ctx context.Context) ([]Prompt, error) {
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
