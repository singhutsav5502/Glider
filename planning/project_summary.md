# Glider — Project Summary

> A local AI harness that sits above Cursor, intercepts requests, and intelligently routes them between local models and cloud APIs — saving cost, optimizing VRAM, and maintaining real-time performance.

---

## The Problem

Cursor Chat sends every request to expensive cloud APIs (OpenAI, Anthropic), burning credits even for simple tasks like adding docstrings, renaming variables, or small refactors. Meanwhile, local GPUs sit idle. There's no middleware that can:

- Intercept these requests before they hit the cloud
- Route simple tasks to fast local models
- Dynamically manage GPU memory without manual intervention
- Fall back to cloud only when truly necessary

**Glider solves this.**

---

## Core Concept

```
┌────────────┐          ┌────────────────┐          ┌──────────────┐
│   Cursor   │  ──────▶ │    GLIDER      │  ──────▶ │ Local GPU    │
│   Chat     │  HTTP    │  (Proxy +      │  routes  │ (Ollama/vLLM)│
│            │ ◀─────── │   Orchestrator)│ ◀─────── │              │
└────────────┘  SSE     │                │          └──────────────┘
                        │  Falls back ──────────▶  ┌──────────────┐
                        │  only when needed        │ Cloud APIs   │
                        └────────────────┘         │ (OpenAI etc) │
                                                   └──────────────┘
```

**How it works:** Cursor is configured to send API requests to `localhost:8080` instead of `api.openai.com`. Cursor thinks it's talking to OpenAI. Glider intercepts, evaluates rules, and routes locally or to cloud. Cursor never knows the difference.

---

## Key Design Decisions Made

These were resolved through iterative discussion and research:

| # | Decision | Resolution | Rationale |
|---|----------|------------|-----------|
| 1 | **Where does Glider sit?** | Above Cursor, as a transparent proxy | Avoids Cursor credit costs. No Cursor modifications needed. |
| 2 | **Proxy language** | **Go** (Golang) | Single binary, excellent memory management, high concurrency for SSE streaming, easily open-sourceable. |
| 3 | **Scripting for rules** | **Starlark** (Python dialect embedded in Go) | Sub-millisecond execution, fully sandboxed (no filesystem/network access), Python-like syntax. |
| 4 | **Inference backends** | **Ollama + vLLM** (both core) | Ollama: simple model variant switching. vLLM: true per-request LoRA hot-swapping. |
| 5 | **LoRA strategy** | Dual approach | Ollama can't hot-swap LoRAs (must pre-bake into model variants). vLLM can (sub-ms on cache hit). Both supported. |
| 6 | **Routing logic** | Explicit (direct precedence) + Implicit (regex, thresholds, Starlark scripts) | Explicit commands like `/local` always win. Implicit rules fill the gaps. |
| 7 | **VRAM management** | Load on demand, unload after idle timeout (scale-to-zero) | Models consume VRAM only when actively serving. Configurable `keep_alive` timeout. |
| 8 | **Context token threshold** | Exposed as configurable parameter | User sets the cutoff (e.g., >8000 tokens → cloud). |
| 9 | **Cursor system prompts** | **Never stripped by default** | Cursor's UI depends on its system prompt formatting to parse responses. Stripping breaks the UI. |
| 10 | **Request transformation** | Opt-in only: context trimming + prompt augmentation | Trims middle context when oversized. Augments with user-defined instructions. Both optional. |
| 11 | **Dashboard** | Full-featured from day one | Real-time VRAM gauge, config editor, rule editor, model management, cost tracker. Not a stretch goal. |
| 12 | **Development methodology** | **TDD (Test-Driven Development)** | 91 tests + 4 benchmarks defined upfront. Tests written before code. Phase is done when all tests pass. |

---

## V2 Future Goals (Swarm & Loop Engineering)

Glider's architecture is designed to evolve into a multi-agent ecosystem. Future V2 enhancements include:

- **Swarm Delegation:** A "heavy" local model acts as a planner, delegating sub-tasks to a swarm of fast, specialized local "worker" models before synthesizing the final response.
- **Loop Engineering:** Implementing local reflection and iterative testing. Glider will be able to test generated code in a local loop, refining it multiple times before returning the final, polished response to Cursor.

---

## Tech Stack

| Component | Technology | Why |
|-----------|-----------|-----|
| Proxy/Orchestrator | **Go** | Single binary, low memory, high concurrency |
| Rule Scripting | **Starlark** (`go.starlark.net`) | Sandboxed Python-like scripts, sub-ms |
| Regex in Scripts | **Starlib** (`qri-io/starlib`) | Starlark standard library extension |
| Local Inference (simple) | **Ollama** | Easy model management, `keep_alive`, `num_gpu` |
| Local Inference (LoRA) | **vLLM** | Per-request LoRA hot-swap, PagedAttention |
| Config Format | **YAML** (`gopkg.in/yaml.v3`) | Human-readable, supports complex nesting |
| Config Hot-Reload | **fsnotify** | Filesystem watcher, atomic config swap |
| Token Counting | **tiktoken-go** | BPE tokenizer matching OpenAI's counting |
| VRAM Monitoring | **nvidia-smi** CLI | Cross-platform (Windows + Linux) |
| Dashboard Real-time | **gorilla/websocket** | WebSocket push for live updates |
| Frontend Embedding | **Go `embed`** | Bundle HTML/JS/CSS into single binary |
| Logging | **`log/slog`** (stdlib) | Structured logging, zero dependencies |

---

## All Planned Features

### Proxy & Routing
- OpenAI-compatible proxy (`/v1/chat/completions`, `/v1/models`)
- SSE streaming passthrough with < 5ms overhead
- Explicit routing commands (`/local`, `/cloud`, `/heavy`)
- Implicit routing via regex, keywords, context-size thresholds
- Custom Starlark scripting for advanced routing logic
- Configurable context token threshold
- Request transformation (opt-in context trimming & augmentation)

### Model & VRAM Management
- Dynamic VRAM allocation (static / dynamic / hybrid strategies)
- Scale-to-zero: unload after configurable idle timeout
- Model Registry with VRAM footprint, context window, capability metadata
- Pre-baked LoRA variants (Ollama) + true LoRA hot-swap (vLLM)
- Multi-GPU support with per-model GPU assignments
- VRAM headroom reservation
- LRU eviction when VRAM is full

### Resilience & Performance
- Fallback chain: Local → Cloud
- Circuit breaker on failing backends
- Priority request queue (interactive > background)
- Health checks for all backends
- Cloud rate limiting and budget caps

### Configuration
- Single `glider.yaml` file
- Hot-reload on file change (no restart)
- Pluggable backend interface (SOLID: add backends without modifying core)

### Dashboard (Web UI)
- Real-time VRAM gauge (per-GPU, per-model)
- Live request log with routing decisions, latency, token counts
- Model management panel (load/unload/switch)
- Rule editor with Starlark script support
- Configuration editor (thresholds, VRAM allotments)
- Cost savings tracker (local tokens vs. estimated cloud cost)
- WebSocket-driven real-time push updates

---

## Architecture (Simplified)

```
CURSOR ──▶ API Gateway ──▶ Tokenizer ──▶ Router ──▶ Execution Layer ──▶ Backend
               │                         (rules)      (Executor)           │
               │                            │              │           ┌────┴────┐
               │                        Starlark      VRAM Mgr       Local   Cloud
               │                        Scripts       Model Reg      (Ollama  (OpenAI
               │                                      Queue           vLLM)   Anthropic)
               │
          Dashboard ◀── WebSocket ◀── Metrics Collector
          (Web UI)
```

*The Router evaluates rules to produce a `RoutingDecision` with a specific `Strategy` (Single, Fan-Out, Pipeline). This decision is passed to the Execution Layer (implementing the `Executor` interface), which handles the orchestration before hitting the backends.*

**SOLID principles enforced throughout:**
- **S**: Each component has exactly one job
- **O**: New backends/rules added via interfaces, not by modifying core
- **L**: Any `InferenceBackend` implementation is interchangeable
- **I**: `LoRAManager` is separate from `InferenceBackend` — Ollama doesn't implement it
- **D**: Core depends on abstractions, never on concrete Ollama/vLLM types

---

## Phased Build Plan

| Phase | Focus | Key Deliverable |
|---|---|---|
| **1** | Foundation & Proxy | Cursor ↔ Glider ↔ Ollama/vLLM/Cloud streaming works end-to-end |
| **2** | Config, Router & Rules | Requests route based on rules. Config hot-reloads. Starlark scripts execute. |
| **3** | VRAM, Model Lifecycle & Orchestrator | Models load/unload dynamically. Scale-to-zero. Fallback chain. Priority queue. |
| **4** | Dashboard & Observability | Full Web UI: live VRAM, config editor, rule editor, cost tracker. Opt-in transforms. |
| **5** | Integration Testing & Polish | E2E tests, benchmarks (< 5ms overhead), stress tests, documentation. |

---

## TDD Approach

| Metric | Target |
|---|---|
| Total tests defined | **91 tests + 4 benchmarks** |
| Coverage target | ≥ 80% line coverage per package |
| Race detection | `go test -race` must pass with zero warnings |
| Phase completion | ALL tests for a phase must be GREEN before moving on |

**Workflow per feature:**
```
Write test (RED) → Implement code (GREEN) → Refactor → Next test
```

---

## Final Expected Output

When Glider is complete, the user will have:

### A Single Binary
```bash
glider.exe    # ~15-20MB, everything bundled
```

### That Does This
1. **Start it:** `./glider --config glider.yaml`
2. **Point Cursor at it:** Set Cursor's API URL to `http://localhost:8080/v1`
3. **Use Cursor normally:** Chat, Cmd+K — everything works as before
4. **But now:**
   - Simple tasks (docstrings, refactors, renames) → **handled by your local GPU for free**
   - Complex tasks (architecture, large codebase analysis) → **forwarded to cloud only when needed**
   - Models load into VRAM on-demand and unload when idle → **GPU free for gaming/other work**
   - Open `localhost:8081` → **live dashboard showing VRAM, routing, cost savings**
   - Edit `glider.yaml` → **changes take effect instantly, no restart**
   - Write a `.star` script → **custom routing logic in Python-like syntax**

### What Success Looks Like
- **Cost:** 60-80% reduction in Cursor cloud API spend
- **Latency:** < 5ms proxy overhead (user doesn't notice Glider exists)
- **VRAM:** Models consume GPU memory only when serving, free it when idle
- **Modularity:** Add a new inference backend by implementing one Go interface
- **Reliability:** Backend crashes don't break Cursor — cloud fallback activates automatically

---

## Reference Documents

| Document | Purpose |
|---|---|
| [Implementation Plan](file:///C:/Users/Utsav/.gemini/antigravity/brain/dd367367-3e5e-404e-8f1b-f72391f4cc0e/implementation_plan.md) | Full HLD/LLD with interfaces, config schema, state machines, dependency map |
| [TDD Test Plan](file:///C:/Users/Utsav/.gemini/antigravity/brain/dd367367-3e5e-404e-8f1b-f72391f4cc0e/tdd_test_plan.md) | 91 tests with Given/When/Then, organized by phase, defining "done" |
