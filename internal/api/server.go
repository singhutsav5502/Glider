package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Server is the OpenAI-compatible API gateway.
type Server struct {
	addr     string
	handlers *Handlers
	httpSrv  *http.Server
}

func NewServer(addr string, h *Handlers) *Server {
	return &Server{addr: addr, handlers: h}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handlers.ChatCompletions)
	mux.HandleFunc("/v1/responses", s.handlers.Responses)
	mux.HandleFunc("/v1/models", s.handlers.ListModels)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	var h http.Handler = mux
	h = requestIDMiddleware(h)
	h = corsMiddleware(h)
	return h
}

func (s *Server) Start() error {
	s.httpSrv = &http.Server{
		Addr:              s.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	go func() { _ = s.httpSrv.Serve(ln) }()
	return nil
}

func (s *Server) Addr() string { return s.addr }

func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) URL() string {
	return fmt.Sprintf("http://%s", s.addr)
}
