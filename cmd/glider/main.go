package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/cloud"
	"github.com/glider-ai/glider/internal/backend/ollama"
	"github.com/glider-ai/glider/internal/backend/vllm"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/dashboard"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
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
		Registry:         reg,
		VRAM:             orchestrator.AdaptVRAM{Inner: vramMgr},
		IdleUnload:       idle,
		FailureThreshold: 5,
		BreakerCooldown:  30 * time.Second,
		CloudBackend:     "openai",
		CloudModel:       "gpt-4o",
		IsHealthy:        orchestrator.DefaultHealthCheck(reg),
	})

	transformer := transform.NewTransformer(cfg.Transform, tok)
	completer := &orchestrator.PipelineCompleter{
		Router: &liveRouter{get: func() router.Router {
			return enginePtr.Load()
		}},
		Executor:     exec,
		Tokenizer:    tok,
		Transformer:  transformer,
		MaxContext:   cfg.Thresholds.MaxLocalContextTokens,
		Metrics:      collector,
		ModelAliases: cfg.ModelAliases,
	}
	provider.Watch(func(c *config.Config) {
		completer.ModelAliases = c.ModelAliases
		completer.MaxContext = c.Thresholds.MaxLocalContextTokens
	})

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
			Harness: completer,
			Metrics: collector,
			Log:     log,
		}
		mitmProxy = &mitm.Proxy{
			Addr:      fmt.Sprintf(":%d", cfg.MITM.Port),
			Authority: auth,
			Hosts:     mitm.NewHostMatcher(cfg.MITM.Hosts),
			Local:     interceptor,
			Log:       log,
			Metrics:   collector,
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

	var dash *dashboard.Server
	if cfg.Dashboard.Enabled {
		dashAddr := fmt.Sprintf(":%d", cfg.Server.DashboardPort)
		store := &dashboard.FileConfigStore{Provider: provider, Path: *cfgPath}
		models := &dashboard.RegistryModelController{Registry: reg}
		dash = dashboard.New(dashAddr, bus, store, models)
		dash.History = history
		dash.GPUs = gpuMonitorAdapter{mon: vramMon}
		if err := dash.Start(); err != nil {
			log.Warn("dashboard start failed", "err", err)
		} else {
			log.Info("glider dashboard listening", "addr", dash.Addr())
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
	for _, p := range cfg.Cloud.Providers {
		key := os.Getenv(p.APIKeyEnv)
		switch p.Name {
		case "openai":
			_ = reg.Register(cloud.NewOpenAI(p.BaseURL, key))
		case "anthropic":
			_ = reg.Register(cloud.NewAnthropic(p.BaseURL, key))
		}
	}
	if len(reg.List()) == 0 {
		log.Warn("no backends configured; registering default ollama")
		_ = reg.Register(ollama.New("http://localhost:11434"))
	}
}
