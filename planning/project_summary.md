# Glider — Project Summary

> A local AI harness that sits above Cursor in **dual mode**: an OpenAI-compatible BYOK gateway and an HTTPS MITM forward proxy. It routes simple work to local models and otherwise either uses your OpenAI/Anthropic keys **or** passthroughs to Cursor’s original upstream (`*.cursor.sh`) with auth intact.

---

## The Problem

Cursor Chat and Agent send requests to expensive cloud APIs (Cursor subscription cloud and/or OpenAI/Anthropic), burning credits even for simple tasks like adding docstrings, renaming variables, or small refactors. Meanwhile, local GPUs sit idle. There's no middleware that can:

- Intercept these requests before they hit the cloud (including **Agent / all Cursor models**, not only BYOK OpenAI)
- Route simple tasks to fast local models
- Fall back to the **original Cursor destination** (or BYOK cloud) when local is not appropriate
- Dynamically manage GPU memory without manual intervention

**Glider solves this.**

---

## Core Concept — Dual Mode

```
┌────────────┐   Mode A: Override OpenAI Base URL    ┌────────────────┐
│   Cursor   │ ─────────────────────────────────────▶│  Gateway :8080 │──▶ Local (Ollama/vLLM)
│  Chat/Ask  │   /v1/chat/completions|/v1/responses  │  (BYOK)        │──▶ Your OpenAI/Anthropic
└────────────┘                                       └────────────────┘

┌────────────┐   Mode B: http.proxy + trust Glider CA ┌────────────────┐
│   Cursor   │ ─────────────────────────────────────▶│  MITM :8082    │──▶ Local (if body known)
│ Agent/all  │   CONNECT → decrypt allowlisted hosts │  (forward)     │──▶ Original Cursor upstream
│  models    │                                       └────────────────┘    (api2.cursor.sh, …)
└────────────┘
```

| Mode | How Cursor connects | “Cloud” / non-local means |
|------|---------------------|---------------------------|
| **A — Gateway** | Override OpenAI Base URL → `http://localhost:8080/v1` | Configured OpenAI/Anthropic with **your** API keys |
| **B — MITM** | `http.proxy` → `http://127.0.0.1:8082` + trust Glider CA | **Original Host** Cursor intended (subscription traffic preserved) |

**Mode A** is ideal for Chat / Ask / Cmd+K with OpenAI-compatible models.  
**Mode B** is required for Agent and built-in Cursor models that never hit a custom OpenAI Base URL.

Dashboard: `http://localhost:8081`.

---

## Key Design Decisions Made

| # | Decision | Resolution | Rationale |
|---|----------|------------|-----------|
| 1 | **Where does Glider sit?** | Dual: OpenAI-compatible **gateway** + HTTPS **MITM** forward proxy | Gateway alone cannot see Cursor Agent / subscription models. MITM intercepts `*.cursor.sh` while preserving origin passthrough. |
| 2 | **Proxy language** | **Go** (Golang) | Single binary, high concurrency for SSE, open-sourceable. |
| 3 | **Scripting for rules** | **Starlark** | Sub-ms, sandboxed, Python-like. |
| 4 | **Inference backends** | **Ollama + vLLM** (both core) | Variants vs true LoRA hot-swap. |
| 5 | **LoRA strategy** | Dual approach | Ollama pre-baked variants; vLLM per-request adapters. |
| 6 | **Routing logic** | **One shared harness** for both modes | Explicit `/local`/`/cloud` = overrides. **Main drivers** = Starlark scripts + context token thresholds. Unrecognized MITM bodies **passthrough**. |
| 7 | **VRAM management** | Load on demand, unload after idle timeout | Scale-to-zero. |
| 8 | **Context token threshold** | Configurable primary driver | e.g. `>8000` → cloud/origin; `<=8000` → local (unless overridden). |
| 9 | **Cursor system prompts** | **Never stripped by default** | UI depends on them. |
| 10 | **Request transformation** | Opt-in trim + augment | Optional only. |
| 11 | **Dashboard** | Core from day one | Live VRAM, routing, config. |
| 12 | **Development methodology** | **TDD** | Tests define done; `go test ./...` green. |
| 13 | **Agent / Responses API** | Accept `/v1/responses` + translate Responses-shaped bodies on `/chat/completions` | Cursor Agent BYOK quirk. |
| 14 | **Model ID mapping** | `model_aliases` in `glider.yaml` | Map Cursor/OpenAI model IDs → local registry names before routing. |
| 15 | **MITM CA** | Local CA under `~/.glider/mitm/` | User trusts CA; leaf certs minted per host. |
| 16 | **MITM vs gateway engine** | Same `PipelineCompleter` | Gateway: `Complete`. MITM: `CompleteLocal` → non-local returns `ErrOriginPassthrough` → Cursor upstream. |

---

## V2 Future Goals (Swarm & Loop Engineering)

- **Swarm Delegation:** Planner local model → worker swarm → aggregate SSE to Cursor.
- **Loop Engineering:** Local lint/test reflection before Cursor sees the final stream.

---

## Tech Stack

| Component | Technology | Why |
|-----------|-----------|-----|
| Gateway + Orchestrator | **Go** | Single binary, SSE concurrency |
| HTTPS MITM | **Go `crypto/tls` + CONNECT** | Forward proxy decrypt for allowlisted hosts |
| Rule Scripting | **Starlark** (`go.starlark.net`) | Sandboxed, sub-ms |
| Regex in Scripts | **Starlib** | Starlark stdlib extension |
| Local Inference | **Ollama** + **vLLM** | Variants + LoRA |
| Config | **YAML** + **fsnotify** | Hot-reload |
| Token Counting | **tiktoken-go** | OpenAI-compatible counts |
| VRAM Monitoring | **nvidia-smi** | Windows + Linux |
| Dashboard | **gorilla/websocket** + **embed** | Live UI in one binary |
| Logging | **`log/slog`** | Stdlib |

---

## Features (Current)

### Dual-mode proxy & routing
- OpenAI-compatible gateway: `/v1/chat/completions`, `/v1/models`, `/v1/responses`
- Responses-shaped body accepted on `/v1/chat/completions` (Agent quirk)
- HTTPS MITM forward proxy (`mitm.port`, default `8082`): CONNECT, CA, allowlist, decrypt
- **Shared harness:** both modes run alias → tokenize → route → transform → execute via `PipelineCompleter`
  - Gateway: `Complete` (cloud → BYOK OpenAI/Anthropic)
  - MITM: `CompleteLocal` (cloud/non-local → `ErrOriginPassthrough` → original Cursor Host)
- Blind tunnel for non-allowlisted CONNECT hosts
- Routing priority: **explicit overrides** → **Starlark scripts** → **context thresholds** → default cloud
- Explicit `/local`, `/cloud`, `/heavy`, `/fast`; regex / context / Starlark rules
- `model_aliases` map
- Opt-in request transform (trim / augment); system prompts preserved

### Model & VRAM
- Dynamic / static / hybrid allocation, scale-to-zero, registry, LoRA (vLLM), multi-GPU, headroom, LRU
- `GET /api/vram` discovers Ollama tags / vLLM models + nvidia-smi gauges; GPU pins via UI → `vram.gpu_assignments`
- Soft catalog validation warnings (`GET|POST /api/validate`) when assignments/models don’t match discovered backends

### Resilience
- Local → cloud fallback (gateway), circuit breaker, priority queue, health checks, cloud rate/budget

### Config & ops
- `configs/glider.yaml` — **intro / full-system demo** (MITM on; scripts + thresholds drive; default cloud → MITM origin / gateway BYOK)
- `configs/glider.cloud.yaml` (gateway default cloud BYOK, MITM off)
- Hot-reload: routing rules, aliases, context thresholds, log level (GPU assignments persist via same Swap path)
- Restart required: listen ports, MITM enable/port/CA/hosts, backend URLs, cloud provider registration
- Windows setup: `scripts/setup-windows.ps1` (CA → Trusted Root + `NODE_EXTRA_CA_CERTS`). Cursor-only proxy — CA in Trusted Root does not change normal Windows internet for apps that ignore the system store / don’t use Glider’s proxy

### Observability
- Request metrics: Mode / Action / Host / Path / Rule / OriginalModel (+ tokens, latency)
- slog: MITM CONNECT `decrypt` vs `blind_tunnel`; intercept local / origin_passthrough / skip / error
- Dashboard Overview request log columns: TIME, MODE, ACTION, HOST / MODEL, RULE, LATENCY, TOKENS
- Session history under `~/.glider/history` (one session = one Glider process run); live WS still tails the current session

### Dashboard
- Tabs: Overview (sessions + live log), VRAM & Models, Rules Engine, Config
- Config: structured form primary (section cards + tooltips); Edit YAML optional/collapsed; `GET|PUT /api/config`
- Rules Engine: create/edit/enable routing rules (explicit / script / context_size / always / etc.) → persisted to config
- Cost / local-vs-cloud split metrics (functional UI; not pixel-perfect vs mockups)

---

## Architecture (Simplified)

```
                    ┌──────────────────────────────────────────┐
                    │  SHARED HARNESS (PipelineCompleter)       │
                    │  alias → tokenize → route → transform     │
                    │           → execute                       │
                    └────────────▲─────────────▲────────────────┘
                                 │             │
Mode A: Gateway :8080 ──Complete─┘             │
        → Local | BYOK Cloud                   │
                                               │
Mode B: MITM :8082 ──decrypt──CompleteLocal────┘
        → Local | ErrOriginPassthrough → Cursor origin

Dashboard :8081 ◀── Metrics (+ history store)
```

**Routing priority (both modes):**
1. Explicit `/local`, `/fast`, `/cloud`, `/heavy` (overrides)
2. Starlark scripts (main driver)
3. Context token thresholds (main driver)
4. Default `cloud` (gateway → BYOK; MITM → origin)

**MITM decrypt allowlist (default intro profile):** `api2.cursor.sh`, `api3.cursor.sh`, `api4.cursor.sh`, `*.api5.cursor.sh`. All other CONNECT hosts are blind-tunneled.

**SOLID** as before: narrow interfaces, pluggable backends, core depends on abstractions.

---

## Phased Build Plan

| Phase | Focus | Status |
|---|---|---|
| **1** | Foundation & Gateway | Implemented |
| **2** | Config, Router & Rules | Implemented |
| **3** | VRAM, Lifecycle & Orchestrator | Implemented |
| **4** | Dashboard & Transforms | Implemented (UI minimal vs mockups) |
| **5** | E2E, benches, docs | Implemented (`go test -race` optional on Windows) |
| **6** | Dual-mode MITM + Responses + aliases + **shared harness** | Implemented |

---

## TDD Approach

| Metric | Target / Current |
|---|---|
| Core phases | Phases 1–5 tests green via `go test ./...` |
| Phase 6 | MITM + Responses + aliases; `CompleteLocal` / `ErrOriginPassthrough`; threshold/script-driven routing |
| Coverage target | ≥ 80% line coverage per package |
| Race detection | `go test -race` where CGO available |

---

## Final Expected Output (How to use)

### Binary
```bash
glider.exe --config configs/glider.yaml
```

### Mode A — BYOK gateway
1. Override OpenAI Base URL → `http://localhost:8080/v1`
2. Use Chat / Ask / Cmd+K with OpenAI-path models
3. Optional overrides: `/local`, `/cloud` — otherwise scripts + token thresholds decide

### Mode B — MITM (Agent + all Cursor models)
1. Trust `~/.glider/mitm/ca.crt` (see `scripts/setup-windows.ps1`)
2. Set `NODE_EXTRA_CA_CERTS` to that CA
3. Cursor settings: `http.proxy` = `http://127.0.0.1:8082`, `proxySupport` = `override`, `disableHttp2` = `true`
4. Fully quit and relaunch Cursor
5. Use Agent normally — same harness as gateway; non-local → original Cursor upstream

### What Success Looks Like
- Agent subscription models work through MITM with origin passthrough
- Thresholds/scripts route small work local without typing `/local`
- Explicit `/local` / `/cloud` still override when needed
- Gateway BYOK path remains independent but shares the same engine
- < 5ms proxy overhead on passthrough
- Dashboard at `:8081` shows routing

---

## Reference Documents

| Document | Purpose |
|---|---|
| [implementation_plan.md](implementation_plan.md) | HLD/LLD, package layout, config schema (incl. MITM) |
| [tdd_test_plan.md](tdd_test_plan.md) | Phase tests + Phase 6 MITM/Responses/aliases |
| [../README.md](../README.md) | User setup for dual mode |
| [../docs/CURSOR_CHECKLIST.md](../docs/CURSOR_CHECKLIST.md) | Manual Cursor verification |
| [../STATUS.md](../STATUS.md) | Build status snapshot |
