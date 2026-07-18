package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// LocalServer is a ServerAdapter that registers tools and can Serve over stdio.
type LocalServer struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	handlers map[string]ToolHandler
	stdin    io.Reader
	stdout   io.Writer
	serving  atomic.Bool
	cancel   context.CancelFunc
}

// NewLocalServer constructs an empty adapter (stdio defaults to os.Stdin/Stdout on Serve).
func NewLocalServer() *LocalServer {
	return &LocalServer{
		tools:    make(map[string]Tool),
		handlers: make(map[string]ToolHandler),
	}
}

// SetStdio overrides stdio streams (tests).
func (s *LocalServer) SetStdio(in io.Reader, out io.Writer) {
	s.stdin = in
	s.stdout = out
}

func (s *LocalServer) RegisterTool(tool Tool, handler ToolHandler) error {
	if s == nil {
		return fmt.Errorf("nil server")
	}
	name := tool.Name
	if name == "" {
		return fmt.Errorf("tool name required")
	}
	if handler == nil {
		return fmt.Errorf("handler required")
	}
	s.mu.Lock()
	s.tools[name] = tool
	s.handlers[name] = handler
	s.mu.Unlock()
	return nil
}

func (s *LocalServer) UnregisterTool(name string) error {
	s.mu.Lock()
	delete(s.tools, name)
	delete(s.handlers, name)
	s.mu.Unlock()
	return nil
}

func (s *LocalServer) ListRegistered() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t)
	}
	return out
}

// Serve starts a stdio JSON-RPC MCP server. Blocks until ctx cancel or stdin EOF.
func (s *LocalServer) Serve(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("nil server")
	}
	if !s.serving.CompareAndSwap(false, true) {
		return fmt.Errorf("already serving")
	}
	defer s.serving.Store(false)

	in := s.stdin
	out := s.stdout
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	defer cancel()

	reader := bufio.NewReader(in)
	var writeMu sync.Mutex
	write := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err = out.Write(append(b, '\n'))
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		line = bytesTrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if err := s.handleLine(ctx, line, write); err != nil {
			// Continue serving unless context cancelled.
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

func bytesTrimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 {
		c := b[len(b)-1]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

func (s *LocalServer) handleLine(ctx context.Context, line []byte, write func(any) error) error {
	var req jsonRPCRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return err
	}
	if req.Method == "" {
		return nil
	}
	// Notifications (no response).
	if req.ID == nil {
		return nil
	}
	switch req.Method {
	case "initialize":
		return write(jsonRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: mustJSON(map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "glider", "version": clientVersion},
			}),
		})
	case "tools/list":
		tools := s.ListRegistered()
		return write(jsonRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result:  mustJSON(map[string]any{"tools": tools}),
		})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		cr, err := s.CallLocal(ctx, p.Name, p.Arguments)
		if err != nil {
			return write(jsonRPCResponse{
				JSONRPC: jsonRPCVersion,
				ID:      req.ID,
				Error:   &jsonRPCError{Code: -32000, Message: err.Error()},
			})
		}
		return write(jsonRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: mustJSON(map[string]any{
				"content": []map[string]string{{"type": "text", "text": cr.Content}},
				"isError": cr.IsError,
			}),
		})
	case "ping":
		return write(jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: req.ID, Result: mustJSON(map[string]any{})})
	default:
		return write(jsonRPCResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method},
		})
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (s *LocalServer) Stop(context.Context) error {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
	return nil
}

// CallLocal invokes a registered tool (used by Glider runtime and Serve handlers).
func (s *LocalServer) CallLocal(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	s.mu.RLock()
	h := s.handlers[name]
	s.mu.RUnlock()
	if h == nil {
		return CallResult{}, fmt.Errorf("unknown tool %q", name)
	}
	return h(ctx, name, args)
}

var _ ServerAdapter = (*LocalServer)(nil)
