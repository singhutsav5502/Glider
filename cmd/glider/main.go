package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/cloud"
	"github.com/glider-ai/glider/internal/backend/ollama"
	"github.com/glider-ai/glider/internal/backend/vllm"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/orchestrator"
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

	defaultBackend := "ollama"
	defaultModel := ""
	if len(cfg.Models) > 0 {
		defaultBackend = cfg.Models[0].Backend
		defaultModel = cfg.Models[0].Name
	}

	completer := &orchestrator.PassthroughCompleter{
		Registry:    reg,
		BackendName: defaultBackend,
		Model:       defaultModel,
	}

	handlers := &api.Handlers{
		Completer: completer,
		Models:    orchestrator.RegistryModelLister{Registry: reg},
	}

	addr := fmt.Sprintf(":%d", cfg.Server.ProxyPort)
	srv := api.NewServer(addr, handlers)
	if err := srv.Start(); err != nil {
		log.Error("proxy start failed", "err", err)
		os.Exit(1)
	}
	log.Info("glider proxy listening", "addr", srv.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	_ = srv.Shutdown(context.Background())
}

func registerBackends(reg *backend.Registry, cfg *config.Config, log *slog.Logger) {
	for _, b := range cfg.Backends {
		switch b.Type {
		case "local":
			switch b.Name {
			case "ollama":
				_ = reg.Register(ollama.New(b.URL))
			case "vllm":
				_ = reg.Register(vllm.New(b.URL))
			default:
				_ = reg.Register(ollama.New(b.URL))
			}
		case "cloud":
			// cloud providers registered from Cloud.Providers
		default:
			log.Warn("unknown backend type", "name", b.Name, "type", b.Type)
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
		_ = reg.Register(ollama.New("http://localhost:11434"))
	}
}
