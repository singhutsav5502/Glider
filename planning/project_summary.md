# Glider — Project Summary

> Local AI harness above Cursor. **Dual mode:** OpenAI-compatible BYOK gateway + HTTPS MITM forward proxy.  
> **Status authority:** [planning/README.md](README.md) (Done / Partial / Next). As of **2026-07-18**.

---

## The Problem

Cursor Chat and Agent burn cloud credits on simple work while local GPUs sit idle. There was no middleware that could:

- Intercept Chat **and** Agent (not only BYOK OpenAI)
- Route simple work to local models
- Fall back to **Cursor origin** (subscription) or BYOK cloud when appropriate
- Manage VRAM without hand-holding

**Glider does that** (gateway + MITM + shared harness). Path B Agent tools still origin; use Path A + `cus-` for Agent+tools.

---

## Dual mode

```
┌────────────┐   Mode A: Override OpenAI Base URL    ┌────────────────┐
│   Cursor   │ ─────────────────────────────────────▶│  Gateway :8080 │──▶ Local (Ollama/vLLM)
│  Chat/Ask  │   /v1/chat/completions|/v1/responses  │  (BYOK)        │──▶ Your OpenAI/Anthropic
└────────────┘                                       └────────────────┘

┌────────────┐   Mode B: http.proxy + trust Glider CA ┌────────────────┐
│   Cursor   │ ─────────────────────────────────────▶│  MITM :8082    │──▶ Local (Path B text /
│ Agent/all  │   CONNECT → decrypt allowlisted hosts │  (forward)     │    OpenAI JSON)
│  models    │                                       └────────────────┘──▶ Cursor origin (else)
└────────────┘
```

| Mode | Connect | Non-local means |
|------|---------|-----------------|
| **A — Gateway** | Base URL → `http://localhost:8080/v1` | Your OpenAI/Anthropic keys |
| **B — MITM** | `http.proxy` → `:8082` + Glider CA | **Original Host** (subscription auth intact) |

Dashboard: `http://localhost:8081`.

---

## Done / Partial / Next (summary)

| | |
|--|--|
| **Done** | Dual-mode + shared harness; Path B text fulfill; `/cloud` hard-force; turn-family + summary + subagent sticky; `contextgraph` MVP; Path A tools + classifier; dashboard analytics LOCAL/CLOUD/CANNED %; orchestrator/VRAM; swarm stubs (flag-off) |
| **Partial** | Path B tool loops (origin + would_*); Episode store not wired to every fulfill; FanOut not productized; hot-swap incomplete for backends/MITM; Loop Engineering hoops MVP (stages + eval) |
| **Next** | See [README.md](README.md) P0–P2 backlog |

---

## Key design decisions

| # | Decision | Resolution |
|---|----------|------------|
| 1 | Where Glider sits | Gateway **+** MITM |
| 2 | Language | Go |
| 3 | Rules | Starlark |
| 4 | Backends | Ollama + vLLM |
| 5 | Routing | One shared harness; explicit → sticky → classifier → Starlark → ceiling → default |
| 6 | VRAM | Load on demand / scale-to-zero |
| 7 | System prompts | Never stripped by default |
| 8 | Transforms | Opt-in only |
| 9 | Dashboard | Core from day one |
| 10 | Methodology | TDD (`go test ./...`) |
| 11 | Agent BYOK | `/v1/responses` + Responses-shaped chat bodies |
| 12 | Model IDs | `model_aliases` |
| 13 | MITM CA | `~/.glider/mitm/` |
| 14 | Non-local MITM | `CompleteLocal` → `ErrOriginPassthrough` |
| 15 | Path B Agent | Text-only when `agent_rpc_fulfill`; tools → prefer Path A |

---

## Features (current)

### Proxy & routing
- Gateway: `/v1/chat/completions`, `/v1/models`, `/v1/responses`
- MITM: CONNECT, CA, allowlist, blind tunnel for other hosts
- Shared: alias → tokenize → route → transform → execute
- Explicit `/local` `/cloud` `/heavy` `/fast` (TipTap-safe)
- Task classifier + role hints; `tool_followup`
- Path B turn-family sticky + `user_visible_high_level_summary` chrome sticky
- `model_aliases`; opt-in transform

### Path A tools
- `tools` / `tool_choice` + message tool_calls round-trip
- Anthropic → OpenAI normalize; stream `tool_calls` on SSE
- Tools-unsupported → cloud fallback; default `tools_force_cloud`

### Path B Agent (MITM)
- StreamChat* fulfill (legacy)
- BidiAppend extract → hub → RunSSE text fulfill
- Sticky: summary/title/subagent; consults `contextgraph`
- Child/tool RunSSE → origin (`tool_followup_would_local` only)

### Ops
- Profiles: `configs/glider.yaml` (MITM on), `glider.cloud.yaml`
- Hot-reload: rules, aliases, threshold, log, GPU assignments
- Restart: ports, MITM, backends, cloud providers
- Metrics + history `~/.glider/history`; context `~/.glider/context/`
- Dashboard: Overview, VRAM, Rules, Config

---

## Architecture (simplified)

```
                    ┌──────────────────────────────────────────┐
                    │  SHARED HARNESS (PipelineCompleter)       │
                    │  alias → tokenize → route → transform     │
                    │           → execute (+ optional Graph)    │
                    └────────────▲─────────────▲────────────────┘
                                 │             │
Mode A: Gateway :8080 ──Complete─┘             │
                                               │
Mode B: MITM :8082 ──decrypt──CompleteLocal────┘
        → Local | ErrOriginPassthrough → Cursor origin
```

**Routing priority:** explicit → turn-family sticky (Path B) → tool_followup → task_classifier → Starlark → token ceiling → default cloud.

---

## Phased build (historical)

| Phase | Focus | Status |
|-------|-------|--------|
| 1–5 | Gateway through E2E/benches | Done |
| 6 | MITM + Responses + aliases + shared harness | Done |
| Post-6 | Path B text, classifier, tools, sticky, contextgraph, swarm stubs | Done / partial — see [README.md](README.md) |

---

## How to use

```bash
glider.exe --config configs/glider.yaml
```

- **Mode A:** Override Base URL → `http://localhost:8080/v1`; Agent+tools use `cus-…` models
- **Mode B:** Trust CA + `http.proxy` → `:8082`; `agent_rpc_fulfill: true` for text Agent; tools stay origin

Success: local offload for simple turns; `/cloud` never interrupted by local summary chrome; dashboard shows LOCAL/CLOUD/CANNED %.

---

## Reference

| Doc | Purpose |
|-----|---------|
| [README.md](README.md) | Index + backlog |
| [implementation_plan.md](implementation_plan.md) | HLD/LLD |
| [cursor_agent_protocol_interception.md](cursor_agent_protocol_interception.md) | Path A/B Agent |
| [../STATUS.md](../STATUS.md) | Build status |
| [../README.md](../README.md) | User setup |
