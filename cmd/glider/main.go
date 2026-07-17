package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/transform"
	"github.com/glider-ai/glider/internal/vram"
)

func main() {
	cfgPath := flag.String("config", "configs/glider.yaml", "path to glider.yaml")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		log.Warn("config load failed, using defaults", "err", err)
		cfg = config.DefaultConfig()
	}
	provider := config.NewProvider(cfg, *cfgPath)
	if err := provider.StartWatcher(); err != nil {
		log.Warn("config watcher unavailable", "err", err)
	}

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

	bus := metrics.NewBus()
	collector := metrics.NewCollector(bus)

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
		Executor:    exec,
		Tokenizer:   tok,
		Transformer: transformer,
		MaxContext:  cfg.Thresholds.MaxLocalContextTokens,
		Metrics:     collector,
	}

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
	log.Info("glider proxy listening", "addr", proxy.Addr())

	var dash *dashboard.Server
	if cfg.Dashboard.Enabled {
		dashAddr := fmt.Sprintf(":%d", cfg.Server.DashboardPort)
		store := &dashboard.FileConfigStore{Provider: provider, Path: *cfgPath}
		models := &dashboard.RegistryModelController{Registry: reg}
		dash = dashboard.New(dashAddr, bus, store, models)
		if err := dash.Start(); err != nil {
			log.Warn("dashboard start failed", "err", err)
		} else {
			log.Info("glider dashboard listening", "addr", dash.Addr())
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	_ = proxy.Shutdown(context.Background())
	if dash != nil {
		_ = dash.Shutdown(context.Background())
	}
	provider.Stop()
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
