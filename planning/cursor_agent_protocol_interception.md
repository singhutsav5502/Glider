# Cursor Agent protocol interception

> **Status (2026-07-18):** Path A **production path** for Agent+tools. Path B **text-only fulfill shipped** (BidiAppend → RunSSE) with turn-family sticky + `contextgraph`. Tool loops / child RunSSE still **origin**.
>
> Index: [README.md](./README.md) · Policy: [routing_session_policy.md](./routing_session_policy.md) · Methodology (frozen research): [cursor_intercept_methodology.md](./cursor_intercept_methodology.md) · Captures: [cursor_agent_rpc_debug_findings.md](./cursor_agent_rpc_debug_findings.md) · Prior art: [cursor_prior_art.md](./cursor_prior_art.md)
>
> Code: `internal/mitm/`, `internal/cursorrpc/`, `internal/contextgraph/`, `internal/api/anthropic_normalize.go`

---

## Sources (merged)

| Source | What it provides | How Glider uses it |
|--------|------------------|--------------------|
| [everestmz/cursor-rpc](https://github.com/everestmz/cursor-rpc) | Go + Connect stubs (`AiService.StreamChat*`, …). Pin **0.43.5**. | Legacy StreamChat* decode/encode on MITM |
| [jacksonkasi / copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor) | Agent → OpenAI-compat via `cus-` + Anthropic→OpenAI tools | **Path A primary** for Agent+tools |
| [eisbaw/cursor_api_demo](https://github.com/eisbaw/cursor_api_demo) | Modern Unified / Bidi schemas | Gap analysis; not in cursor-rpc pin |
| [burpheart/cursor-tap](https://github.com/burpheart/cursor-tap) | MITM observe/decode modern Agent | Architecture notes; observe-only prior art |
| [Cursor network docs](https://cursor.com/docs/enterprise/network-configuration) | Host roles | MITM allowlist |

Modern Agent+tools can be forced onto a custom Base URL via `cus-…` (JSON, often Anthropic-flavored). Subscription Connect plane (BidiAppend + RunSSE) is Path B.

---

## Dual-path architecture (current)

```text
Path A — Gateway ★ PRIMARY for Agent+tools
  Cursor (cus-MODEL + Override Base URL) → Glider :8080/v1
       → NormalizeAnthropicShapedJSON
       → Complete / harness → local or BYOK cloud
       → stream tool_calls re-emitted on SSE

Path B — MITM Connect (text Agent + sticky)
  Cursor → MITM decrypt api2 / *.api5
       → ClassifyPath
       → StreamChat*: decode → CompleteLocal → Connect stream | origin
       → BidiAppend: ExtractBidiCompletionRequest → DecideLocal / sticky
            → AgentFulfillHub.ArmLocal | ArmOrigin (corr UUID + turn family)
       → RunSSE root: Wait hub → CompleteLocal → WriteRunSSETextResponse
            OR origin (cloud sticky / tools / fail-soft)
       → Child / tool-loop RunSSE → origin (+ tool_followup_would_local)
```

---

## Product intent vs reality

| Intent | Reality |
|--------|---------|
| Intercept Agent on Cursor hosts | TLS decrypt works; StreamChat* + **Bidi/RunSSE text** fulfill |
| Parse prompt → harness | TipTap / LatestUserTurnText extract |
| Small → local; else origin | Classifier + sticky; `/cloud` hard-force |
| Full Agent tools on subscription plane | **Incomplete** — prefer Path A |
| Agent tools via BYOK | Path A shipped |

---

## Path classification

| Kind | Examples | MITM behavior |
|------|----------|---------------|
| `openai_compat` | `/v1/chat/completions`, `/v1/responses` | JSON → harness |
| `agent_rpc` fulfillable | `AiService/StreamChat*` / StreamComposer | cursor-rpc → harness or origin |
| `agent_rpc` Path B modern | `BidiAppend`, `agent.v1.AgentService/RunSSE` | Text fulfill when `agent_rpc_fulfill`; sticky/origin otherwise |
| `cursor_control` | Dashboard / Analytics / … | `skip_control` → origin |

---

## Implementation status

### Done

- [x] cursor-rpc dependency + `THIRD_PARTY.md`
- [x] `internal/cursorrpc` Connect helpers + StreamChat* fulfill
- [x] Gateway/MITM OpenAI: `cus-` strip; Anthropic normalize
- [x] Debug dumps + `/api/mitm/debug/recent`
- [x] `ExtractBidiCompletionRequest` (TipTap / latest user turn)
- [x] RunSSE response classify + RESP peeks
- [x] `agent_rpc_fulfill` hub → `WriteRunSSETextResponse` (canned UI-proven)
- [x] `/cloud` TipTap hard-force; never canned
- [x] Turn-family sticky (~90s); summary/title follow-ons
- [x] Composer chrome: `user_visible_high_level_summary` → `bidi_sticky_cloud_summary`
- [x] Subagent sticky (`bidi_sticky_cloud_child`)
- [x] Sticky consults `contextgraph` (`ResolveCloudSticky` / live `RunSSEOpen`)
- [x] Path A tools + stream tool_calls SSE
- [x] `tool_followup` metrics on Path B child steps

### Incomplete

- [ ] Path B **tool loops / child RunSSE** fulfill (codec)
- [ ] Unified ChatService schema in pin (newer extract if needed)
- [ ] Re-extract schema vs current Cursor (upstream assumes macOS Cursor.app)
- [ ] Live Ollama on every install (env-dependent; leave canned off for real backends)

---

## Path B phases (modern Agent)

| Phase | Deliverable | Status |
|------:|-------------|--------|
| 1 | BidiAppend → `CompletionRequest` | **Done** |
| 2 | Observe RunSSE responses | **Done** |
| 3 | Decision hook + hub arm | **Done** |
| 4 | Text-only local RunSSE fulfill | **Done** (experimental flag; canned UI-proven) |
| 5 | Tool calls / child RunSSE | **Deferred** (Path A) |
| 6 | Origin passthrough when cloud/rules say so | **Done** (must not regress) |

### Enable

```yaml
mitm:
  debug_agent_rpc: true
  agent_rpc_fulfill: true
  agent_rpc_canned_on_error: false   # prefer real Ollama
```

Env: `GLIDER_MITM_DEBUG_RPC=1`, `GLIDER_MITM_AGENT_RPC_FULFILL=1`. Restart after flag/binary change.

---

## How to test

### Path A (Agent + tools)

1. Gateway `:8080`; Override Base URL → `http://127.0.0.1:8080/v1`
2. Model prefix: `cus-codellama:7b` / `cus-gpt-4o`
3. Watch tool_calls on SSE + dashboard CLASS chips

### Path B (text Agent + sticky)

1. CA + proxy `:8082`; HTTP/1.1; `agent_rpc_fulfill: true`
2. Short root Agent message → expect `runsse_local` or origin fail-soft
3. `/cloud …` → origin; follow-on summary / `user_visible_high_level_summary` → sticky cloud (**no** `runsse_local`)
4. Next user turn without flag → re-decide (may local)

Policy checklist: [routing_session_policy.md](./routing_session_policy.md).

---

## Next steps (Path B / G13)

| Pri | Item |
|-----|------|
| **P0** | Manual Cursor verify (checklist) for text fulfill + `/cloud` wrap-up |
| **P1** | Checkpoint/codec hardening from origin RESP when UI flakes |
| **P2** | Feature-flagged child RunSSE / tool frames (only if Path A insufficient) |

Non-goals: mutate unknown Bidi frames; replace Path A for tools; claim public prior art for Path B tool fulfill (none exists — see prior art).
