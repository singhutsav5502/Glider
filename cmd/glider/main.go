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
	"github.com/glider-ai/glider/internal/runstate"
	"github.com/glider-ai/glider/internal/safego"
	"github.com/glider-ai/glider/internal/summarizer"
	"github.com/glider-ai/glider/internal/tools"
	"github.com/glider-ai/glider/internal/transform"
	"github.com/glider-ai/glider/internal/tray"
	"github.com/glider-ai/glider/internal/vendors"
	"github.com/glider-ai/glider/internal/vram"
	"github.com/glider-ai/glider/internal/webviewshell"
	"github.com/google/uuid"
)

// main reads the -config flag and then gives control to the system tray.
//
// systray.Run, on Windows, blocks the main goroutine and runs its own message
// loop for the graphic interface. Therefore it must be the outermost call
// here.
//
// Each operation of Glider is in runGlider, from the start of the servers to
// the clean shutdown. The onReady callback of the tray starts runGlider as a
// goroutine.
//
// Two events can start a shutdown, and both use the same cancellation. The
// first is a true signal from the operating system, which is Ctrl+C or
// SIGTERM. That signal did not change when the tray arrived. The second is
// the "Exit" item in the menu of the tray.
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

	// runGlider reads the config again, in full. This read is only sufficient to
	// know the port of the dashboard before the menu of the tray exists. Therefore
	// "Open Dashboard" has an address from the first click.
	//
	// To read the file two times is cheap and safe. seedDefaultWorkspace and
	// seedResponseDetail already use the same pattern for their own independent
	// reads.
	dashboardPort := 8081
	if cfg, err := config.LoadConfig(*cfgPath); err == nil && cfg.Server.DashboardPort != 0 {
		dashboardPort = cfg.Server.DashboardPort
	}
	dashboardURL := fmt.Sprintf("http://127.0.0.1:%d", dashboardPort)

	onReady := func() {
		tray.SetupMenu(func() {
			if err := webviewshell.Show(dashboardURL); err != nil {
				slog.Default().Warn("open dashboard failed", "err", err)
			}
		}, cancel)
		go runGlider(ctx, *cfgPath)
	}
	tray.Run(onReady, func() {})
}

// runGlider does all the work that main() did directly, before the system tray
// needed the outermost call that blocks.
//
// Its behaviour does not change. It only takes cfgPath as a parameter, and it
// takes its signal to stop from ctx. That ctx also carries the Exit click of the
// tray, and main above gives the detail. Before, this code made its own context
// with signal.NotifyContext.
func runGlider(ctx context.Context, cfgPath string) {
	levelVar := &slog.LevelVar{}
	levelVar.Set(slog.LevelInfo)
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar}))

	// Refer to the comment on internal/runstate. A failure or a forceful stop,
	// from taskkill /F or from SIGKILL, runs no Go code. Therefore the only
	// position that can show that event is here, at the next start, after the
	// event.
	//
	// This is a true incident, and a live test confirmed it on 2026-07-30. An
	// instance that a person stopped left a delegate subprocess with no parent.
	// No code was left to see it.
	//
	// This test operates before MarkStarted writes over the evidence.
	if runstate.WasUncleanShutdown() {
		log.Warn("previous glider instance did not shut down cleanly (crashed, or was forcefully killed) — " +
			"if it was running transparent interception, verify no orphaned vendor-CLI subprocess or stale " +
			"redirect rule is still active (tasklist/netstat, or iptables -t nat -L on Linux) before relying on this run")
	}
	if err := runstate.MarkStarted(); err != nil {
		log.Debug("runstate: could not write startup marker", "err", err)
	}
	// Remove per-delegate context directories a previous run left behind.
	// Safe here specifically: a live delegate can only exist inside a
	// running Glider, and no delegate has started yet.
	if err := vendors.SweepDelegateContextDirs(); err != nil {
		log.Debug("could not sweep stale delegate context dirs", "err", err)
	}

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

	// Continuity summarization. Glider always compacts the continuity record of
	// a workspace. This code decides two things. First, does a model write the
	// summary, or does the method with no model write it? Second, in what
	// sequence of preference?
	//
	// "origin" is first by default. The user already pays for the agent CLIs.
	// Therefore the bookkeeping of Glider must not spend the separate API
	// credits of that user.
	//
	// That source runs the CLI as a usual delegate with no console. **It never
	// uses again the credentials that Glider saw in intercepted traffic.**
	// planning/glider_high_level_design.md §8 gives that pattern as a risk with
	// the terms of service.
	vendors.SetBackgroundLimits(
		cfg.Context.Background.TokenBudget,
		cfg.Context.Background.MaxEntries,
		cfg.Context.Background.MaxEntryChars,
	)
	configureContinuitySummarizers(reg, cfg, log)

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
	// Seed the allocator from the true device, and never from a constant.
	//
	// This read a fixed 16 GiB until 2026-08-13. On a 4 GiB card every
	// Reserve then succeeded, the model went to Ollama, and Ollama answered
	// with a CUDA out-of-memory error after about 35 seconds. The gateway
	// gave a 502 that named the wrong cause: "ollama warm: ... status 500".
	// The allocator was not wrong about arithmetic. It was given a number
	// that no code had measured.
	//
	// A failure here is ordinary and is not an error. nvidia-smi is absent
	// on plenty of ordinary Windows and Linux machines: an AMD or Intel
	// card, integrated graphics alone, a laptop where the discrete GPU is
	// switched off, or a headless Linux box with no GPU at all. Each of
	// those still runs Glider, and each of those still routes to Ollama on
	// the CPU.
	//
	// The manager is then unmetered: it keeps its record of what is loaded
	// and permits every reservation. Refer to vram.ManagerConfig.Unmetered
	// for why "I do not know" is better than any invented number, in either
	// direction.
	vramCfg := vram.ManagerConfig{
		HeadroomBytes: int64(cfg.VRAM.HeadroomMB) * 1024 * 1024,
		Strategy:      vram.AllocationStrategy(cfg.VRAM.Strategy),
	}
	if info, err := vram.NewDefaultNvidiaSmiMonitor().GetMemoryInfo(0); err == nil {
		vramCfg.TotalBytes = info.Total
		vramCfg.UsedBytes = info.Used
		vramCfg.FreeBytes = info.Free
		log.Info("vram measured",
			"gpu", 0,
			"total_mb", info.Total/(1024*1024),
			"free_mb", info.Free/(1024*1024),
			"headroom_mb", cfg.VRAM.HeadroomMB)
	} else {
		vramCfg.Unmetered = true
		log.Info("vram not measured — the allocator will not limit local models", "err", err)
	}
	vramMgr := vram.NewManager(vramCfg)

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
		Metrics:   collector,
	}

	seedDefaultWorkspace(log)
	seedResponseDetail(log)

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
		// DelegateHandler operates first. It takes only a /v1/messages request with an
		// explicit trailing flag, "<prompt> /vendor-name". That is the same convention
		// as /local and /cloud.
		//
		// Therefore it never changes the behaviour of the interceptor, which gives
		// attention to Cursor. Each request that DelegateHandler does not take
		// continues.
		localHandler := &mitm.ChainHandler{Handlers: []mitm.LocalHandler{
			&mitm.DelegateHandler{Log: log, Metrics: collector},
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
	safego.Go("pollVRAM", log, func() { pollVRAM(stopVRAM, vramMon, collector, reg, log) })
	safego.Go("pollBackendHealth", log, func() { pollBackendHealth(stopVRAM, reg, log) })

	var dash *dashboard.Server
	if cfg.Dashboard.Enabled {
		// Use 127.0.0.1, and not ":<port>", which is 0.0.0.0 and means each
		// interface. The dashboard is a local control panel. It controls the
		// delegation to vendors, the changes to the config, and each other setting.
		// There is no correct cause for a different machine on the network to reach
		// it. A person found this while that person connected the webview shell,
		// which needs 127.0.0.1 only. But the defect is older than that work. This
		// code does not change the ports of the gateway and of the MITM proxy. For
		// those, access from a different machine is a true and separate decision,
		// and some people want it. It is not an error.
		dashAddr := fmt.Sprintf("127.0.0.1:%d", cfg.Server.DashboardPort)
		store := &dashboard.FileConfigStore{Provider: provider, Path: cfgPath}
		models := &dashboard.RegistryModelController{Registry: reg}
		dash = dashboard.New(dashAddr, bus, store, models)
		dash.History = history
		dash.GPUs = gpuMonitorAdapter{mon: vramMon}
		dash.Metrics = collector
		dash.MITMDebug = mitmDebug
		if mitmProxy != nil {
			if scoper, ok := mitmProxy.Redirector.(mitm.PIDScoper); ok {
				dash.Redirector = scoper
			}
		}
		dash.ContextGraph = ctxGraph
		dash.Episodes = episodeStore
		dash.ContextRetainDays = retainDays

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
	if err := runstate.MarkStoppedCleanly(); err != nil {
		log.Debug("runstate: could not clear startup marker", "err", err)
	}
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

// seedDefaultWorkspace reads the default workspace directory from the disk,
// and puts it in the WorkspaceStore in memory. A person sets that directory
// on the Vendors page of the dashboard, through setDefaultWorkspace in
// internal/dashboard. The delegate calls read the store in memory.
// vendors.Registry is only the form of that value on the disk.
// ResolveDelegate reads vendors.SetDefaultWorkspace at the time of a request.
// A missing/unreadable registry means no default configured yet, not a
// startup failure.
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

// seedResponseDetail loads the persisted response-detail setting (same
// pattern as seedDefaultWorkspace above, same reasoning) into the in-memory
// value ResolveDelegate actually reads. A value that is empty or absent gives
// the default, which is clean. vendors.SetResponseDetail also reads a value
// that it does not know as clean. Therefore this code needs no other test for
// an empty value, except "the code cannot read the registry, so do nothing".
func seedResponseDetail(log *slog.Logger) {
	regPath, err := vendors.DefaultRegistryPath()
	if err != nil {
		return
	}
	reg, err := vendors.LoadRegistry(regPath)
	if err != nil || reg.ResponseDetail == "" {
		return
	}
	vendors.SetResponseDetail(reg.ResponseDetail)
	log.Info("glider delegate response detail loaded", "mode", reg.ResponseDetail)
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

// configureContinuitySummarizers registers the summary sources the config
// enables and sets their preference order.
//
// A source that cannot be built is simply not registered: the chain skips it
// and tries the next. That is deliberate — naming "cloud" on a machine with
// no cloud key should fall through to the next option, not fail compaction.
func configureContinuitySummarizers(reg *backend.Registry, cfg *config.Config, log *slog.Logger) {
	sum := cfg.Context.Summary

	var chain []vendors.SummarySource
	for _, name := range sum.Chain {
		switch vendors.SummarySource(strings.ToLower(strings.TrimSpace(name))) {
		case vendors.SummaryOrigin:
			chain = append(chain, vendors.SummaryOrigin)
		case vendors.SummaryCloud:
			chain = append(chain, vendors.SummaryCloud)
		case vendors.SummaryLocal:
			chain = append(chain, vendors.SummaryLocal)
		case vendors.SummaryNone:
			chain = append(chain, vendors.SummaryNone)
		default:
			log.Warn("context.summary: unknown chain entry, ignoring", "entry", name)
		}
	}
	vendors.SetSummaryChain(chain)

	if !sum.Enabled {
		// Compaction still runs, using the deterministic digest. Registering
		// nothing is what selects it.
		log.Info("context.summary: model summarization disabled, using deterministic digest")
		return
	}

	// origin — run an installed agent CLI headlessly.
	vendors.RegisterSummarizer(vendors.SummaryOrigin, vendors.OriginSummarizer{
		Registry: func() vendors.Registry {
			path, err := vendors.DefaultRegistryPath()
			if err != nil {
				return vendors.Registry{}
			}
			r, err := vendors.LoadRegistry(path)
			if err != nil {
				return vendors.Registry{}
			}
			return r
		},
		Vendor: strings.TrimSpace(sum.OriginVendor),
	})

	// cloud — a BYOK backend.
	if name := firstNonEmpty(sum.CloudBackend, defaultCloudBackendName(cfg)); name != "" {
		if b, err := reg.Get(name); err == nil {
			model := firstNonEmpty(sum.CloudModel, defaultCloudModelName(cfg))
			if model != "" {
				vendors.RegisterSummarizer(vendors.SummaryCloud, summarizer.Backend{Inference: b, Model: model})
			} else {
				log.Warn("context.summary: no cloud model resolved, skipping cloud source", "backend", name)
			}
		} else {
			log.Warn("context.summary: cloud backend unavailable, skipping cloud source", "backend", name, "err", err)
		}
	}

	// local — a local model.
	if model := firstNonEmpty(sum.LocalModel, cfg.Routing.DefaultLocalModel); model != "" {
		if b, err := reg.Get("ollama"); err == nil {
			vendors.RegisterSummarizer(vendors.SummaryLocal, summarizer.Backend{Inference: b, Model: model})
		}
	}

	log.Info("context.summary: configured", "chain", vendors.SummaryChain())
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// defaultCloudBackendName picks the first configured non-local backend.
func defaultCloudBackendName(cfg *config.Config) string {
	for _, p := range cfg.Cloud.Providers {
		if strings.TrimSpace(p.Name) != "" {
			return p.Name
		}
	}
	return ""
}

// defaultCloudModelName picks a sensible cloud model when none is named.
func defaultCloudModelName(cfg *config.Config) string {
	for _, r := range cfg.Routing.Rules {
		if strings.EqualFold(r.Action.Target, "cloud") && strings.TrimSpace(r.Action.Model) != "" {
			return r.Action.Model
		}
	}
	return ""
}
