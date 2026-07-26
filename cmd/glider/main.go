package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/cloud"
	"github.com/glider-ai/glider/internal/backend/ollama"
	"github.com/glider-ai/glider/internal/backend/vllm"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/cursorrpc"
	"github.com/glider-ai/glider/internal/dashboard"
	"github.com/glider-ai/glider/internal/hotswap"
	"github.com/glider-ai/glider/internal/mcp"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/plugin"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/tools"
	"github.com/glider-ai/glider/internal/transform"
	"github.com/glider-ai/glider/internal/tray"
	"github.com/glider-ai/glider/internal/vendors"
	"github.com/glider-ai/glider/internal/vram"
	"github.com/google/uuid"
)

// main parses the -config flag and hands control to the system tray:
// systray.Run (Windows) blocks the main goroutine running its own GUI
// message loop, so it needs to be the outermost call here — everything
// Glider actually does (server startup, all the way to graceful shutdown)
// lives in runGlider, started as a goroutine from the tray's onReady
// callback. Shutdown can be triggered two ways, both converging on the
// same cancellation: a real OS signal (Ctrl+C, SIGTERM — unchanged from
// before the tray existed), or the tray's own "Exit" menu item.
func main() {
	cfgPath := flag.String("config", "configs/glider.yaml", "path to glider.yaml")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	sigCtx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSig()
	go func() {
		<-sigCtx.Done()
		cancel()
	}()

	onReady := func() {
		tray.SetupMenu(cancel)
		go runGlider(ctx, *cfgPath)
	}
	tray.Run(onReady, func() {})
}

// runGlider is the entirety of what main() used to do directly, before
// the system tray needed to own the outermost blocking call — unchanged
// in behavior, just parameterized on cfgPath and taking its shutdown
// signal from ctx (unified with the tray's Exit click, see main above)
// instead of creating its own signal.NotifyContext internally.
func runGlider(ctx context.Context, cfgPath string) {
	levelVar := &slog.LevelVar{}
	levelVar.Set(slog.LevelInfo)
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar}))

	if loaded, err := config.LoadDotEnvFiles(); err != nil {
		log.Warn("dotenv load", "err", err)
	} else if len(loaded) > 0 {
		log.Info("loaded env file(s)", "files", loaded)
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Warn("config load failed, using defaults", "err", err)
		cfg = config.DefaultConfig()
		config.ApplyMITMDebugEnv(cfg)
	}
	applyLogLevel(levelVar, cfg.Server.LogLevel)

	provider := config.NewProvider(cfg, cfgPath)
	if err := provider.StartWatcher(); err != nil {
		log.Warn("config watcher unavailable", "err", err)
	}
	provider.Watch(func(c *config.Config) {
		applyLogLevel(levelVar, c.Server.LogLevel)
		log.Info("log level reloaded", "level", c.Server.LogLevel)
	})

	reg := backend.NewRegistry()
	registerBackends(reg, cfg, log)
	pingBackends(context.Background(), reg, log, true)
	for _, m := range cfg.Models {
		_ = reg.RegisterModel(backend.ModelInfo{
			Name:           m.Name,
			Backend:        m.Backend,
			VRAMEstimateMB: m.VRAMEstimateMB,
			MaxContext:     m.MaxContext,
			Capabilities:   m.Capabilities,
			Adapter:        m.Adapter,
			KeepWarm:       m.KeepWarm,
		})
	}

	tok, err := transform.NewTokenizer()
	if err != nil {
		log.Error("tokenizer init failed", "err", err)
		os.Exit(1)
	}
	starExec := router.NewStarlarkExecutor()
	engine, err := router.NewEngineFromConfig(cfg.Routing, starExec)
	if err != nil {
		log.Error("router init failed", "err", err)
		os.Exit(1)
	}
	var enginePtr atomic.Pointer[router.Engine]
	enginePtr.Store(engine)
	provider.Watch(func(c *config.Config) {
		if next, err := router.NewEngineFromConfig(c.Routing, starExec); err == nil {
			enginePtr.Store(next)
			log.Info("router reloaded from config")
		} else {
			log.Error("router reload failed", "err", err)
		}
	})

	idle, _ := time.ParseDuration(cfg.Thresholds.IdleUnloadTimeout)
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	vramMgr := vram.NewManager(vram.ManagerConfig{
		TotalBytes:    16 * 1024 * 1024 * 1024,
		FreeBytes:     16 * 1024 * 1024 * 1024,
		HeadroomBytes: int64(cfg.VRAM.HeadroomMB) * 1024 * 1024,
		Strategy:      vram.AllocationStrategy(cfg.VRAM.Strategy),
	})

	sessionID := "run-" + uuid.NewString()
	history, histErr := metrics.OpenHistoryStore(metrics.DefaultHistoryDir(), sessionID)
	if histErr != nil {
		log.Warn("history store unavailable", "err", histErr)
	}

	bus := metrics.NewBus()
	collector := metrics.NewCollector(bus)
	if history != nil {
		collector.SetHistory(history)
		log.Info("session history started", "session_id", sessionID, "dir", metrics.DefaultHistoryDir())
	}

	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:             reg,
		VRAM:                 orchestrator.AdaptVRAM{Inner: vramMgr},
		IdleUnload:           idle,
		FailureThreshold:     5,
		BreakerCooldown:      30 * time.Second,
		CloudBackend:         "openai",
		CloudModel:           "gpt-4o",
		DisableCloudFallback: !cfg.Routing.AllowCloudFallbackOrDefault(),
		IsHealthy:            orchestrator.DefaultHealthCheck(reg),
	})
	if !cfg.Routing.AllowCloudFallbackOrDefault() {
		log.Info("pure-local: cloud fallback after local disabled")
	}
	episodeStore := contextkit.NewStore(32)
	fanCfg := orchestrator.FanOutConfigFromOrchestration(cfg.Orchestration, episodeStore, sessionID)
	fanOut := &orchestrator.FanOutExecutor{Inner: exec, Config: fanCfg}
	fanOut.ApplyConfig(fanCfg)
	var executor orchestrator.Executor = fanOut
	if fanCfg.Enabled {
		log.Info("fan_out executor enabled", "max_workers", fanCfg.MaxWorkers, "result_chan", fanCfg.ResultChanSize)
	}
	hotSwap := hotswap.NewRegistry()
	_ = hotSwap.Register(&hotswap.Module{
		Name: "fan_out",
		Kind: hotswap.ModuleFanOut,
		Hot:  true,
		Apply: func(c *config.Config) error {
			next := orchestrator.FanOutConfigFromOrchestration(c.Orchestration, episodeStore, sessionID)
			next.Graph = fanOut.Config.Graph // preserve contextgraph wired after init
			fanOut.ApplyConfig(next)
			return nil
		},
	})

	backendReloader := &backend.Reloader{
		Registry:    reg,
		WarmPing:    true,
		PingTimeout: 2 * time.Second,
		Log:         log,
		Build:       buildBackendSnapshot,
		AfterSwap: func(c *config.Config) {
			exec.Fallback().SetDisableCloudFallback(!c.Routing.AllowCloudFallbackOrDefault())
		},
	}
	_ = hotSwap.Register(&hotswap.Module{
		Name:        "backends",
		Kind:        hotswap.ModuleBackend,
		Hot:         true,
		Description: "Ollama/vLLM/cloud clients + models; in-flight Complete keeps old client until cycle ends",
		Apply:       backendReloader.Apply,
		Status: func() (ok bool, errMsg string, warnings []string, at time.Time, attempted bool) {
			st := backendReloader.Status()
			return st.OK, st.Error, st.Warnings, st.At, st.Attempted
		},
	})
	_ = hotSwap.Register(&hotswap.Module{
		Name: "classifier",
		Kind: hotswap.ModuleClassifier,
		Hot:  true,
		Apply: func(c *config.Config) error {
			// Router rebuild happens via existing provider.Watch in main; this
			// module tracks generation for the dashboard enable/disable UI.
			return nil
		},
	})
	hotSwap.BindProvider(provider)

	transformer := transform.NewTransformer(cfg.Transform, tok)
	ctxGraph := contextgraph.New(contextgraph.DefaultDir())
	if cfg.Context.MaxEvents > 0 {
		ctxGraph.Max = cfg.Context.MaxEvents
	}
	warmDays := cfg.Context.WarmLoadDays
	if warmDays <= 0 {
		warmDays = 2
	}
	if n, err := ctxGraph.LoadWarm(warmDays); err != nil {
		log.Warn("contextgraph warm load failed", "err", err)
	} else if n > 0 {
		log.Info("contextgraph warm-loaded", "events", n, "days", warmDays)
	}
	if n, err := ctxGraph.LoadEntities(); err != nil {
		log.Warn("contextgraph entity load failed", "err", err)
	} else if n > 0 {
		log.Info("contextgraph entities loaded", "lines", n)
	}
	retainDays := cfg.Context.RetainDays
	if retainDays <= 0 {
		retainDays = 14
	}
	if n, err := ctxGraph.PruneDisk(retainDays); err != nil {
		log.Warn("contextgraph prune failed", "err", err)
	} else if n > 0 {
		log.Info("contextgraph pruned old jsonl", "files", n, "retain_days", retainDays)
	}
	fanCfg.Graph = ctxGraph
	fanOut.ApplyConfig(fanCfg)
	completer := &orchestrator.PipelineCompleter{
		Router: &liveRouter{get: func() router.Router {
			return enginePtr.Load()
		}},
		Executor:       executor,
		Tokenizer:      tok,
		Transformer:    transformer,
		TransformCfg:   cfg.Transform,
		MaxContext:     cfg.Thresholds.MaxLocalContextTokens,
		Metrics:        collector,
		ModelAliases:   cfg.ModelAliases,
		Graph:          ctxGraph,
		Episodes:       episodeStore,
		EpisodeSession: sessionID,
	}
	provider.Watch(func(c *config.Config) {
		completer.ModelAliases = c.ModelAliases
		completer.MaxContext = c.Thresholds.MaxLocalContextTokens
		completer.TransformCfg = c.Transform
		completer.Transformer = transform.NewTransformer(c.Transform, tok)
	})

	handlers := &api.Handlers{
		Completer: completer,
		Models:    orchestrator.RegistryModelLister{Registry: reg},
	}

	seedDefaultWorkspace(log)

	proxyAddr := fmt.Sprintf(":%d", cfg.Server.ProxyPort)
	proxy := api.NewServer(proxyAddr, handlers)
	if err := proxy.Start(); err != nil {
		log.Error("proxy start failed", "err", err)
		os.Exit(1)
	}
	log.Info("glider gateway listening", "addr", proxy.Addr())

	var mitmProxy *mitm.Proxy
	var mitmDebug *mitm.AgentRPCDebugger
	fulfillHub := mitm.NewAgentFulfillHub()
	fulfillHub.Graph = ctxGraph
	if cfg.MITM.Enabled {
		certPath := mitm.ExpandPath(cfg.MITM.CACert)
		keyPath := mitm.ExpandPath(cfg.MITM.CAKey)
		if certPath == "" || keyPath == "" {
			certPath, keyPath = mitm.DefaultCAPaths()
		}
		auth, err := mitm.LoadOrCreateAuthority(certPath, keyPath)
		if err != nil {
			log.Error("mitm CA init failed", "err", err)
			os.Exit(1)
		}
		interceptor := &mitm.Interceptor{
			Harness:           completer,
			Metrics:           collector,
			Log:               log,
			AgentRPCFulfill:   cfg.MITM.AgentRPCFulfill,
			CannedOnError:     cfg.MITM.AgentRPCCannedOnError,
			CannedText:        cfg.MITM.AgentRPCCannedText,
			AgentRPCToolCodec: cfg.MITM.AgentRPCToolCodec,
			SurfaceLocalError: !cfg.MITM.OriginOnLocalErrorOrDefault(),
			FulfillHub:        fulfillHub,
		}
		if cfg.MITM.AgentRPCToolCodec {
			cursorrpc.SetRunSSEToolCodecEnabled(true)
		}
		if cfg.MITM.RequireLocalHealthy {
			interceptor.LocalHealthy = func() bool {
				b, err := reg.Get("ollama")
				if err != nil {
					return false
				}
				hc, ok := b.(backend.HealthChecker)
				if !ok {
					return true
				}
				if hc.IsHealthy() {
					return true
				}
				_ = hc.Ping(context.Background())
				return hc.IsHealthy()
			}
		}
		interceptor.ApplyRoutingPolicy(cfg.Routing)
		provider.Watch(func(c *config.Config) {
			interceptor.ApplyRoutingPolicy(c.Routing)
		})
		if cfg.MITM.DebugAgentRPC {
			dumpDir := cfg.MITM.DebugDumpDir
			if dumpDir == "" {
				dumpDir = mitm.DefaultDebugDumpDir()
			}
			mitmDebug = &mitm.AgentRPCDebugger{
				Enabled:  true,
				DumpDir:  dumpDir,
				Log:      log,
				Metrics:  collector,
				RingSize: 128,
			}
			interceptor.Debug = mitmDebug
			log.Info("mitm agent rpc debug enabled", "dump_dir", mitm.ExpandPath(dumpDir))
		}
		if cfg.MITM.AgentRPCFulfill {
			log.Info("mitm agent rpc fulfill experimental enabled",
				"canned_on_error", cfg.MITM.AgentRPCCannedOnError,
				"tool_codec", cfg.MITM.AgentRPCToolCodec,
				"note", "BidiAppend extract -> DecideLocal -> RunSSE text codec when correlated; child tool RunSSE local only when tool_codec on")
		}
		// DelegateHandler goes first: it only claims /v1/messages requests
		// carrying an explicit "/vendor-name <prompt>" flag (same convention
		// as /local /cloud), so it never interferes with interceptor's
		// existing Cursor-focused logic — anything it doesn't claim falls
		// straight through.
		localHandler := &mitm.ChainHandler{Handlers: []mitm.LocalHandler{
			&mitm.DelegateHandler{Log: log},
			interceptor,
		}}
		mitmProxy = &mitm.Proxy{
			Addr:      fmt.Sprintf(":%d", cfg.MITM.Port),
			Authority: auth,
			Hosts:     mitm.NewHostMatcher(cfg.MITM.Hosts),
			Local:     localHandler,
			Log:       log,
			Metrics:   collector,
			Debug:     mitmDebug,
		}
		if cfg.MITM.Transparent {
			if cfg.MITM.WinDivertDLLPath == "" {
				log.Warn("mitm.transparent is true but windivert_dll_path is empty; transparent interception stays off")
			} else if cfg.MITM.TransparentPort == 0 {
				log.Warn("mitm.transparent is true but transparent_port is 0; transparent interception stays off")
			} else {
				mitmProxy.Redirector = mitm.NewRedirector(mitm.ExpandPath(cfg.MITM.WinDivertDLLPath), log)
				mitmProxy.TransparentPort = cfg.MITM.TransparentPort
				mitmProxy.TransparentPorts = cfg.MITM.TransparentPorts
				mitmProxy.TransparentAllowProcessNames = vendorProcessNames(log)
			}
		}
		if err := mitmProxy.Start(); err != nil {
			log.Error("mitm start failed", "err", err)
			os.Exit(1)
		}
		log.Info("glider MITM proxy listening", "addr", mitmProxy.ListenAddr(), "ca", certPath)
		if mitmProxy.Redirector != nil {
			log.Info("glider transparent OS-level interception active", "port", cfg.MITM.TransparentPort, "match_ports", cfg.MITM.TransparentPorts)
		}
	}

	vramMon := vram.NewDefaultNvidiaSmiMonitor()
	stopVRAM := make(chan struct{})
	go pollVRAM(stopVRAM, vramMon, collector, reg, log)
	go pollBackendHealth(stopVRAM, reg, log)

	var dash *dashboard.Server
	if cfg.Dashboard.Enabled {
		dashAddr := fmt.Sprintf(":%d", cfg.Server.DashboardPort)
		store := &dashboard.FileConfigStore{Provider: provider, Path: cfgPath}
		models := &dashboard.RegistryModelController{Registry: reg}
		dash = dashboard.New(dashAddr, bus, store, models)
		dash.History = history
		dash.GPUs = gpuMonitorAdapter{mon: vramMon}
		dash.Metrics = collector
		dash.MITMDebug = mitmDebug
		dash.ContextGraph = ctxGraph
		dash.Episodes = episodeStore
		dash.ContextRetainDays = retainDays

		agentLogs := agentlog.NewStore(256)
		agentLogs.OnAppend(func(e agentlog.Entry) {
			bus.Publish(metrics.Event{Type: metrics.EventAgentLog, Data: e})
		})
		mcpMgr := mcp.NewManager()
		if hydrated, err := mcp.HydrateGitHubTokenFromStore(); err != nil {
			log.Warn("mcp github credential hydrate", "err", err)
		} else if hydrated {
			log.Info("mcp github token loaded from ~/.glider/credentials/github_token")
		}
		if err := mcpMgr.Configure(mcp.DefaultGitHubConfig(), mcp.DefaultGitHubStdioConfig()); err != nil {
			log.Warn("mcp configure", "err", err)
		}
		if mcp.GitHubTokenPresent() {
			if _, err := mcpMgr.Connect(context.Background(), mcp.DefaultGitHubConfig()); err != nil {
				log.Warn("mcp github connect failed", "err", err)
			} else {
				log.Info("mcp github HTTP connected")
			}
		} else if mcp.ResolveGitHubOAuthClientID() != "" {
			log.Info("mcp github: OAuth client id loaded — open Dashboard MCP tab → Sign in with GitHub (no PAT yet)")
		} else {
			log.Info("mcp github: no token yet — Paste PAT or set GLIDER_GITHUB_OAUTH_CLIENT_ID in .env.local, then Sign in on MCP tab")
		}
		dash.MCP = mcpMgr
		plugReg := plugin.NewMemRegistry(&plugin.SimpleHost{Root: "."})
		allowShell := cfg.Orchestration.Tools.AllowShell
		workspace, err := tools.ResolveWorkspace(cfg.Orchestration.Tools.Workspace)
		if err != nil {
			log.Warn("tools workspace", "err", err)
			workspace = "."
		} else {
			log.Info("tools workspace", "path", workspace)
		}
		toolReg := tools.NewRegistry(tools.Options{
			Workspace:  workspace,
			AllowShell: allowShell,
			ShellAllow: cfg.Orchestration.Tools.ShellAllowlist,
			AllowHosts: cfg.Orchestration.Tools.AllowHosts,
			WebSearch: tools.WebSearchOptions{
				Provider:        cfg.Orchestration.Tools.WebSearch.Provider,
				MaxResults:      cfg.Orchestration.Tools.WebSearch.MaxResults,
				BraveAPIKeyEnv:  cfg.Orchestration.Tools.WebSearch.BraveAPIKeyEnv,
				TavilyAPIKeyEnv: cfg.Orchestration.Tools.WebSearch.TavilyAPIKeyEnv,
				SerpAPIKeyEnv:   cfg.Orchestration.Tools.WebSearch.SerpAPIKeyEnv,
				SearXNGURL:      cfg.Orchestration.Tools.WebSearch.SearXNGURL,
				FetchMaxBytes:   cfg.Orchestration.Tools.WebSearch.FetchMaxBytes,
			},
			Context: contextgraph.ContextQuerier{Store: ctxGraph},
			MCP:     mcpMgr,
			Plugins: plugReg,
		})
		fulfillHub.Tools = toolReg
		dash.HotSwap = hotSwap
		dash.Workspace = workspace
		dash.AgentLogs = agentLogs
		if st, err := os.Stat("docs/site"); err == nil && st.IsDir() {
			dash.DocsDir = "docs/site"
			log.Info("docs available", "url", fmt.Sprintf("http://127.0.0.1:%d/docs/", cfg.Server.DashboardPort))
		}

		if err := dash.Start(); err != nil {
			log.Warn("dashboard start failed", "err", err)
		} else {
			log.Info("glider dashboard listening", "addr", dash.Addr())
		}
	}

	<-ctx.Done()
	close(stopVRAM)
	_ = proxy.Shutdown(context.Background())
	if mitmProxy != nil {
		_ = mitmProxy.Shutdown(context.Background())
	}
	if dash != nil {
		_ = dash.Shutdown(context.Background())
	}
	if history != nil {
		_ = history.Close()
	}
	provider.Stop()
	tray.Quit()
}

type gpuMonitorAdapter struct {
	mon *vram.NvidiaSmiMonitor
}

func (g gpuMonitorAdapter) AllMemoryInfo() ([]vram.GPUMemoryInfo, error) {
	return g.mon.AllMemoryInfo()
}

func applyLogLevel(levelVar *slog.LevelVar, raw string) {
	levelVar.Set(parseLogLevel(raw))
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// seedDefaultWorkspace loads the persisted default workspace directory
// (set from the dashboard's Vendors page, internal/dashboard's
// setDefaultWorkspace) into the in-memory WorkspaceStore delegate calls
// actually consult — vendors.Registry is just the on-disk form of it,
// vendors.SetDefaultWorkspace is what ResolveDelegate reads at request
// time. A missing/unreadable registry means no default configured yet,
// not a startup failure.
func seedDefaultWorkspace(log *slog.Logger) {
	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		return
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil || reg.DefaultWorkspace == "" {
		return
	}
	vendors.SetDefaultWorkspace(reg.DefaultWorkspace)
	log.Info("glider default delegate workspace loaded", "dir", reg.DefaultWorkspace)
}

// vendorProcessNames derives the transparent redirector's process allowlist
// from whichever CLIs discovery has actually found and the dashboard has
// left enabled (internal/vendors) — same registry the delegate route
// (internal/api Messages handler) reads, never a hardcoded name here either.
// An empty/missing registry (discovery never run) means no process-based
// narrowing, not a startup failure — the IP/port filter alone still applies.
func vendorProcessNames(log *slog.Logger) []string {
	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		log.Warn("vendor registry path unavailable; transparent redirector has no process-based narrowing", "err", err)
		return nil
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil {
		log.Warn("vendor registry unreadable; transparent redirector has no process-based narrowing", "err", err)
		return nil
	}
	var names []string
	for _, v := range reg.Enabled() {
		names = append(names, filepath.Base(v.Path))
	}
	if len(names) == 0 {
		log.Warn("no enabled vendors in registry; transparent redirector has no process-based narrowing — run discovery from the dashboard's Vendors page first")
	}
	return names
}

func pollVRAM(stop <-chan struct{}, mon *vram.NvidiaSmiMonitor, collector *metrics.Collector, reg *backend.Registry, _ *slog.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	publish := func() {
		infos, err := mon.AllMemoryInfo()
		if err != nil {
			return
		}
		if len(infos) == 0 {
			return
		}
		info := infos[0]
		rows := []metrics.VRAMModelRow{}
		if reg != nil {
			for _, m := range reg.ListModels() {
				rows = append(rows, metrics.VRAMModelRow{
					Name:    m.Name,
					VRAM:    int64(m.VRAMEstimateMB) * 1024 * 1024,
					State:   string(m.State),
					Backend: m.Backend,
				})
			}
		}
		collector.PublishVRAM(metrics.VRAMEventData{
			Total:  info.Total,
			Used:   info.Used,
			Free:   info.Free,
			Models: rows,
		})
	}
	publish()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			publish()
		}
	}
}

// liveRouter re-reads the engine pointer after hot-reload.
type liveRouter struct {
	get func() router.Router
}

func (l *liveRouter) Route(ctx context.Context, req *backend.CompletionRequest) (*backend.RoutingDecision, error) {
	return l.get().Route(ctx, req)
}

func registerBackends(reg *backend.Registry, cfg *config.Config, log *slog.Logger) {
	snap, err := buildBackendSnapshot(cfg)
	if err != nil {
		log.Warn("backend snapshot build failed", "err", err)
		return
	}
	for _, w := range snap.Warnings {
		log.Warn("backend build", "warning", w)
	}
	for name, b := range snap.Backends {
		if err := reg.Register(b); err != nil {
			log.Warn("backend register skipped", "backend", name, "err", err)
		}
	}
}

// buildBackendSnapshot constructs local + cloud clients from config without mutating a Registry.
// Used at startup and by backend.Reloader for live hot-reload.
func buildBackendSnapshot(cfg *config.Config) (*backend.Snapshot, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}
	reqTimeout := parseRequestTimeout(cfg)
	backends := make(map[string]backend.InferenceBackend)
	var warnings []string

	for _, b := range cfg.Backends {
		name := strings.TrimSpace(b.Name)
		if name == "" {
			warnings = append(warnings, "skipping backend with empty name")
			continue
		}
		url := strings.TrimSpace(b.URL)
		if url == "" {
			return nil, fmt.Errorf("backend %q: url is required", name)
		}
		switch name {
		case "ollama":
			backends[name] = ollama.NewWithTimeout(url, reqTimeout)
		case "vllm":
			backends[name] = vllm.NewWithTimeout(url, reqTimeout)
		default:
			if b.Type == "local" {
				backends[name] = ollama.NewWithTimeout(url, reqTimeout)
			} else {
				warnings = append(warnings, fmt.Sprintf("skipping unknown backend %q (type=%q)", name, b.Type))
			}
		}
	}

	skipCloud := !cfg.Routing.AllowCloudFallbackOrDefault() &&
		strings.EqualFold(strings.TrimSpace(cfg.Routing.Default), "local")
	if !skipCloud {
		for _, p := range cfg.Cloud.Providers {
			key := os.Getenv(p.APIKeyEnv)
			switch p.Name {
			case "openai":
				backends[p.Name] = cloud.NewOpenAI(p.BaseURL, key)
			case "anthropic":
				backends[p.Name] = cloud.NewAnthropic(p.BaseURL, key)
			}
		}
	} else {
		warnings = append(warnings, "pure-local: skipping cloud provider registration")
	}

	if len(backends) == 0 {
		warnings = append(warnings, "no backends configured; using default ollama")
		backends["ollama"] = ollama.NewWithTimeout("http://127.0.0.1:11434", reqTimeout)
	}

	models := make([]backend.ModelInfo, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		models = append(models, backend.ModelInfo{
			Name:           m.Name,
			Backend:        m.Backend,
			VRAMEstimateMB: m.VRAMEstimateMB,
			MaxContext:     m.MaxContext,
			Capabilities:   m.Capabilities,
			Adapter:        m.Adapter,
			KeepWarm:       m.KeepWarm,
		})
	}

	return &backend.Snapshot{
		Backends: backends,
		Models:   models,
		Warnings: warnings,
	}, nil
}

// parseRequestTimeout reads thresholds.request_timeout for local HTTP clients (default 10m).
func parseRequestTimeout(cfg *config.Config) time.Duration {
	const fallback = 10 * time.Minute
	if cfg == nil {
		return fallback
	}
	s := strings.TrimSpace(cfg.Thresholds.RequestTimeout)
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func pingBackends(ctx context.Context, reg *backend.Registry, log *slog.Logger, verbose bool) {
	if reg == nil {
		return
	}
	for _, b := range reg.List() {
		hc, ok := b.(backend.HealthChecker)
		if !ok {
			continue
		}
		if err := hc.Ping(ctx); err != nil {
			log.Warn("backend unhealthy", "backend", b.Name(), "err", err)
			continue
		}
		if verbose {
			log.Info("backend healthy", "backend", b.Name())
		}
	}
}

func pollBackendHealth(stop <-chan struct{}, reg *backend.Registry, log *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			pingBackends(context.Background(), reg, log, false)
		}
	}
}
