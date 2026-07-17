# Glider: AI Harness — Architecture Document

> Comprehensive HLD & LLD for a local AI proxy that intercepts Cursor Chat, routes to local SLMs/LoRAs or cloud models, and manages VRAM dynamically.

---

## 1. Gap Analysis (vs. Previous Plan)

The previous implementation plan had critical gaps uncovered by deep research into Ollama, Starlark, vLLM, and NVML. These are documented below so we can address every one of them in the architecture.

### 1.1 Critical Gaps

| # | Gap | Impact | Resolution |
|---|-----|--------|------------|
| G1 | **Ollama cannot hot-swap LoRAs per-request.** Adapters must be pre-baked into named model variants via Modelfiles. | Our original "hot-swap LoRA on a warm base model" strategy is impossible with Ollama alone. | **Dual-backend architecture:** Use Ollama for simple model switching (pre-baked variants). Use vLLM as a core backend for true per-request LoRA hot-swapping (`/v1/load_lora_adapter`). |
| G2 | **No error handling or resilience.** No fallback chain if a backend crashes, OOMs, or returns errors. | A single Ollama crash would freeze Cursor with no response. | Add a **Fallback Chain** in the Orchestrator: Local → Cloud, with configurable retry/timeout policies. Circuit breaker pattern. |
| G3 | **No concurrency model.** Multiple Cursor tabs/windows can send simultaneous requests. GPU can only handle one inference at a time efficiently. | Request collision, GPU stutter, or dropped requests. | Add a **Request Queue** with priority levels (user-interactive > background). Mutex on GPU-bound operations. |
| G4 | **No Model Registry.** No catalog of available models, their VRAM footprint, context window size, or capabilities. | The Router can't make informed decisions about which model fits in memory. | Add a **Model Registry** that tracks each model's metadata (VRAM size, max context, capabilities, warm/cold state). |
| G5 | **No Request Transformation.** Large context payloads can exceed a local model's context window, causing failures or degraded quality. | Requests fail or local models produce garbage output on oversized context. | Add an **opt-in Request Transformer** for context trimming and prompt augmentation. **Do NOT strip Cursor's system prompts by default** — they contain formatting instructions Cursor's UI depends on to parse responses correctly. |
| G6 | **No Config Hot-Reload.** Changing `glider.yaml` requires restarting the daemon. | Breaks the "quick switching" requirement. | Use `fsnotify` to watch config files and atomically swap configuration at runtime. |
| G7 | **Windows VRAM monitoring.** `go-nvml` is Linux-only. We're on Windows. | Cannot programmatically query VRAM on the target platform. | Use `nvidia-smi --query-gpu=...` CLI as a cross-platform fallback. Optionally load `nvml.dll` directly on Windows via `syscall`. |
| G8 | **No rate limiting for cloud fallback.** If many requests cascade to cloud, costs explode. | Uncontrolled cloud API spend. | Add configurable rate limits and budget caps for cloud tier. |
| G9 | **No health checks.** No way to know if Ollama/vLLM is alive before routing to it. | Silent failures. | Periodic health pings to all registered backends. Mark unhealthy backends as unavailable. |
| G10 | **No metrics or cost tracking.** No way to see how many tokens were routed locally vs. cloud, latency percentiles, or estimated cost savings. | User can't validate if Glider is actually saving them money. | Add a metrics collector exposed via the Dashboard. |

### 1.2 Revised Key Decisions

| Decision | Previous | Revised |
|----------|----------|---------|
| Inference Backend | Ollama only | **Ollama + vLLM** (both core, pluggable via interface) |
| LoRA Strategy | Hot-swap on warm base | **Ollama:** pre-baked model variants. **vLLM:** true per-request LoRA swap. |
| VRAM Monitoring | Unspecified | **`nvidia-smi` CLI** (cross-platform) + optional `nvml.dll` on Windows |
| Config Changes | Restart required | **Hot-reload** via filesystem watcher |
| Scripting | Starlark (confirmed) | Starlark + `starlib` for regex support |

---

## 2. Planned Features (Complete)

### Core
- [ ] OpenAI-compatible proxy (`/v1/chat/completions`, `/v1/models`)
- [ ] SSE streaming passthrough (Cursor ↔ Proxy ↔ Backend)
- [ ] Explicit routing commands (`/local`, `/cloud`, `/heavy`)
- [ ] Implicit routing via regex, keywords, and context-size thresholds
- [ ] Custom Starlark scripting for advanced routing logic
- [ ] Configurable context token threshold (exposed parameter)

### Model & VRAM Management
- [ ] Dynamic VRAM allocation with configurable strategies (static warm / dynamic load-on-demand)
- [ ] Scale-to-zero: unload models after configurable idle timeout (`keep_alive`)
- [ ] Model Registry with VRAM footprint, context window, and capability metadata
- [ ] Pre-baked LoRA model variants (Ollama) with fast named-model switching
- [ ] vLLM backend for true per-request LoRA hot-swapping (core, not optional)
- [ ] Multi-GPU support (allocate models to specific GPUs)
- [ ] VRAM headroom reservation (always keep X MB free for OS/other apps)

### Resilience & Performance
- [ ] Fallback chain: Local → Cloud with configurable retry/timeout
- [ ] Circuit breaker on failing backends
- [ ] Request queue with priority (interactive > background)
- [ ] Health checks for all registered backends
- [ ] Cloud rate limiting and budget caps

### Configuration & Extensibility
- [ ] `glider.yaml` — single-file configuration
- [ ] Hot-reload on config file change (no restart)
- [ ] Pluggable backend interface (add new backends without modifying core)
- [ ] Request transformation pipeline (opt-in context trimming & prompt augmentation — system prompts preserved by default)

### Observability Dashboard (Web UI)
- [ ] Real-time VRAM usage visualization (per-GPU, per-model)
- [ ] Live request log with routing decisions, latency, token counts
- [ ] Model management panel (load/unload/switch models)
- [ ] Rule editor with Starlark script support
- [ ] Configuration editor (thresholds, VRAM allotments, PnCs)
- [ ] Cost savings tracker (local tokens vs. estimated cloud cost)
- [ ] WebSocket-driven real-time updates

---

## 3. High-Level Design (HLD)

### 3.1 System Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                          Cursor IDE                                  │
│                (sends POST /v1/chat/completions)                     │
└──────────────────────────┬───────────────────────────────────────────┘
                           │ HTTP (localhost:8080)
                           ▼
┌──────────────────────────────────────────────────────────────────────┐
│                      GLIDER PROXY DAEMON                             │
│                                                                      │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────────────────────┐  │
│  │  API Gateway │  │  Config Mgr  │  │   Dashboard Server         │  │
│  │  (OpenAI)    │  │  (hot-reload)│  │   (Web UI on :8081)        │  │
│  └──────┬───────┘  └──────┬───────┘  └────────────┬───────────────┘  │
│         │                 │                        │                  │
│  ═══════▼═════════════════▼════════════════════════▼══════════════   │
│  ║                  REQUEST PIPELINE                             ║   │
│  ║  ┌────────────┐  ┌────────────┐  ┌─────────────────────────┐  ║  │
│  ║  │ Tokenizer  │→ │  Router    │→ │ Request Transformer     │  ║  │
│  ║  │ (count)    │  │  (rules +  │  │ (strip Cursor prompts,  │  ║  │
│  ║  │            │  │  Starlark) │  │  reformat for target)   │  ║  │
│  ║  └────────────┘  └────────────┘  └─────────────────────────┘  ║  │
│  ═════════════════════════╤══════════════════════════════════════  │
│                           │ RoutingDecision                       │
│  ┌────────────────────────▼──────────────────────────────────────┐│
│  │                    ORCHESTRATOR                                ││
│  │  ┌──────────────┐  ┌──────────────┐  ┌─────────────────────┐ ││
│  │  │ VRAM Manager │  │ Model        │  │ Request Queue       │ ││
│  │  │ (allocator + │  │ Registry     │  │ (priority, mutex)   │ ││
│  │  │  monitor)    │  │ (catalog)    │  │                     │ ││
│  │  └──────────────┘  └──────────────┘  └─────────────────────┘ ││
│  └───────────┬────────────────────────────────┬──────────────────┘│
│              │                                │                   │
└──────────────┼────────────────────────────────┼───────────────────┘
               │                                │
      ┌────────▼────────┐             ┌─────────▼──────────┐
      │   LOCAL TIER    │             │    CLOUD TIER      │
      │                 │             │                    │
      │ ┌─────────────┐ │             │ ┌────────────────┐ │
      │ │ Ollama      │ │             │ │ OpenAI         │ │
      │ │ Backend     │ │             │ │ Backend        │ │
      │ └─────────────┘ │             │ └────────────────┘ │
      │ ┌─────────────┐ │             │ ┌────────────────┐ │
      │ │ vLLM        │ │             │ │ Anthropic      │ │
      │ │ Backend     │ │             │ │ Backend        │ │
      │ └─────────────┘ │             │ └────────────────┘ │
      └─────────────────┘             └────────────────────┘
```

### 3.2 Component Responsibilities

| Component | Single Responsibility | Owns |
|-----------|----------------------|------|
| **API Gateway** | Accept and validate OpenAI-format HTTP requests, stream SSE responses back. | HTTP lifecycle only. |
| **Tokenizer** | Estimate token count of incoming payloads. | BPE encoding, token math. |
| **Router** | Evaluate rules (explicit, regex, threshold, Starlark scripts) and produce a `RoutingDecision`. | Rule evaluation only. Does NOT execute the request. |
| **Request Transformer** | Rewrite the payload for the target backend (strip Cursor system prompts, adjust chat template, trim context). | Prompt manipulation only. |
| **Orchestrator** | Coordinate model readiness and dispatch the transformed request to the correct backend. Handle fallback on failure. | Lifecycle coordination. |
| **VRAM Manager** | Track GPU memory state, enforce allocation budgets, decide when to evict idle models. | GPU memory accounting. |
| **Model Registry** | Catalog all available models with their metadata (VRAM size, context window, capabilities, current state). | Model metadata only. |
| **Request Queue** | Serialize GPU-bound requests with priority ordering. Prevent GPU contention. | Queueing only. |
| **Config Manager** | Load `glider.yaml`, watch for changes, atomically swap config, notify subscribers. | Configuration state. |
| **Dashboard Server** | Serve the Web UI, expose WebSocket for real-time updates, provide REST API for config edits. | UI and management API. |
| **Metrics Collector** | Aggregate latency, token counts, routing decisions, cost estimates. Feed to Dashboard. | Telemetry data. |
| **Backend (interface)** | Execute a completion request against a specific inference engine. | Inference execution. |

### 3.3 Data Flow (Detailed)

```
1. RECEIVE     │ Cursor sends POST /v1/chat/completions to localhost:8080
               │ Payload: {model, messages[], stream: true, ...}
               ▼
2. TOKENIZE    │ Tokenizer estimates total prompt token count
               │ Output: estimated_tokens = 4,200
               ▼
3. ROUTE       │ Router evaluates rules in priority order:
               │   a) Explicit: Does last message start with "/local"? → YES → local
               │   b) Starlark: Run scripts/detect_refactor.star → matched? 
               │   c) Threshold: estimated_tokens > max_local_context_tokens? → cloud
               │   d) Default: fallback rule
               │ Output: RoutingDecision{target: "ollama", model: "codellama:7b", ...}
               ▼
4. TRANSFORM   │ If target is local:
               │   - Strip Cursor's system prompt (saves ~2000 tokens)
               │   - Inject local-optimized system prompt
               │   - Truncate context if still over model's max_context
               │ If target is cloud:
               │   - Pass through unmodified
               ▼
5. QUEUE       │ Request enters the priority queue
               │   - Interactive (user waiting) → HIGH priority
               │   - Background task → LOW priority
               │ GPU mutex acquired when it's this request's turn
               ▼
6. ORCHESTRATE │ Orchestrator checks Model Registry:
               │   - Is "codellama:7b" already loaded? → skip load
               │   - Not loaded? → VRAM Manager checks if space available
               │     - Space available → tell backend to load model
               │     - No space → evict LRU model, then load
               ▼
7. EXECUTE     │ Backend.Complete(ctx, request) → returns chan CompletionChunk
               │ Proxy streams SSE chunks back to Cursor in real-time
               ▼
8. OBSERVE     │ Metrics Collector records:
               │   - Route taken (local/cloud), model used, latency
               │   - Tokens in/out, estimated cost saved
               │ Dashboard updates via WebSocket
               ▼
9. IDLE        │ After keep_alive timeout, VRAM Manager unloads idle model
               │ GPU memory freed → scale-to-zero achieved
```

---

## 4. Low-Level Design (LLD)

### 4.1 Project Structure (Go)

```
glider/
├── cmd/
│   └── glider/
│       └── main.go                    # Entry point, DI wiring
│
├── internal/
│   ├── api/                           # [S] HTTP server layer
│   │   ├── server.go                  # HTTP server lifecycle
│   │   ├── handlers.go                # /v1/chat/completions, /v1/models
│   │   ├── streaming.go               # SSE response writer
│   │   └── middleware.go              # Logging, CORS, request ID
│   │
│   ├── config/                        # [S] Configuration management
│   │   ├── config.go                  # Config struct definitions
│   │   ├── loader.go                  # YAML parser + validator
│   │   └── watcher.go                 # fsnotify hot-reload
│   │
│   ├── router/                        # [S] Routing logic
│   │   ├── router.go                  # Rule evaluation engine
│   │   ├── rules.go                   # Rule types (regex, threshold, explicit)
│   │   └── starlark.go                # Starlark script executor + caching
│   │
│   ├── transform/                     # [S] Request/response transformation
│   │   ├── pipeline.go                # Transform pipeline coordinator
│   │   ├── cursor_strip.go            # Strip Cursor system prompts
│   │   ├── context_trim.go            # Intelligent context truncation
│   │   └── tokenizer.go              # BPE token counter
│   │
│   ├── orchestrator/                  # [S] Execution coordination
│   │   ├── orchestrator.go            # Main orchestration logic
│   │   ├── queue.go                   # Priority request queue
│   │   └── fallback.go               # Fallback chain + circuit breaker
│   │
│   ├── backend/                       # [O][L][I][D] Pluggable backends
│   │   ├── interfaces.go             # Core interfaces (see §4.2)
│   │   ├── registry.go               # Backend + model registry
│   │   ├── ollama/                    # Ollama implementation
│   │   │   ├── client.go             # HTTP client for Ollama API
│   │   │   ├── backend.go            # InferenceBackend impl
│   │   │   └── models.go             # ModelManager impl
│   │   ├── vllm/                      # vLLM implementation
│   │   │   ├── client.go
│   │   │   ├── backend.go
│   │   │   └── lora.go               # LoRA hot-swap logic
│   │   └── cloud/                     # Cloud backends
│   │       ├── openai.go
│   │       └── anthropic.go
│   │
│   ├── vram/                          # [S] GPU memory management
│   │   ├── manager.go                 # VRAM allocation + eviction
│   │   ├── monitor_nvidia_smi.go      # nvidia-smi based monitor
│   │   ├── monitor_nvml_windows.go    # Optional nvml.dll direct call
│   │   └── allocator.go              # Allocation strategies
│   │
│   ├── dashboard/                     # [S] Web UI
│   │   ├── server.go                  # Dashboard HTTP + WebSocket
│   │   ├── api.go                     # REST API for config/model mgmt
│   │   └── static/                    # Embedded frontend assets
│   │       ├── index.html
│   │       ├── app.js
│   │       └── style.css
│   │
│   └── metrics/                       # [S] Observability
│       ├── collector.go               # Metrics aggregation
│       └── events.go                  # Event bus for Dashboard
│
├── scripts/                           # User Starlark routing scripts
│   └── examples/
│       ├── detect_refactor.star
│       └── large_file_router.star
│
├── configs/
│   └── glider.yaml                    # Default configuration
│
├── web/                               # Frontend source (dev)
│   ├── index.html
│   ├── app.js
│   └── style.css
│
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

### 4.2 Core Interfaces (SOLID Mapped)

Each interface is narrow (Interface Segregation) and the core depends only on these abstractions (Dependency Inversion), never on concrete Ollama/vLLM types.

```go
// ═══════════════════════════════════════════════════════════════
// backend/interfaces.go — The contract layer
// ═══════════════════════════════════════════════════════════════

// --- Data Types ---

type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type CompletionRequest struct {
    Model       string            `json:"model"`
    Messages    []Message         `json:"messages"`
    Stream      bool              `json:"stream"`
    Temperature *float64          `json:"temperature,omitempty"`
    MaxTokens   *int              `json:"max_tokens,omitempty"`
    Metadata    RequestMetadata   // Glider-internal, not sent to backend
}

type RequestMetadata struct {
    RequestID       string
    EstimatedTokens int
    Priority        Priority // HIGH (interactive) | LOW (background)
    OriginalModel   string   // What Cursor originally requested
    Adapter         string   // LoRA adapter name (vLLM only)
}

type CompletionChunk struct {
    ID           string `json:"id"`
    Content      string `json:"content"`
    FinishReason string `json:"finish_reason,omitempty"`
    Model        string `json:"model"`
}

type RoutingDecision struct {
    Strategy    ExecutionStrategy // "single" | "fan_out" | "pipeline" | "ensemble"
    Target      string // "local" | "cloud"
    BackendName string // "ollama" | "vllm" | "openai" | "anthropic"
    Model       string // Target model name
    Adapter     string // LoRA adapter (vLLM only, empty otherwise)
    RuleName    string // Which rule matched (for observability)
    Reason      string // Human-readable reason
    SubTasks    []SubTask // For V2 swarm routing
}

type ExecutionStrategy string
const (
    StrategySingle   ExecutionStrategy = "single"   // V1: one model, one response
    StrategyFanOut   ExecutionStrategy = "fan_out"  // V2: N models in parallel
    StrategyPipeline ExecutionStrategy = "pipeline" // V2: chained models
    StrategyEnsemble ExecutionStrategy = "ensemble" // V2: same prompt to N models, pick best
)

type SubTask struct {
    Prompt string
    Target string
    Model  string
}

// --- [I] Interface Segregation: Narrow, focused interfaces ---

// InferenceBackend — executes completions (Single Responsibility)
type InferenceBackend interface {
    Name() string
    Type() BackendType // LOCAL | CLOUD
    Complete(ctx context.Context, req *CompletionRequest) (<-chan CompletionChunk, error)
}

// ModelManager — manages model lifecycle on a backend (Single Responsibility)
type ModelManager interface {
    LoadModel(ctx context.Context, model string, opts LoadOptions) error
    UnloadModel(ctx context.Context, model string) error
    ListLoaded(ctx context.Context) ([]LoadedModel, error)
}

// LoRAManager — manages adapter hot-swapping (Interface Segregation)
// Only implemented by backends that support it (e.g., vLLM). Not Ollama.
type LoRAManager interface {
    LoadAdapter(ctx context.Context, name string, path string) error
    UnloadAdapter(ctx context.Context, name string) error
    ListAdapters(ctx context.Context) ([]string, error)
}

// HealthChecker — reports backend health (Interface Segregation)
type HealthChecker interface {
    Ping(ctx context.Context) error
    IsHealthy() bool
}

// --- Load Options ---

type LoadOptions struct {
    NumGPULayers int           // -1 = auto, 0 = CPU only, N = specific layers
    KeepAlive    time.Duration // How long to keep loaded after last request
    GPUIndex     int           // Which GPU to load on (multi-GPU)
}

type LoadedModel struct {
    Name      string
    SizeVRAM  int64         // Bytes currently in VRAM
    SizeRAM   int64         // Bytes in system RAM
    ExpiresAt time.Time     // When it will be auto-unloaded
    Backend   string        // Which backend owns it
}
```

```go
// ═══════════════════════════════════════════════════════════════
// router/router.go — Rule evaluation
// ═══════════════════════════════════════════════════════════════

// Router evaluates rules and returns a RoutingDecision
type Router interface {
    Route(ctx context.Context, req *CompletionRequest) (*RoutingDecision, error)
}

// Rule is the interface for all rule types (Open/Closed: add new types
// without modifying the engine)
type Rule interface {
    Name() string
    Priority() int
    Evaluate(ctx context.Context, req *CompletionRequest) (*RuleResult, error)
}

type RuleResult struct {
    Matched bool
    Action  *RoutingDecision // nil if not matched
}

// Concrete rule types (each satisfies Rule interface — Liskov Substitution)
// - ExplicitCommandRule  → matches "/local", "/cloud", "/heavy" prefixes
// - RegexRule            → matches patterns against message content
// - ContextSizeRule      → matches on estimated token count vs threshold
// - StarlarkScriptRule   → executes a .star file and interprets result
```

```go
// ═══════════════════════════════════════════════════════════════
// orchestrator/orchestrator.go — Execution Layer
// ═══════════════════════════════════════════════════════════════

// Executor replaces a hard-coded 1-to-1 dispatch model, allowing
// V2 to implement SwarmExecutor for Fan-Out and Pipeline strategies.
type Executor interface {
    Execute(ctx context.Context, decision *RoutingDecision, req *CompletionRequest) (<-chan CompletionChunk, error)
}

// SimpleExecutor handles V1's StrategySingle
type SimpleExecutor struct {
    backends BackendRegistry
    vram     VRAMManager
}
```

```go
// ═══════════════════════════════════════════════════════════════
// vram/manager.go — VRAM state machine
// ═══════════════════════════════════════════════════════════════

type VRAMManager interface {
    // Query
    GetState() *VRAMState
    CanFit(model string, requiredBytes int64) (bool, *EvictionPlan)

    // Mutate
    Reserve(model string, bytes int64) error
    Release(model string) error

    // Swarm Batch Allocation (V2 readiness)
    // Reserves space for N models atomically (all or nothing)
    BatchReserve(models []ModelAllocation) (*BatchReservation, error)
    BatchRelease(reservation *BatchReservation) error

    // Policy
    SetStrategy(strategy AllocationStrategy)
    SetHeadroom(bytes int64) // Always keep this much VRAM free
}

type VRAMState struct {
    TotalBytes     int64
    UsedBytes      int64
    FreeBytes      int64
    HeadroomBytes  int64
    LoadedModels   []ModelAllocation
    GPUIndex       int
}

type AllocationStrategy string
const (
    StrategyStatic  AllocationStrategy = "static"  // Keep warm always
    StrategyDynamic AllocationStrategy = "dynamic" // Load on demand
    StrategyHybrid  AllocationStrategy = "hybrid"  // Pin base, dynamic others
)

type EvictionPlan struct {
    ModelsToEvict []string
    BytesFreed    int64
}
```

```go
// ═══════════════════════════════════════════════════════════════
// config/config.go — Configuration structure
// ═══════════════════════════════════════════════════════════════

type Config struct {
    Server     ServerConfig     `yaml:"server"`
    Thresholds ThresholdConfig  `yaml:"thresholds"`
    VRAM       VRAMConfig       `yaml:"vram"`
    Models     []ModelConfig    `yaml:"models"`
    Routing    RoutingConfig    `yaml:"routing"`
    Cloud      CloudConfig      `yaml:"cloud"`
    Dashboard  DashboardConfig  `yaml:"dashboard"`
}

type ThresholdConfig struct {
    MaxLocalContextTokens int    `yaml:"max_local_context_tokens"`
    IdleUnloadTimeout     string `yaml:"idle_unload_timeout"` // e.g. "5m"
}

type VRAMConfig struct {
    Strategy       string `yaml:"strategy"`       // static | dynamic | hybrid
    HeadroomMB     int    `yaml:"headroom_mb"`     // Always keep free
    MaxLoadedModels int   `yaml:"max_loaded_models"`
    GPUAssignments  map[string]int `yaml:"gpu_assignments"` // model → GPU index
}

type ModelConfig struct {
    Name          string `yaml:"name"`          // e.g. "codellama:7b"
    Backend       string `yaml:"backend"`       // "ollama" | "vllm"
    VRAMEstimateMB int   `yaml:"vram_estimate_mb"`
    MaxContext    int    `yaml:"max_context"`
    Capabilities  []string `yaml:"capabilities"` // ["code", "refactor", "docs"]
    Adapter       string `yaml:"adapter,omitempty"` // LoRA name (vLLM)
    KeepWarm      bool   `yaml:"keep_warm"`     // Pin in VRAM
}

type CloudConfig struct {
    Providers []CloudProviderConfig `yaml:"providers"`
    RateLimit RateLimitConfig       `yaml:"rate_limit"`
    BudgetCap float64               `yaml:"budget_cap_usd"` // Monthly cap
}
```

### 4.3 SOLID Principles Mapping

| Principle | How It's Applied |
|-----------|-----------------|
| **S — Single Responsibility** | Every component has exactly one reason to change. The Router only evaluates rules. The Orchestrator only coordinates lifecycle. The VRAM Manager only tracks memory. They never bleed into each other's concerns. |
| **O — Open/Closed** | New backends (e.g., SGLang, TensorRT) are added by implementing the `InferenceBackend` interface and registering in the Backend Registry. Zero changes to Router or Orchestrator code. New rule types (e.g., `FileTypeRule`) implement the `Rule` interface. |
| **L — Liskov Substitution** | Any `InferenceBackend` implementation (Ollama, vLLM, OpenAI, Anthropic) can be used interchangeably by the Orchestrator. The Orchestrator never type-asserts to a concrete backend. Optional capabilities (LoRA) are checked via interface assertion (`if lm, ok := backend.(LoRAManager); ok`). |
| **I — Interface Segregation** | `LoRAManager` is a separate interface from `InferenceBackend`. Ollama implements `InferenceBackend` + `ModelManager` + `HealthChecker` but NOT `LoRAManager`. vLLM implements all four. No backend is forced to implement capabilities it doesn't have. |
| **D — Dependency Inversion** | `main.go` (the composition root) wires concrete implementations into the Orchestrator via interfaces. The Orchestrator depends on `InferenceBackend`, never on `*ollama.Client`. Config is injected, not imported globally. |

### 4.4 Hot-Swap Mechanics

#### 4.4.1 Model Hot-Swap

```
┌─────────────────────────────────────────────────────┐
│              MODEL STATE MACHINE                     │
│                                                      │
│   ┌──────────┐   load()   ┌──────────┐              │
│   │          │ ──────────▶ │          │              │
│   │  COLD    │             │ LOADING  │              │
│   │ (on disk)│ ◀────────── │          │              │
│   │          │   fail()    │          │              │
│   └──────────┘             └─────┬────┘              │
│        ▲                         │ ready()           │
│        │ evict()                 ▼                    │
│   ┌────┴─────┐  keep_alive  ┌──────────┐            │
│   │          │ ◀──timeout─── │          │            │
│   │ UNLOADING│               │   WARM   │            │
│   │          │               │ (in VRAM)│◀──request──│
│   └──────────┘               └──────────┘            │
│                                                      │
│  Transitions triggered by: Orchestrator, VRAM Mgr,   │
│  or idle timeout timer.                              │
└─────────────────────────────────────────────────────┘
```

**Ollama backend:** Model switch = `POST /api/generate {model: X, keep_alive: 0}` (unload old) → `POST /api/generate {model: Y, keep_alive: "30m"}` (load new). Latency: ~1-3s depending on model size.

**vLLM backend:** Base model stays loaded. Adapter switch = `POST /v1/load_lora_adapter`. Latency: sub-ms on cache hit, ~100ms on cache miss.

#### 4.4.2 Config Hot-Swap

```
┌───────────────────────────────────────────────────┐
│              CONFIG HOT-RELOAD FLOW                │
│                                                    │
│  glider.yaml ──(fsnotify)──▶ Config Watcher       │
│                                    │               │
│                              Parse + Validate      │
│                                    │               │
│                              ┌─────▼─────┐        │
│                              │ Atomic     │        │
│                              │ Pointer    │        │
│                              │ Swap       │        │
│                              └─────┬─────┘        │
│                                    │               │
│                    ┌───────────────┼──────────┐    │
│                    ▼               ▼          ▼    │
│              Router          VRAM Mgr    Dashboard │
│           (re-compiles     (adjusts     (refreshes │
│            Starlark)       allotments)   UI)       │
└───────────────────────────────────────────────────┘

Uses sync/atomic.Value for lock-free reads.
Subscribers are notified via a fan-out callback channel.
Invalid configs are rejected — old config stays active.
```

#### 4.4.3 Backend Hot-Swap

Backends can be added/removed at runtime via the Dashboard API:
1. User adds a new backend (e.g., vLLM at `localhost:8001`) via Dashboard.
2. Dashboard API calls `BackendRegistry.Register(newBackend)`.
3. Registry health-checks the new backend.
4. If healthy, it becomes available for routing immediately.
5. Router's next `Route()` call can now target it.

---

## 5. Configuration Schema (Complete)

```yaml
# glider.yaml — Complete configuration

server:
  proxy_port: 8080        # OpenAI-compatible proxy
  dashboard_port: 8081    # Web UI
  log_level: "info"       # debug | info | warn | error

thresholds:
  max_local_context_tokens: 8000  # Above this → route to cloud
  idle_unload_timeout: "5m"       # Unload model after this idle period
  request_timeout: "120s"         # Max time for a single request

vram:
  strategy: "hybrid"              # static | dynamic | hybrid
  headroom_mb: 512                # Always keep 512MB free
  max_loaded_models: 3
  gpu_assignments:                # Pin models to specific GPUs
    "codellama:7b": 0
    "llama3:70b-q4": 1

models:
  - name: "codellama:7b"
    backend: "ollama"
    vram_estimate_mb: 4200
    max_context: 16384
    capabilities: ["code", "refactor", "debug"]
    keep_warm: true               # Always loaded (hybrid strategy)

  - name: "llama3:8b-instruct"
    backend: "ollama"
    vram_estimate_mb: 5000
    max_context: 8192
    capabilities: ["general", "docs", "explain"]
    keep_warm: false

  - name: "codellama-base"
    backend: "vllm"
    vram_estimate_mb: 4200
    max_context: 16384
    capabilities: ["code"]
    adapters:                     # vLLM LoRA adapters
      - name: "refactor-lora"
        path: "./adapters/refactor/"
      - name: "test-gen-lora"
        path: "./adapters/test-gen/"

routing:
  # Rules are evaluated top-to-bottom. First match wins.
  rules:
    - name: "Explicit Local"
      priority: 100
      trigger:
        type: "explicit"
        commands: ["/local", "/fast"]
      action:
        target: "local"
        model: "codellama:7b"

    - name: "Explicit Cloud"
      priority: 99
      trigger:
        type: "explicit"
        commands: ["/cloud", "/heavy"]
      action:
        target: "cloud"
        backend: "openai"
        model: "gpt-4o"

    - name: "Refactor Detector"
      priority: 50
      trigger:
        type: "script"
        file: "scripts/detect_refactor.star"
      action:
        target: "local"
        model: "codellama-base"
        adapter: "refactor-lora"

    - name: "Context Overflow"
      priority: 10
      trigger:
        type: "context_size"
        operator: ">"
        value: 8000
      action:
        target: "cloud"
        backend: "anthropic"
        model: "claude-sonnet-4-20250514"

    - name: "Default Local"
      priority: 0
      trigger:
        type: "always"
      action:
        target: "local"
        model: "codellama:7b"

cloud:
  providers:
    - name: "openai"
      api_key_env: "OPENAI_API_KEY"     # Read from env var
      base_url: "https://api.openai.com/v1"
    - name: "anthropic"
      api_key_env: "ANTHROPIC_API_KEY"
      base_url: "https://api.anthropic.com/v1"
  rate_limit:
    requests_per_minute: 30
    tokens_per_minute: 100000
  budget_cap_usd: 50.00              # Monthly cap, alerts at 80%

backends:
  - name: "ollama"
    type: "local"
    url: "http://localhost:11434"
    health_check_interval: "30s"
  - name: "vllm"
    type: "local"
    url: "http://localhost:8001"
    health_check_interval: "30s"

dashboard:
  enabled: true
  auth: false                         # TODO: add auth for shared machines
```

---

## 6. Dependency Map

| Dependency | Purpose | Go Package / Tool |
|------------|---------|-------------------|
| **Ollama** | Core local inference engine (model variants) | External process, HTTP API |
| **vLLM** | Core local inference engine (LoRA hot-swap) | External process, HTTP API |
| **Starlark** | Rule scripting engine | `go.starlark.net/starlark` |
| **Starlib** | Regex + utilities for Starlark | `github.com/qri-io/starlib` |
| **fsnotify** | Config file hot-reload | `github.com/fsnotify/fsnotify` |
| **gorilla/websocket** | Dashboard real-time updates | `github.com/gorilla/websocket` |
| **yaml.v3** | Config parsing | `gopkg.in/yaml.v3` |
| **tiktoken-go** | BPE token counting | `github.com/pkoukk/tiktoken-go` |
| **nvidia-smi** | VRAM monitoring (cross-platform) | CLI subprocess |
| **embed** | Bundle frontend assets into binary | `embed` (stdlib) |
| **slog** | Structured logging | `log/slog` (stdlib) |

---

## 7. Phased Implementation Plan

> [!NOTE]
> All features are core — no stretch goals. vLLM, full Dashboard, and all backends are built from the start across the phases below.

### Phase 1 — Foundation & Proxy
> Goal: Cursor talks to Glider, Glider forwards to local backends. Streaming works.

- [ ] Go project scaffold with full module structure (see §4.1)
- [ ] API Gateway: `/v1/chat/completions` handler with SSE streaming
- [ ] Backend interface definitions (`InferenceBackend`, `ModelManager`, `LoRAManager`, `HealthChecker`)
- [ ] Ollama backend implementation (Complete, Load, Unload, Health)
- [ ] vLLM backend implementation (Complete, Load, Unload, LoRA swap, Health)
- [ ] Cloud backends: OpenAI + Anthropic passthrough
- [ ] Backend Registry (register/discover all backends)
- [ ] Simple passthrough routing (no rules yet, just default target)
- [ ] Verify: Cursor → Glider → Ollama → Cursor streams correctly
- [ ] Verify: Cursor → Glider → vLLM → Cursor streams correctly

### Phase 2 — Config, Router & Rules Engine
> Goal: Requests are intelligently routed based on rules. Config is hot-reloadable.

- [ ] Config loader + validator (`glider.yaml`)
- [ ] Config hot-reload via `fsnotify` with atomic swap
- [ ] Tokenizer integration (tiktoken-go)
- [ ] Rule engine core with priority ordering
- [ ] Rule types: ExplicitCommandRule, RegexRule, ContextSizeRule
- [ ] Starlark script executor with compiled-script caching
- [ ] StarlarkScriptRule type (load `.star` files, pass request, get routing decision)
- [ ] Starlib integration for regex in Starlark scripts
- [ ] Verify: `/local` routes locally, large context routes to cloud

### Phase 3 — VRAM Management, Model Lifecycle & Orchestrator
> Goal: Models load/unload dynamically. Scale-to-zero, fallback, and queuing work.

- [ ] VRAM Monitor (`nvidia-smi` CLI, with optional `nvml.dll` on Windows)
- [ ] Model Registry with metadata (VRAM estimate, max context, capabilities, state)
- [ ] VRAM Allocator with strategies (static / dynamic / hybrid)
- [ ] VRAM headroom reservation
- [ ] Model state machine (COLD → LOADING → WARM → UNLOADING)
- [ ] Idle timeout unloading (`keep_alive`)
- [ ] LRU eviction when VRAM is full
- [ ] Multi-GPU support (GPU assignments from config)
- [ ] Request Queue with priority (interactive > background)
- [ ] Fallback chain (local fail → cloud)
- [ ] Circuit breaker on failing backends
- [ ] Health check loop for all backends
- [ ] Cloud rate limiter and budget cap
- [ ] Verify: model auto-loads on request, unloads after idle timeout, fallback fires on backend crash

### Phase 4 — Dashboard, Observability & Request Transformation
> Goal: Full Web UI for monitoring and configuration. Opt-in request transformation.

- [ ] Dashboard HTTP server on separate port (embedded frontend via `embed`)
- [ ] Dashboard frontend: real-time VRAM gauge (per-GPU, per-model)
- [ ] Dashboard frontend: live request log with routing decisions, latency, token counts
- [ ] Dashboard frontend: model management panel (load/unload/switch)
- [ ] Dashboard frontend: rule editor with Starlark script support
- [ ] Dashboard frontend: configuration editor (thresholds, VRAM allotments, PnCs)
- [ ] Dashboard frontend: cost savings tracker (local tokens vs. estimated cloud cost)
- [ ] WebSocket server for real-time push updates
- [ ] REST API for config editing and model management
- [ ] Metrics collector (latency percentiles, token counts, routing stats, cost estimates)
- [ ] Request Transformer: opt-in context trimming (truncate middle file context when over model's max)
- [ ] Request Transformer: opt-in prompt augmentation (user-defined prepend/append instructions)
- [ ] Request Transformer: Starlark-scriptable transforms for advanced users
- [ ] **System prompts are preserved by default** — transformations never strip them unless user explicitly scripts it
- [ ] Verify: Dashboard shows live VRAM, config edits apply without restart, cost tracker reflects local savings

### Phase 5 — Integration Testing & Polish
> Goal: End-to-end stability, performance benchmarks, documentation.

- [ ] End-to-end integration tests (Cursor → Glider → Ollama/vLLM/Cloud → Cursor)
- [ ] Benchmark: proxy overhead target < 5ms on passthrough
- [ ] Stress test: concurrent requests from multiple Cursor windows
- [ ] Edge cases: OOM recovery, backend crash mid-stream, config corruption
- [ ] README, setup guide, example `glider.yaml` configs
- [ ] Makefile for build, test, release (single binary with embedded frontend)

---

## 8. Verification Plan

### Automated Tests
- **Unit:** Rule evaluation, config parsing, token counting, VRAM allocation math.
- **Integration:** Spin up Ollama, send requests through Glider, verify streaming output matches direct Ollama output.
- **Benchmark:** Measure proxy overhead (target: <5ms added latency on passthrough).

### Manual Verification
- Point Cursor at `localhost:8080`, use `/local` and `/cloud` commands, observe Dashboard routing log.
- Load a large file in Cursor, send a request, verify context threshold triggers cloud routing.
- Kill Ollama mid-request, verify fallback to cloud fires correctly.
- Edit `glider.yaml` while running, verify changes take effect without restart.

---

## Resolved Design Decisions

| # | Decision | Resolution |
|---|----------|------------|
| 1 | **Backend Priority** | Ollama + vLLM are both **core** from Phase 1. No stretch goals. |
| 2 | **Dashboard Scope** | Full-featured from the start: real-time monitoring, config editor, rule editor, model management, cost tracker. |
| 3 | **Request Transformation** | **Opt-in only.** Cursor's system prompts are **never stripped by default** — they contain formatting instructions Cursor's UI needs to parse responses. Transformation is limited to optional context trimming (truncate middle context when oversized) and optional prompt augmentation (user-defined instructions). Advanced users can write Starlark scripts for custom transforms. |

---

## 9. V2 Future Goals: Swarms & Loop Engineering

The architecture is explicitly decoupled to support advanced Agentic workflows in V2 without a rewrite. By making the `Executor` an interface and adding `Strategy` to `RoutingDecision`, we unlock:

### 9.1 Local Model Swarms (Delegation)
Instead of 1 request = 1 model, V2 will support a `SwarmExecutor` that handles `fan_out` and `pipeline` strategies.
- A "heavy" local model (e.g., Llama 3 70B via Ollama) acts as the **Planner**.
- The Planner decomposes a complex Cursor request into subtasks.
- The `SwarmExecutor` dispatches these subtasks to a pool of "worker" models (e.g., 8B fast coders) running in parallel.
- An Aggregator model merges the results into a single SSE stream sent back to Cursor.
- **VRAM implications:** Handled safely via the `BatchReserve()` atomic allocation interface designed in V1.

### 9.2 Loop Engineering (Local Reflection)
Because Glider intercepts the prompt before it hits the cloud (and before Cursor sees the final response), V2 can implement **Evaluation Loops**:
- Model A writes code.
- Glider executes a local linter or unit test against the output *in the background*.
- If it fails, Glider automatically re-prompts Model A with the error.
- Cursor only sees the final, working code stream back, unaware that 3 iterations happened locally in a fraction of a second.
