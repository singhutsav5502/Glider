package dashboard

import (
	"context"
	"embed"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/hotswap"
	"github.com/glider-ai/glider/internal/mcp"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/gorilla/websocket"
)

//go:embed all:static
var staticFS embed.FS

type Server struct {
	addr    string
	http    *http.Server
	Bus     *metrics.Bus
	Config  ConfigStore
	Models  ModelController
	History *metrics.HistoryStore
	GPUs    GPUInfoProvider
	Metrics *metrics.Collector
	// MITMDebug is optional Path B R&D observer (GET /api/mitm/debug/recent).
	MITMDebug MITMDebugSource
	// Redirector is optional live PID-scoping control for the transparent
	// interceptor (POST /api/mitm/enrollment) — nil when transparent mode
	// isn't running, or when the platform's redirector doesn't implement
	// PIDScoper. See mitm.PIDScoper's doc comment for why this exists.
	Redirector mitm.PIDScoper
	// ContextGraph is optional turn-family event log (GET /api/context/turns/{id}).
	ContextGraph ContextGraphSource
	// Episodes is optional session episode ring (GET /api/context/episodes).
	Episodes *contextkit.Store
	// ContextRetainDays used by POST /api/context/prune (0 → 14).
	ContextRetainDays int
	// Workspace is the tools sandbox root for GET /api/workspace (empty → tools.DefaultWorkspaceDir()).
	Workspace string
	// HotSwap is optional module registry (GET /api/hotswap/modules).
	HotSwap *hotswap.Registry
	// AgentLogs is per-run activity rings (NOT a global mixed log).
	AgentLogs *agentlog.Store
	// DocsDir optional static docs root (e.g. docs/site). Served at /docs/.
	DocsDir string
	// MCP is optional live MCP manager (GET /api/mcp/*, connect/disconnect/tools).
	MCP      *mcp.Manager
	upgrader websocket.Upgrader
}

// MITMDebugSource exposes recent agent RPC observations (implemented by mitm.AgentRPCDebugger).
type MITMDebugSource interface {
	Recent(limit int) []mitm.RPCObservation
	PathCounts() map[string]int
}

// ContextGraphSource exposes orchestrator context turn views.
type ContextGraphSource interface {
	Turn(id string) (contextgraph.TurnView, bool)
	RecentTurns(limit int) []contextgraph.TurnView
	RecentEvents(limit int) []contextgraph.Event
	Stats() contextgraph.StoreStats
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
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetMetrics(w, r)
	})
	mux.HandleFunc("/api/mitm/debug/recent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleMITMDebugRecent(w, r)
	})
	mux.HandleFunc("/api/mitm/enrollment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleSetMITMEnrollment(w, r)
	})
	mux.HandleFunc("/api/context/recent", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleContextRecent(w, r)
	})
	mux.HandleFunc("/api/context/turns", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleContextTurns(w, r)
	})
	mux.HandleFunc("/api/context/turns/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleContextTurn(w, r)
	})
	mux.HandleFunc("/api/context/episodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleContextEpisodes(w, r)
	})
	mux.HandleFunc("/api/context/export", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleContextExport(w, r)
	})
	mux.HandleFunc("/api/context/prune", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleContextPrune(w, r)
	})
	mux.HandleFunc("/api/hotswap/modules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleHotSwapList(w, r)
	})
	mux.HandleFunc("/api/hotswap/modules/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleHotSwapEnable(w, r)
	})
	mux.HandleFunc("/api/context/index-tree", s.handleContextIndexTree)
	mux.HandleFunc("/api/context/index-symbols", s.handleContextIndexSymbols)
	mux.HandleFunc("/api/context/communities", s.handleContextCommunities)
	mux.HandleFunc("/api/context/explain", s.handleContextExplain)
	mux.HandleFunc("/api/agent-logs", s.handleAgentLogs)
	mux.HandleFunc("/api/workspace", s.handleWorkspace)
	mux.HandleFunc("/api/mcp/servers", s.handleMCPServers)
	mux.HandleFunc("/api/mcp/servers/", s.handleMCPServer)
	mux.HandleFunc("/api/mcp/github", s.handleMCPGitHub)
	mux.HandleFunc("/api/mcp/github/", s.handleMCPGitHub)
	mux.HandleFunc("/api/vendors", s.handleVendors)
	mux.HandleFunc("/api/vendors/", s.handleVendors)
	mux.HandleFunc("/api/playground/parse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handlePlaygroundParse(w, r)
	})
	mux.HandleFunc("/api/router/explain", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleRouterExplain(w, r)
	})
	mux.HandleFunc("/api/router/lint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleRouterLint(w, r)
	})
	mux.HandleFunc("/oauth/callback", s.handleGitHubOAuthCallback)
	mux.HandleFunc("/ws", s.handleWS)

	if dir := strings.TrimSpace(s.DocsDir); dir != "" {
		docs := http.FileServer(http.Dir(dir))
		mux.Handle("/docs/", http.StripPrefix("/docs/", docs))
		mux.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/docs/", http.StatusFound)
		})
	}

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/docs") {
			http.NotFound(w, r)
			return
		}
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
