package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// LocalServer is a minimal ServerAdapter that registers tools in-process
// (HTTP/stdio serve is TODO — handlers are invocable via CallLocal).
type LocalServer struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	handlers map[string]ToolHandler
}

// NewLocalServer constructs an empty adapter.
func NewLocalServer() *LocalServer {
	return &LocalServer{
		tools:    make(map[string]Tool),
		handlers: make(map[string]ToolHandler),
	}
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

func (s *LocalServer) Serve(ctx context.Context) error {
	// TODO: stdio JSON-RPC / HTTP MCP transport for external hosts.
	<-ctx.Done()
	return ctx.Err()
}

func (s *LocalServer) Stop(context.Context) error { return nil }

// CallLocal invokes a registered tool (used by Glider runtime before Serve exists).
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
