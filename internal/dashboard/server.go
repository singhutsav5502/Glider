package dashboard

import (
	"context"
	"embed"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/metrics"
	"github.com/gorilla/websocket"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	addr     string
	http     *http.Server
	Bus      *metrics.Bus
	Config   ConfigStore
	Models   ModelController
	History  *metrics.HistoryStore
	GPUs     GPUInfoProvider
	upgrader websocket.Upgrader
}

func New(addr string, bus *metrics.Bus, cfg ConfigStore, models ModelController) *Server {
	return &Server{
		addr:   addr,
		Bus:    bus,
		Config: cfg,
		Models: models,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleGetConfig(w, r)
		case http.MethodPut:
			s.handlePutConfig(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/validate", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodPost:
			s.handleValidate(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/vram", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetVRAM(w, r)
	})
	mux.HandleFunc("/api/gpu-assignments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handlePatchGPUAssignments(w, r)
	})
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetModels(w, r)
	})
	mux.HandleFunc("/api/models/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleModelAction(w, r)
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleListSessions(w, r)
	})
	mux.HandleFunc("/api/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSession(w, r)
	})
	mux.HandleFunc("/ws", s.handleWS)

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		} else if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "application/javascript")
		} else if strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Content-Type", "text/css")
		}
		fileServer.ServeHTTP(w, r)
	})
	return mux
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	if s.Bus == nil {
		return
	}
	ch := s.Bus.Subscribe(32)
	defer s.Bus.Unsubscribe(ch)
	for ev := range ch {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteJSON(ev); err != nil {
			return
		}
	}
}

func (s *Server) Start() error {
	s.http = &http.Server{
		Addr:              s.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	go func() { _ = s.http.Serve(ln) }()
	return nil
}

func (s *Server) Addr() string { return s.addr }

func (s *Server) URL() string { return "http://" + s.addr }

func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}
