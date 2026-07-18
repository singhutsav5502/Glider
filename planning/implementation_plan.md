# Glider: AI Harness — Architecture Document

> Comprehensive HLD & LLD for a local AI proxy that intercepts Cursor Chat **and Agent**, routes to local SLMs/LoRAs or cloud, and manages VRAM dynamically.
>
> **Implemented dual mode:** (A) OpenAI-compatible BYOK gateway on `:8080`, and (B) HTTPS MITM forward proxy on `:8082` that can passthrough to Cursor’s original upstream (`*.cursor.sh`) with auth intact.

---

## 0. Implementation status (as of dual-mode MITM + dashboard UX)

Phases 1–5 core packages are implemented and covered by `go test ./...`. Phase 6 (MITM + Responses + aliases + shared harness) and later dashboard/observability work add:

| Area | Package / artifact | Behavior |
|------|-------------------|----------|
| Gateway Responses API | `internal/api` | `/v1/responses`; Responses-shaped bodies on `/chat/completions` |
| Model aliases | `internal/orchestrator`, `glider.yaml` | Rewrite Cursor model IDs before routing |
| HTTPS MITM | `internal/mitm` | CONNECT, local CA, allowlist decrypt, local intercept or origin passthrough |
| Config profiles | `configs/glider.yaml`, `configs/glider.cloud.yaml` | Intro/full-system demo (MITM on) vs gateway default-cloud BYOK |
| Setup | `scripts/setup-windows.ps1`, `start-glider.*`, `docs/CURSOR_CHECKLIST.md` | CA trust + Cursor proxy settings |
| Observability | `internal/metrics` | Mode/Action/Host/Path/Rule/OriginalModel; session JSONL under `~/.glider/history` |
| Dashboard UX | `internal/dashboard` | Config form + optional YAML; Rules editor; VRAM discovery; session browser |

**Shared harness:** Gateway and MITM both use `PipelineCompleter.Handle` (alias → tokenize → route → transform → execute).

| Entry | Mode | Non-local `target: cloud` |
|-------|------|---------------------------|
| `Complete` | Gateway `:8080` | BYOK OpenAI/Anthropic |
| `CompleteLocal` | MITM `:8082` | `ErrOriginPassthrough` → **Cursor origin** (auth preserved) |

Unrecognized Cursor-proprietary envelopes always blind-passthrough. **Routing priority:** (1) explicit `/local`/`/cloud` overrides → (2) Starlark scripts → (3) context thresholds → (4) default cloud.

**Hot-reload vs restart:** Swap/watch rebuilds router, model aliases, context threshold, and slog log level. GPU assignments are persisted on the same config Swap path and read by `GET /api/vram`. Listen ports, MITM (enable/port/CA/hosts), backend URLs, and cloud provider registration require process restart.

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
| G11 | **Gateway-only cannot see Cursor Agent / subscription models.** Override OpenAI Base URL only affects BYOK OpenAI path. | Agent and Claude/Cursor-native models never hit Glider. | Add **HTTPS MITM forward proxy** (`http.proxy`) with local CA; decrypt allowlisted hosts; passthrough to original Cursor upstream when not routing local. |
| G12 | **Cursor Agent Responses API / wrong endpoint shape.** Agent may POST Responses-shaped JSON to `/chat/completions`. | Gateway rejects `missing messages`. | Accept `/v1/responses` and translate Responses → chat completions (and stream Responses-shaped events back when needed). |

### 1.2 Revised Key Decisions

| Decision | Previous | Revised |
|----------|----------|---------|
| Inference Backend | Ollama only | **Ollama + vLLM** (both core, pluggable via interface) |
| LoRA Strategy | Hot-swap on warm base | **Ollama:** pre-baked model variants. **vLLM:** true per-request LoRA swap. |
| VRAM Monitoring | Unspecified | **`nvidia-smi` CLI** (cross-platform) + optional `nvml.dll` on Windows |
| Config Changes | Restart required | **Hot-reload** via filesystem watcher |
| Scripting | Starlark (confirmed) | Starlark + `starlib` for regex support |
| Cursor integration | Base URL override only | **Dual mode:** gateway BYOK **+** MITM for Agent / all models |
| Non-local destination | Always BYOK OpenAI/Anthropic | Gateway: BYOK. MITM: **original Host** passthrough |

---

## 2. Planned Features (Complete)

### Core
- [x] OpenAI-compatible proxy (`/v1/chat/completions`, `/v1/models`)
- [x] OpenAI Responses API (`/v1/responses`) + Responses-shaped body on chat completions
- [x] SSE streaming passthrough (Cursor ↔ Proxy ↔ Backend)
- [x] HTTPS MITM forward proxy (CONNECT, CA, allowlist, origin passthrough)
- [x] Explicit routing commands (`/local`, `/cloud`, `/heavy`)
- [x] Implicit routing via regex, keywords, and context-size thresholds
- [x] Custom Starlark scripting for advanced routing logic
- [x] Configurable context token threshold (exposed parameter)
- [x] `model_aliases` map (Cursor/OpenAI model ID → registry model)

### Model & VRAM Management
- [x] Dynamic VRAM allocation with configurable strategies (static warm / dynamic load-on-demand)
- [x] Scale-to-zero: unload models after configurable idle timeout (`keep_alive`)
- [x] Model Registry with VRAM footprint, context window, and capability metadata
- [x] Pre-baked LoRA model variants (Ollama) with fast named-model switching
- [x] vLLM backend for true per-request LoRA hot-swapping (core, not optional)
- [x] Multi-GPU support (allocate models to specific GPUs)
- [x] VRAM headroom reservation (always keep X MB free for OS/other apps)

### Resilience & Performance
- [x] Fallback chain: Local → Cloud with configurable retry/timeout (gateway path)
- [x] Circuit breaker on failing backends
- [x] Request queue with priority (interactive > background)
- [x] Health checks for all registered backends
- [x] Cloud rate limiting and budget caps

### Configuration & Extensibility
- [x] `glider.yaml` — single-file configuration (+ `glider.cloud.yaml` profile)
- [x] Hot-reload on config file change (no restart)
- [x] Pluggable backend interface (add new backends without modifying core)
- [x] Request transformation pipeline (opt-in context trimming & prompt augmentation — system prompts preserved by default)
- [ ] Starlark-scriptable transforms as a separate surface from routing Starlark (advanced; routing Starlark shipped)

### Observability Dashboard (Web UI)
- [x] Real-time VRAM usage visualization (per-GPU, per-model) — functional UI; `GET /api/vram` (Ollama/vLLM discover + nvidia-smi)
- [x] Live request log: Mode / Action / Host·Model / Rule / latency / tokens (WS + Overview)
- [x] Session history browser (`GET /api/sessions…`; store under `~/.glider/history`)
- [x] Model management panel (load/unload/switch models) + GPU assignment UI → `vram.gpu_assignments`
- [x] Rules Engine UI — create/edit/enable rules (explicit / script / context_size / always / …) persisted via `PUT /api/config`
- [x] Configuration editor — form primary (section cards + tooltips); YAML optional/collapsed; `GET|PUT /api/config`
- [x] Soft validation warnings (`GET|POST /api/validate`) against discovered model catalog
- [x] Cost / local-vs-cloud split tracker
- [x] WebSocket-driven real-time updates
- [ ] Pixel-perfect parity with `mock_dashboard_ui_design.md` (functional UI shipped)

---

## 3. High-Level Design (HLD)

### 3.1 System Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                          Cursor IDE                                  │
│  Mode A: POST /v1/* → localhost:8080   Mode B: http.proxy → :8082    │
└───────────────┬───────────────────────────────────┬──────────────────┘
                │                                   │ CONNECT + TLS
                ▼                                   ▼
┌───────────────────────────────┐    ┌──────────────────────────────────┐
│  API GATEWAY (:8080)          │    │  MITM FORWARD PROXY (:8082)      │
│  chat/completions, responses  │    │  CA mint · allowlist decrypt     │
│  models · SSE                 │    │  Interceptor → local | origin    │
└───────────────┬───────────────┘    └────────────────┬─────────────────┘
                │                                     │
                └──────────────────┬──────────────────┘
                                   ▼
┌──────────────────────────────────────────────────────────────────────┐
│                      REQUEST PIPELINE / ORCHESTRATOR                 │
│  Tokenizer → Router (rules + Starlark) → Transform → Executor        │
│  VRAM Manager · Model Registry · Priority Queue · Metrics            │
│  Dashboard (:8081)                                                   │
└───────────────┬───────────────────────────────────┬──────────────────┘
                │                                   │
       ┌────────▼────────┐               ┌──────────▼───────────┐
       │   LOCAL TIER    │               │  NON-LOCAL           │
       │ Ollama · vLLM   │               │ Gateway: OpenAI/     │
       └─────────────────┘               │   Anthropic (BYOK)   │
                                         │ MITM: original Host  │
                                         │   (*.cursor.sh, …)   │
                                         └──────────────────────┘
```

### 3.2 Component Responsibilities

| Component | Single Responsibility | Owns |
|-----------|----------------------|------|
| **API Gateway** | Accept and validate OpenAI-format HTTP requests (chat + Responses), stream SSE responses back. | HTTP lifecycle only. |
| **MITM Proxy** | Explicit HTTP CONNECT proxy; TLS terminate allowlisted hosts with local CA; intercept or blind-tunnel. | TLS/CONNECT only. |
| **MITM Interceptor** | Parse chat/Responses bodies; call shared `Harness.CompleteLocal` (same pipeline as gateway). | Adapter only — no separate router shortcut. |
| **PipelineCompleter** | Shared harness: alias → tokenize → route → transform → execute (`Complete` / `CompleteLocal`). | Decision + execution for both modes. |
| **Tokenizer** | Estimate token count of incoming payloads. | BPE encoding, token math. |
| **Router** | Evaluate rules (explicit, regex, threshold, Starlark scripts) and produce a `RoutingDecision`. | Rule evaluation only. Does NOT execute the request. |
| **Request Transformer** | Opt-in trim/augment (never strip Cursor system prompts by default). | Prompt manipulation only. |
| **Orchestrator** | Coordinate model readiness and dispatch; apply `model_aliases`; fallback on failure. | Lifecycle coordination. |
| **VRAM Manager** | Track GPU memory state, enforce allocation budgets, decide when to evict idle models. | GPU memory accounting. |
| **Model Registry** | Catalog all available models with their metadata (VRAM size, context window, capabilities, current state). | Model metadata only. |
| **Request Queue** | Serialize GPU-bound requests with priority ordering. Prevent GPU contention. | Queueing only. |
| **Config Manager** | Load `glider.yaml`, watch for changes, atomically swap config, notify subscribers. | Configuration state. |
| **Dashboard Server** | Serve the Web UI, expose WebSocket for real-time updates, REST for config/rules/VRAM/sessions. | UI and management API. |
| **Metrics Collector** | Aggregate latency, tokens, routing decisions; enrich with Mode/Action/Host/Path/Rule/OriginalModel; persist session history. | Telemetry data. |
| **Backend (interface)** | Execute a completion request against a specific inference engine. | Inference execution. |

### 3.3 Data Flow (Detailed)

**Mode A (gateway) — `PipelineCompleter.Complete`:**

```
1. RECEIVE     │ Cursor POST /v1/chat/completions or /v1/responses → :8080
               │ (Responses body may be translated to CompletionRequest)
2. ALIAS       │ Apply model_aliases (e.g. gpt-4o → codellama:7b)
3. TOKENIZE    │ Estimate prompt tokens
4. ROUTE       │ Priority: explicit overrides → Starlark → context thresholds → default
5. TRANSFORM   │ Opt-in trim/augment; system prompts preserved
6. QUEUE       │ Priority queue + GPU mutex
7. ORCHESTRATE │ Load model if needed (VRAM / registry)
8. EXECUTE     │ Local backends OR BYOK cloud → SSE / Responses events to Cursor
9. OBSERVE     │ Metrics (mode/action/host/path/rule/original_model) → Dashboard WS + `~/.glider/history`
```

**Mode B (MITM) — same harness via `CompleteLocal`:**

```
1. CONNECT     │ Cursor CONNECT api2.cursor.sh:443 → Glider :8082
2. MITM TLS    │ If host allowlisted (api2/api3/api4/*.api5.cursor.sh) → leaf cert from Glider CA; else blind tunnel
3. DECRYPT     │ Read HTTP request; if not chat/Responses → blind origin passthrough
4. HARNESS     │ Interceptor → CompleteLocal (alias → tokenize → route → transform → …)
               │   target local  → execute Ollama/vLLM, write response to client
               │   target cloud  → ErrOriginPassthrough → TLS to original Host (auth intact)
5. OBSERVE     │ slog decrypt|blind_tunnel; intercept local|origin_passthrough|skip|error; metrics → Dashboard
```

**Rule priority (both modes, `configs/glider.yaml`):**

| Priority | Kind | Role |
|----------|------|------|
| 100 / 99 | Explicit `/local`, `/fast`, `/cloud`, `/heavy` | **Overrides** |
| 50 | Starlark scripts (e.g. `detect_refactor.star`) | **Main driver** |
| 10 / 5 | Context size `>` / `<=` thresholds | **Main driver** |
| 0 | Default `cloud` | Gateway → BYOK; MITM → origin |
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
│   ├── api/                           # [S] HTTP gateway layer
│   │   ├── server.go                  # HTTP server lifecycle
│   │   ├── handlers.go                # /v1/chat/completions, /v1/models, /v1/responses
│   │   ├── responses.go               # Responses ↔ CompletionRequest translation + SSE
│   │   ├── streaming.go               # SSE / JSON chat writers
│   │   └── middleware.go              # Logging, CORS, request ID
│   │
│   ├── mitm/                          # [S] HTTPS MITM forward proxy
│   │   ├── proxy.go                   # CONNECT, TLS terminate, passthrough, blind tunnel
│   │   ├── ca.go                      # Local CA + per-host leaf certs
│   │   ├── hosts.go                   # Allowlist matcher (*.cursor.sh, …)
│   │   ├── intercept.go               # Parse body → Harness.CompleteLocal (shared engine)
│   │   └── paths.go                   # ~/.glider/mitm default paths
│   │
│   ├── config/                        # [S] Configuration management
│   │   ├── config.go                  # Config struct definitions (incl. mitm, model_aliases)
│   │   ├── loader.go                  # YAML parser + validator
│   │   └── watcher.go                 # fsnotify hot-reload
│   │
│   ├── router/                        # [S] Routing logic
│   │   ├── router.go                  # Rule evaluation engine
│   │   ├── rules.go                   # Rule types (regex, threshold, explicit)
│   │   └── starlark.go                # Starlark script executor + caching
│   │
│   ├── transform/                     # [S] Request/response transformation
│   │   ├── transformer.go             # Opt-in trim + augment
│   │   └── tokenizer.go               # BPE token counter
│   │
│   ├── orchestrator/                  # [S] Execution coordination
│   │   ├── pipeline.go                # Complete / CompleteLocal / Handle; ErrOriginPassthrough
│   │   ├── queue.go                   # Priority request queue
│   │   ├── fallback.go                # Fallback chain + circuit breaker
│   │   ├── executor.go                # Lifecycle + dispatch
│   │   └── ...
│   │
│   ├── backend/                       # [O][L][I][D] Pluggable backends
│   │   ├── interfaces.go              # Core interfaces (see §4.2)
│   │   ├── registry.go                # Backend + model registry
│   │   ├── ollama/
│   │   ├── vllm/
│   │   └── cloud/                     # openai.go, anthropic.go
│   │
│   ├── vram/                          # [S] GPU memory management
│   ├── dashboard/                     # [S] Web UI + REST (config, vram, rules, sessions)
│   │   ├── server.go / api.go
│   │   ├── discover.go                # Ollama/vLLM + nvidia-smi snapshot for /api/vram
│   │   └── static/                    # Overview, VRAM & Models, Rules Engine, Config
│   └── metrics/                       # [S] Observability
│       ├── collector.go / events.go   # Mode/Action/Host/Path/Rule/OriginalModel
│       └── history.go                 # Session JSONL under ~/.glider/history
│
├── scripts/
│   ├── examples/                      # User Starlark routing scripts
│   ├── setup-windows.ps1              # CA generate/trust + Cursor settings
│   ├── start-glider.ps1 / .bat
│   └── gen-ca.go                      # Standalone CA mint helper
│
├── configs/
│   ├── glider.yaml                    # Intro / full-system demo: MITM on; explicit → script → threshold → default cloud
│   └── glider.cloud.yaml              # Gateway default cloud BYOK, MITM off
│
├── docs/
│   └── CURSOR_CHECKLIST.md            # Manual Cursor Ask/Agent verification
│
├── e2e/                               # End-to-end tests (incl. mitm_test.go)
├── bench/                             # Proxy overhead / rule eval benches
├── go.mod
├── Makefile
├── README.md
└── STATUS.md
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
    Server       ServerConfig      `yaml:"server"`
    Thresholds   ThresholdConfig   `yaml:"thresholds"`
    VRAM         VRAMConfig        `yaml:"vram"`
    Models       []ModelConfig     `yaml:"models"`
    ModelAliases map[string]string `yaml:"model_aliases"`
    Routing      RoutingConfig     `yaml:"routing"`
    Cloud        CloudConfig       `yaml:"cloud"`
    Backends     []BackendConfig   `yaml:"backends"`
    Dashboard    DashboardConfig   `yaml:"dashboard"`
    Transform    TransformConfig   `yaml:"transform"`
    MITM         MITMConfig        `yaml:"mitm"`
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
│  Dashboard PUT /api/config ─▶ Validate + Write    │
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
│              Router          Pipeline    slog level│
│           (re-compiles     aliases +    (reloaded) │
│            Starlark)       MaxContext              │
└───────────────────────────────────────────────────┘

Uses sync/atomic.Value for lock-free reads.
Subscribers are notified via Watch callbacks (cmd/glider).
Invalid configs are rejected — old config stays active.

Hot-reload without restart: routing rules, model_aliases,
max_local_context_tokens, log_level; gpu_assignments persist
on the same Swap and are read by GET /api/vram.

Restart required: proxy_port, dashboard_port, mitm.*, backends,
cloud provider registration (wired once at process start).
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
  proxy_port: 8080        # OpenAI-compatible gateway
  dashboard_port: 8081    # Web UI
  log_level: "info"       # debug | info | warn | error

thresholds:
  max_local_context_tokens: 8000  # Above this → route to cloud (gateway) / non-local
  idle_unload_timeout: "5m"
  request_timeout: "120s"

vram:
  strategy: "hybrid"              # static | dynamic | hybrid
  headroom_mb: 512
  max_loaded_models: 3
  gpu_assignments:
    "codellama:7b": 0

models:
  - name: "codellama:7b"
    backend: "ollama"
    vram_estimate_mb: 4200
    max_context: 16384
    capabilities: ["code", "refactor", "debug"]
    keep_warm: true

# Map Cursor / OpenAI model IDs → registry names (applied before routing).
model_aliases:
  "gpt-4o": "codellama:7b"
  "gpt-4o-mini": "llama3:8b-instruct"
  "claude-3.5-sonnet": "codellama:7b"

routing:
  # Priority: explicit overrides → Starlark → context thresholds → default cloud
  rules:
    - name: "Explicit Local"
      priority: 100
      trigger: { type: "explicit", commands: ["/local", "/fast"] }
      action: { target: "local", model: "codellama:7b" }
    - name: "Explicit Cloud"
      priority: 99
      trigger: { type: "explicit", commands: ["/cloud", "/heavy"] }
      action: { target: "cloud", backend: "openai", model: "gpt-4o" }
    - name: "Script Refactor Local"
      priority: 50
      trigger: { type: "script", file: "scripts/examples/detect_refactor.star" }
      action: { target: "local", model: "codellama:7b" }
    - name: "Context Overflow"
      priority: 10
      trigger: { type: "context_size", operator: ">", value: 8000 }
      action: { target: "cloud", backend: "openai", model: "gpt-4o" }
    - name: "Small Context Local"
      priority: 5
      trigger: { type: "context_size", operator: "<=", value: 8000 }
      action: { target: "local", model: "codellama:7b" }
    - name: "Default Origin"
      priority: 0
      trigger: { type: "always" }
      action: { target: "cloud", backend: "openai", model: "gpt-4o" }
      # Gateway: BYOK. MITM CompleteLocal: ErrOriginPassthrough → Cursor upstream.

cloud:
  providers:
    - name: "openai"
      api_key_env: "OPENAI_API_KEY"
      base_url: "https://api.openai.com/v1"
    - name: "anthropic"
      api_key_env: "ANTHROPIC_API_KEY"
      base_url: "https://api.anthropic.com/v1"
  rate_limit:
    requests_per_minute: 30
    tokens_per_minute: 100000
  budget_cap_usd: 50.00

backends:
  - name: "ollama"
    type: "local"
    url: "http://localhost:11434"
    health_check_interval: "30s"
  - name: "vllm"
    type: "local"
    url: "http://localhost:8001"
    health_check_interval: "30s"

# HTTPS MITM — Cursor http.proxy points here for Agent / all models.
mitm:
  enabled: true
  port: 8082
  ca_cert: "~/.glider/mitm/ca.crt"
  ca_key: "~/.glider/mitm/ca.key"
  hosts:
    - "api2.cursor.sh"
    - "api3.cursor.sh"
    - "api4.cursor.sh"
    - "*.api5.cursor.sh"
  passthrough_default: true   # non-local → original upstream (not BYOK)

dashboard:
  enabled: true
  auth: false

transform:
  enabled: false
```

Also: `configs/glider.cloud.yaml` — same schema with `mitm.enabled: false` and default rule `target: cloud` for gateway-only BYOK users.

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
> Goal: Cursor talks to Glider gateway, Glider forwards to local backends. Streaming works.

- [x] Go project scaffold with full module structure (see §4.1)
- [x] API Gateway: `/v1/chat/completions` handler with SSE streaming
- [x] Backend interface definitions (`InferenceBackend`, `ModelManager`, `LoRAManager`, `HealthChecker`)
- [x] Ollama backend implementation (Complete, Load, Unload, Health)
- [x] vLLM backend implementation (Complete, Load, Unload, LoRA swap, Health)
- [x] Cloud backends: OpenAI + Anthropic passthrough
- [x] Backend Registry (register/discover all backends)
- [x] Simple passthrough routing (no rules yet, just default target)
- [x] Verify: Cursor → Glider → Ollama → Cursor streams correctly
- [x] Verify: Cursor → Glider → vLLM → Cursor streams correctly

### Phase 2 — Config, Router & Rules Engine
> Goal: Requests are intelligently routed based on rules. Config is hot-reloadable.

- [x] Config loader + validator (`glider.yaml`)
- [x] Config hot-reload via `fsnotify` with atomic swap
- [x] Tokenizer integration (tiktoken-go)
- [x] Rule engine core with priority ordering
- [x] Rule types: ExplicitCommandRule, RegexRule, ContextSizeRule
- [x] Starlark script executor with compiled-script caching
- [x] StarlarkScriptRule type (load `.star` files, pass request, get routing decision)
- [x] Starlib integration for regex in Starlark scripts
- [x] Verify: `/local` routes locally, large context routes to cloud

### Phase 3 — VRAM Management, Model Lifecycle & Orchestrator
> Goal: Models load/unload dynamically. Scale-to-zero, fallback, and queuing work.

- [x] VRAM Monitor (`nvidia-smi` CLI; optional `nvml.dll` on Windows still open)
- [x] Model Registry with metadata
- [x] VRAM Allocator with strategies (static / dynamic / hybrid)
- [x] VRAM headroom reservation
- [x] Model state machine (COLD → LOADING → WARM → UNLOADING)
- [x] Idle timeout unloading (`keep_alive`)
- [x] LRU eviction when VRAM is full
- [x] Multi-GPU support (GPU assignments from config)
- [x] Request Queue with priority (interactive > background)
- [x] Fallback chain (local fail → cloud)
- [x] Circuit breaker on failing backends
- [x] Health check loop for all backends
- [x] Cloud rate limiter and budget cap

### Phase 4 — Dashboard, Observability & Request Transformation
> Goal: Web UI for monitoring and configuration. Opt-in request transformation.

- [x] Dashboard HTTP server on separate port (embedded frontend via `embed`)
- [x] Tabs: Overview (sessions + request log), VRAM & Models, Rules Engine, Config
- [x] Config form primary + optional YAML; tooltips + section cards; `GET|PUT /api/config`
- [x] Rules Engine UI persists routing rules to config (hot-reloads router)
- [x] `GET /api/vram` discovery + GPU assignment UI; `PUT /api/gpu-assignments`
- [x] Soft validation (`/api/validate`) vs discovered model catalog
- [x] Metrics: Mode/Action/Host/Path/Rule/OriginalModel; session history `~/.glider/history`
- [x] WebSocket server for real-time push updates
- [x] REST API for config editing and model management
- [x] Request Transformer: opt-in context trimming + prompt augmentation
- [x] **System prompts preserved by default**
- [ ] Starlark-scriptable transforms as separate advanced surface
- [ ] Pixel-perfect mockup parity (functional UI shipped)

### Phase 5 — Integration Testing & Polish
> Goal: End-to-end stability, performance benchmarks, documentation.

- [x] End-to-end integration tests (gateway + routing + resilience)
- [x] Benchmark: proxy overhead target < 5ms on passthrough
- [x] Concurrent request coverage
- [x] Edge cases: config corruption, fallback
- [x] README, setup guide, example configs
- [x] Makefile for build/test
- [ ] `go test -race` signed off on Windows (needs CGO toolchain)

### Phase 6 — Dual-mode MITM, Responses API, Model Aliases, Shared Harness
> Goal: Agent / all Cursor models via MITM; same orchestration engine as gateway; BYOK Responses compatibility; model ID mapping.

- [x] `internal/mitm`: CONNECT, CA persist, leaf mint, host allowlist, blind tunnel
- [x] Shared `PipelineCompleter.Handle` — gateway `Complete`, MITM `CompleteLocal`
- [x] MITM non-local → `ErrOriginPassthrough` → origin TLS passthrough (not BYOK)
- [x] Interceptor uses Harness only (no bare Router shortcut)
- [x] Routing: explicit overrides → Starlark → thresholds → default cloud
- [x] Wire MITM into `cmd/glider` (`mitm.enabled`, port `8082`)
- [x] `/v1/responses` + Responses-shaped chat completions translation
- [x] `model_aliases` in config + `ApplyModelAlias` in pipeline
- [x] `configs/glider.cloud.yaml` default-cloud profile
- [x] Docs: README dual-mode, `docs/CURSOR_CHECKLIST.md`, Windows setup scripts
- [x] Unit/e2e: MITM passthrough, CompleteLocal local fulfill, Responses, aliases, threshold/script intercept
---

## 8. Verification Plan

### Automated Tests
- **Unit:** Rule evaluation, config parsing, token counting, VRAM math, MITM host match, CA mint, Responses translate, aliases.
- **Integration / E2E:** Gateway round-trips; MITM CONNECT → decrypt → upstream; local MITM intercept stub.
- **Benchmark:** Proxy overhead (target: <5ms added latency on passthrough).

### Manual Verification
- **Mode A:** Point Cursor OpenAI Base URL at `localhost:8080/v1`; `/local` and `/cloud`; Dashboard.
- **Mode B:** Trust Glider CA; `http.proxy` → `:8082`; `disableHttp2`; fully quit/relaunch; Agent with Cursor-native model passthrough; `/local` when body is chat/Responses.
- Kill Ollama mid-request (gateway) → cloud fallback.
- Edit `glider.yaml` while running → hot-reload.

See `docs/CURSOR_CHECKLIST.md`.

---

## Resolved Design Decisions

| # | Decision | Resolution |
|---|----------|------------|
| 1 | **Backend Priority** | Ollama + vLLM are both **core** from Phase 1. No stretch goals. |
| 2 | **Dashboard Scope** | Full-featured intent; shipped functional UI (Overview/VRAM/Rules/Config). Pixel-perfect mockup parity still open. |
| 3 | **Request Transformation** | **Opt-in only.** Cursor system prompts **never stripped by default**. |
| 4 | **Cursor Agent / all models** | **MITM forward proxy** required; gateway alone is insufficient. |
| 5 | **MITM non-local destination** | **Original upstream Host** (Cursor subscription), not BYOK OpenAI. |
| 6 | **Unrecognized MITM bodies** | Always **passthrough** — never break Agent. |
| 7 | **Shared harness** | Gateway and MITM both use `PipelineCompleter`; MITM uses `CompleteLocal`. |
| 8 | **Routing drivers** | Explicit `/local`/`/cloud` = overrides; **Starlark + token thresholds** = main drivers; default cloud. |
| 9 | **MITM CA scope** | Cursor-only proxy; Trusted Root install + `NODE_EXTRA_CA_CERTS` for Cursor/Node — does not rewrite normal Windows internet for non-proxy clients. |
| 10 | **Intro config** | `configs/glider.yaml` is the full-system demo profile (MITM on). |

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
