package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/cloud"
	"github.com/glider-ai/glider/internal/backend/ollama"
	"github.com/glider-ai/glider/internal/backend/vllm"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/dashboard"
	"github.com/glider-ai/glider/internal/loop"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/mcp"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/plugin"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/swarm"
	"github.com/glider-ai/glider/internal/tools"
	"github.com/glider-ai/glider/internal/transform"
	"github.com/glider-ai/glider/internal/vram"
	"github.com/google/uuid"
)

func main() {
	cfgPath := flag.String("config", "configs/glider.yaml", "path to glider.yaml")
	flag.Parse()

	levelVar := &slog.LevelVar{}
	levelVar.Set(slog.LevelInfo)
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar}))

	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		log.Warn("config load failed, using defaults", "err", err)
		cfg = config.DefaultConfig()
		config.ApplyMITMDebugEnv(cfg)
	}
	applyLogLevel(levelVar, cfg.Server.LogLevel)

	provider := config.NewProvider(cfg, *cfgPath)
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
	hotSwap := swarm.NewRegistry()
	hotSwap.RegisterLoopStages()
	_ = hotSwap.Register(&swarm.Module{
		Name: "fan_out",
		Kind: swarm.ModuleFanOut,
		Hot:  true,
		Apply: func(c *config.Config) error {
			next := orchestrator.FanOutConfigFromOrchestration(c.Orchestration, episodeStore, sessionID)
			next.Graph = fanOut.Config.Graph // preserve contextgraph wired after init
			fanOut.ApplyConfig(next)
			return nil
		},
	})

	hoopsDir := cfg.Orchestration.HoopsDir
	if hoopsDir == "" {
		hoopsDir = swarm.DefaultTemplatesDir()
	}
	tplStore := swarm.NewTemplateStore(hoopsDir)
	swarmRunner := &swarm.Runner{
		Opts:         swarm.OptionsFromConfig(cfg.Orchestration),
		Episodes:     episodeStore,
		Templates:    tplStore,
		SessionID:    sessionID,
		DefaultModel: "codellama:7b",
		Graph:        nil, // set after ctxGraph
	}
	swarmRunner.SetEnabled(cfg.Orchestration.Swarm.Enabled || cfg.Orchestration.FanOut.Enabled)
	_ = hotSwap.Register(&swarm.Module{
		Name: "swarm",
		Kind: swarm.ModuleSwarm,
		Hot:  true,
		Apply: func(c *config.Config) error {
			swarmRunner.ApplyOpts(swarm.OptionsFromConfig(c.Orchestration))
			swarmRunner.SetEnabled(c.Orchestration.Swarm.Enabled || c.Orchestration.FanOut.Enabled)
			return nil
		},
	})
	_ = hotSwap.Register(&swarm.Module{
		Name: "swarm_templates",
		Kind: swarm.ModuleSwarmTemplate,
		Hot:  true,
		Apply: func(c *config.Config) error {
			dir := c.Orchestration.HoopsDir
			if dir == "" {
				dir = swarm.DefaultTemplatesDir()
			}
			swarmRunner.Templates = swarm.NewTemplateStore(dir)
			return nil
		},
	})
	_ = hotSwap.Register(&swarm.Module{
		Name: "classifier",
		Kind: swarm.ModuleClassifier,
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
	retainDays := cfg.Context.RetainDays
	if retainDays <= 0 {
		retainDays = 14
	}
	if n, err := ctxGraph.PruneDisk(retainDays); err != nil {
		log.Warn("contextgraph prune failed", "err", err)
	} else if n > 0 {
		log.Info("contextgraph pruned old jsonl", "files", n, "retain_days", retainDays)
	}
	contextgraph.SetDefault(ctxGraph)
	fanCfg.Graph = ctxGraph
	fanOut.ApplyConfig(fanCfg)
	swarmRunner.Graph = dashboard.NewGraphSwarmSink(ctxGraph)
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

	swarmRunner.WorkerFn = swarm.CompleterWorkerFn(func(ctx context.Context, r *http.Request, prompt, model string) (string, error) {
		req := &backend.CompletionRequest{
			Model:  model,
			Stream: true,
			Messages: []backend.Message{
				{Role: "user", Content: prompt},
			},
		}
		ch, err := completer.Complete(r, req)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for chunk := range ch {
			b.WriteString(chunk.Content)
			if ctx.Err() != nil {
				return b.String(), ctx.Err()
			}
		}
		return b.String(), nil
	}, true)

	handlers := &api.Handlers{
		Completer: completer,
		Models:    orchestrator.RegistryModelLister{Registry: reg},
	}

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
			SurfaceLocalError: !cfg.MITM.OriginOnLocalErrorOrDefault(),
			FulfillHub:        fulfillHub,
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
				"note", "BidiAppend extractâ†’DecideLocalâ†’RunSSE text codec when correlated; else origin")
		}
		mitmProxy = &mitm.Proxy{
			Addr:      fmt.Sprintf(":%d", cfg.MITM.Port),
			Authority: auth,
			Hosts:     mitm.NewHostMatcher(cfg.MITM.Hosts),
			Local:     interceptor,
			Log:       log,
			Metrics:   collector,
			Debug:     mitmDebug,
		}
		if err := mitmProxy.Start(); err != nil {
			log.Error("mitm start failed", "err", err)
			os.Exit(1)
		}
		log.Info("glider MITM proxy listening", "addr", mitmProxy.ListenAddr(), "ca", certPath)
	}

	vramMon := vram.NewDefaultNvidiaSmiMonitor()
	stopVRAM := make(chan struct{})
	go pollVRAM(stopVRAM, vramMon, collector, reg, log)
	go pollBackendHealth(stopVRAM, reg, log)

	var dash *dashboard.Server
	var loopMgr *loop.Manager
	if cfg.Dashboard.Enabled {
		dashAddr := fmt.Sprintf(":%d", cfg.Server.DashboardPort)
		store := &dashboard.FileConfigStore{Provider: provider, Path: *cfgPath}
		models := &dashboard.RegistryModelController{Registry: reg}
		dash = dashboard.New(dashAddr, bus, store, models)
		dash.History = history
		dash.GPUs = gpuMonitorAdapter{mon: vramMon}
		dash.Metrics = collector
		dash.MITMDebug = mitmDebug
		dash.ContextGraph = ctxGraph
		dash.Episodes = episodeStore
		dash.ContextRetainDays = retainDays

		// Glider-owned loops: dashboard/API triggered; pure-local needs no Cursor.
		loopDir := cfg.Orchestration.Loops.Dir
		if loopDir == "" {
			loopDir = loop.DefaultDir()
		}
		defaultRoute := loop.RoutePref(strings.ToLower(strings.TrimSpace(cfg.Orchestration.Loops.DefaultRoute)))
		if defaultRoute == "" {
			defaultRoute = loop.RouteLocal
		}
		loopMgr = loop.NewManager(loop.NewStore(loopDir), completer, ctxGraph, loop.RunnerConfig{
			DefaultRoute: defaultRoute,
			Hoop: loop.HoopLearningConfig{
				Enabled:       cfg.Orchestration.Loops.HoopLearning.Enabled,
				LocalBiasStep: cfg.Orchestration.Loops.HoopLearning.LocalBiasStep,
				MaxBias:       cfg.Orchestration.Loops.HoopLearning.MaxBias,
				Window:        cfg.Orchestration.Loops.HoopLearning.Window,
			},
			OutcomeRing: 64,
		})
		loopMgr.Episodes = episodeStore
		agentLogs := agentlog.NewStore(256)
		agentLogs.OnAppend(func(e agentlog.Entry) {
			bus.Publish(metrics.Event{Type: metrics.EventAgentLog, Data: e})
		})
		loopMgr.Logs = agentLogs
		mcpMgr := mcp.NewManager()
		if err := mcpMgr.Configure(mcp.DefaultGitHubConfig()); err != nil {
			log.Warn("mcp configure", "err", err)
		}
		if _, err := mcpMgr.Connect(context.Background(), mcp.DefaultGitHubConfig()); err != nil {
			log.Warn("mcp github connect (set GITHUB_TOKEN / GITHUB_PERSONAL_ACCESS_TOKEN for live tools)", "err", err)
		}
		plugReg := plugin.NewMemRegistry(&plugin.SimpleHost{Root: "."})
		allowShell := cfg.Orchestration.Tools.AllowShell
		toolReg := tools.NewRegistry(tools.Options{
			Workspace:  ".",
			AllowShell: allowShell,
			ShellAllow: cfg.Orchestration.Tools.ShellAllowlist,
			Context:    contextgraph.ContextQuerier{Store: ctxGraph},
			MCP:        mcpMgr,
			Plugins:    plugReg,
		})
		loopMgr.Tools = toolReg
		loopMgr.BudgetCheck = func(st *loop.LoopState) bool {
			return st == nil || !st.Spend.HardHit
		}
		swarmRunner.Logs = agentLogs
		swarmRunner.Tools = toolReg
		dash.Loops = loopMgr
		dash.HotSwap = hotSwap
		dash.Swarm = swarmRunner
		dash.Templates = tplStore
		dash.AgentLogs = agentLogs
		dash.HoopsDir = hoopsDir
		if st, err := os.Stat("docs/site"); err == nil && st.IsDir() {
			dash.DocsDir = "docs/site"
			log.Info("docs available", "url", fmt.Sprintf("http://127.0.0.1:%d/docs/", cfg.Server.DashboardPort))
		}

		_ = hotSwap.Register(&swarm.Module{
			Name: "loop",
			Kind: swarm.ModuleLoop,
			Hot:  true,
			Apply: func(c *config.Config) error {
				loopMgr.Cfg.Hoop = loop.HoopLearningConfig{
					Enabled:       c.Orchestration.Loops.HoopLearning.Enabled,
					LocalBiasStep: c.Orchestration.Loops.HoopLearning.LocalBiasStep,
					MaxBias:       c.Orchestration.Loops.HoopLearning.MaxBias,
					Window:        c.Orchestration.Loops.HoopLearning.Window,
				}
				if dr := loop.RoutePref(strings.ToLower(strings.TrimSpace(c.Orchestration.Loops.DefaultRoute))); dr != "" {
					loopMgr.Cfg.DefaultRoute = dr
				}
				return nil
			},
		})

		if err := dash.Start(); err != nil {
			log.Warn("dashboard start failed", "err", err)
		} else {
			log.Info("glider dashboard listening", "addr", dash.Addr(),
				"loops_dir", loopDir, "loops_default_route", string(defaultRoute))
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	close(stopVRAM)
	_ = proxy.Shutdown(context.Background())
	if mitmProxy != nil {
		_ = mitmProxy.Shutdown(context.Background())
	}
	if dash != nil {
		_ = dash.Shutdown(context.Background())
	}
	if loopMgr != nil {
		loopMgr.Shutdown()
	}
	if history != nil {
		_ = history.Close()
	}
	provider.Stop()
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
	for _, b := range cfg.Backends {
		switch b.Name {
		case "ollama":
			_ = reg.Register(ollama.New(b.URL))
		case "vllm":
			_ = reg.Register(vllm.New(b.URL))
		default:
			if b.Type == "local" {
				_ = reg.Register(ollama.New(b.URL))
			}
		}
	}
	skipCloud := !cfg.Routing.AllowCloudFallbackOrDefault() &&
		strings.EqualFold(strings.TrimSpace(cfg.Routing.Default), "local")
	if skipCloud {
		log.Info("pure-local: skipping cloud provider registration (no API keys required)")
	} else {
		for _, p := range cfg.Cloud.Providers {
			key := os.Getenv(p.APIKeyEnv)
			switch p.Name {
			case "openai":
				_ = reg.Register(cloud.NewOpenAI(p.BaseURL, key))
			case "anthropic":
				_ = reg.Register(cloud.NewAnthropic(p.BaseURL, key))
			}
		}
	}
	if len(reg.List()) == 0 {
		log.Warn("no backends configured; registering default ollama")
		_ = reg.Register(ollama.New("http://127.0.0.1:11434"))
	}
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
