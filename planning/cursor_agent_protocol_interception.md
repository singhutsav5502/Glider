# Cursor Agent protocol interception

> Status: **research complete** — full methodology in [`cursor_intercept_methodology.md`](./cursor_intercept_methodology.md).
> Prior art survey: [`cursor_prior_art.md`](./cursor_prior_art.md).
>
> Related: live capture notes in [`cursor_agent_rpc_debug_findings.md`](./cursor_agent_rpc_debug_findings.md) (2026-07-18: api2 BidiAppend + `agent.v1.AgentService/RunSSE`).
> Routing / local tools roadmap: [`smart_routing_and_local_tools.md`](./smart_routing_and_local_tools.md).
>
> Implementation: dual-path (Path A gateway primary; Path B MITM StreamChat* partial).
>
> Related code: `internal/mitm/`, `internal/cursorrpc/`, `internal/api/anthropic_normalize.go`.
> Attribution: `internal/cursorrpc/THIRD_PARTY.md` (cursor-rpc has **no published LICENSE**; schemas only).

---

## Sources (merged)

| Source | What it provides | How Glider uses it |
|--------|------------------|--------------------|
| [everestmz/cursor-rpc](https://github.com/everestmz/cursor-rpc) | Go + Connect stubs for `aiserver.v1` (`AiService.StreamChat`, `GetChatRequest`, `StreamChatResponse`, Composer, …). Pin Cursor **0.43.5**. | In-process decode/encode for fulfillable Agent chat RPCs on MITM |
| [jacksonkasi DEV.to](https://dev.to/jacksonkasi/how-i-reverse-engineered-cursor-ide-to-run-on-github-copilot-a-proxy-architecture-deep-dive-2jin) / [copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor) | Proven **Agent → OpenAI-compat HTTP**: Override Base URL + `cus-` + Anthropic→OpenAI tool mutation | Gateway Mode A — **recommended primary** for Agent+tools today |
| [eisbaw/cursor_api_demo](https://github.com/eisbaw/cursor_api_demo) (2.6.22) | Modern `ChatService/StreamUnifiedChatWithTools`, `agent*.api5`, auth/checksum, Bidi | Gap analysis — not in cursor-rpc pin; opaque passthrough |
| [Cursor network docs](https://cursor.com/docs/enterprise/network-configuration) | Official host roles (api2/3/4/5) | MITM allowlist rationale |

Critical design point: modern Cursor **Agent with tools** can be forced off Cursor hosts onto a custom OpenAI Base URL via `cus-…`. That traffic is JSON (often Anthropic-flavored), not BidiAppend. Full Agent tool loops are primarily a **gateway transform** problem; MITM protobuf covers subscription AiService text chat that still hits the Connect plane.

---

## Dual-path architecture (current)

```text
Path A — Gateway (article-proven) ★ PRIMARY
  Cursor (cus-MODEL + Override Base URL) → Glider :8080/v1
       → NormalizeAnthropicShapedJSON (cus- strip, system/content/tools)
       → Complete / harness → local or cloud BYOK

Path B — MITM Connect (cursor-rpc + experimental Bidi/RunSSE fulfill)
  Cursor → MITM decrypt api2 / *.api5
       → ClassifyPath agent_rpc
       → DecodeChatRequest (StreamChat* / StreamComposer)
       → CompleteLocal → local StreamChatResponse Connect stream
                      → OR ErrOriginPassthrough (original bytes unchanged)
       → BidiAppend context_envelope (experimental):
            ExtractBidiCompletionRequest → DecideLocal
            → AgentFulfillHub.ArmLocal (corr request UUID)
       → RunSSE root (experimental, agent_rpc_fulfill):
            Wait hub ≤800ms → CompleteLocal → WriteRunSSETextResponse
            (no origin body); tool-loop child RunSSE → origin
       → other Bidi / ChatService: opaque → origin
         (debug: dumps + RunSSE response peeks / .bin)
```

---

## Product intent vs reality

| Intent | Reality (2026-07) |
|--------|-------------------|
| Intercept Agent chats on Cursor hosts | TLS CONNECT decrypt works; **StreamChat\*** decode+local fulfill shipped |
| Parse prompt → shared harness | StreamChat* yes; **Bidi context_envelope → CompletionRequest** (TipTap) |
| Route small/simple → local; else origin | StreamChat* yes; **Bidi+RunSSE text MVP** when `agent_rpc_fulfill` (canned proven in UI; Ollama when backends work) |
| Full Agent tools on subscription plane | Incomplete — child RunSSE skipped; prefer Path A |
| Agent tools via BYOK | Prefer Path A (`cus-` + gateway); Anthropic tool normalize **partial** |

---

## Path classification

| Kind | Examples | MITM behavior |
|------|----------|---------------|
| `openai_compat` | `/v1/chat/completions`, `/v1/responses` | JSON (+ Anthropic normalize) → harness |
| `agent_rpc` fulfillable | `AiService/StreamChat`, `StreamChatWeb`, `StreamChatTryReallyHard`, `StreamNotepadChat`, `StreamComposer` | cursor-rpc decode → harness → Connect `StreamChatResponse` **or** origin |
| `agent_rpc` opaque / experimental | `BidiService/BidiAppend`, `agent.v1.AgentService/RunSSE` | Extract+hub+RunSSE text fulfill when flag on; else opaque → origin |
| `cursor_control` | Dashboard / Analytics / … | `skip_control` → origin |

---

## Implementation status

### Done

- [x] Depend on `github.com/everestmz/cursor-rpc` (+ connect/protobuf); attribute in `THIRD_PARTY.md`
- [x] `internal/cursorrpc`: Connect frame helpers, `DecodeChatRequest`, `WriteStreamChatResponse`, fail-closed end-stream errors
- [x] MITM `handleAgentRPC`: decode → `CompleteLocal` → local Connect stream / passthrough / opaque
- [x] Gateway + MITM OpenAI path: `cus-`/`glider-` strip; Anthropic system/content/tools normalize (article)
- [x] Unit tests: decode fixtures, Connect encode, MITM local vs passthrough, opaque Bidi metrics
- [x] Deep methodology research doc (`cursor_intercept_methodology.md`)
- [x] Path B R&D debug: `mitm.debug_agent_rpc` / `GLIDER_MITM_DEBUG_RPC`, dumps, `/api/mitm/debug/recent`
- [x] Phase 1 extract: `ExtractBidiCompletionRequest` (TipTap nested field 2 → `CompletionRequest`)
- [x] Phase 2 deep: RunSSE response classify (`text_delta`/`heartbeat`/`turn_ended`/…) + `*_RESP.bin`
- [x] Phase 3+4 experimental: `agent_rpc_fulfill` → hub → `WriteRunSSETextResponse` (text-only; canned proven in Cursor UI)
### Still incomplete

- [ ] **Tool loops / child RunSSE** — not fulfilled on MITM (prefer Path A + `cus-`); `tool_followup_would_local` logged
- [ ] **Live Ollama Path B** on every install (depends on healthy local backends; leave `agent_rpc_canned_on_error: false`)
- [ ] **BidiService / ChatService Unified** — schema not in pinned cursor-rpc; needs newer extract or capture
- [ ] **Toolformer / client-side tool RPCs** — child RunSSE / tool_call frames not fulfilled (Path A)
- [x] **Gateway tools passthrough** — `CompletionRequest.Tools` + stream tool_calls SSE bridge
- [ ] **Auth headers / checksum** — MITM passthrough preserves client headers; local fulfill does not re-sign (N/A for local)
- [ ] Re-extract schema against current Cursor (upstream `make extract-schema` assumes macOS Cursor.app)

### Path B R&D observability (debug dumps)

Use this to confirm which Connect paths modern Agent traffic hits (`BidiAppend` / `RunSSE` / …) and to verify fulfill logs (`mitm runsse local fulfill`).

1. Enable (either):
   ```yaml
   mitm:
     debug_agent_rpc: true
     agent_rpc_fulfill: true
     # leave false so Path B returns real Ollama (or origin on error)
     agent_rpc_canned_on_error: false
     # optional: debug_dump_dir: ~/.glider/mitm-debug
   ```
   or env: `GLIDER_MITM_DEBUG_RPC=1` / `GLIDER_MITM_AGENT_RPC_FULFILL=1`
2. Restart Glider; trust CA + Cursor proxy as usual (HTTP/1.1).
3. Send an Agent chat on a built-in / subscription model.
4. Inspect:
   - Logs: `mitm agent rpc debug`; `mitm runsse local fulfill` / `runsse_canned` when Path B answers
   - With `agent_rpc_canned_on_error: true` / `GLIDER_MITM_AGENT_RPC_CANNED=1`: CompleteLocal failure → canned RunSSE text instead of origin — codec dry-run only
   - Files: `~/.glider/mitm-debug/*.txt` (redacted headers + hex preview; RunSSE **response** peeks when debug on)
   - API: `GET http://127.0.0.1:8081/api/mitm/debug/recent` (recent ring + per-path counts + mitm metrics)

Default passthrough remains for tool-loop / uncorrelated RPCs. Text-only Path B fulfill is **on** when `agent_rpc_fulfill` is true.

---

## How to test with Cursor

See methodology **§F** for full recipes. Short version:

### Path A (recommended for Agent + tools)

1. Run Glider gateway (`:8080`).
2. Override OpenAI Base URL → `http://127.0.0.1:8080/v1` (HTTPS tunnel if required).
3. Model **must** use prefix: `cus-codellama:7b` / `cus-gpt-4o`.
4. Watch harness decisions (`/local`, context thresholds).

### Path B (MITM subscription / text-only Agent)

1. Install Glider MITM CA; proxy Cursor at `:8082`; allowlist includes `api2` + `*.api5`.
2. Force HTTP/1.1 compatibility.
3. `agent_rpc_fulfill: true` — text-only BidiAppend→RunSSE can local-fulfill; tool loops / child RunSSE → origin.

---

## “Fulfill everything” (Path B modern Agent) — ordered plan

> Goal: local/cloud harness for **modern** Cursor Agent (`BidiAppend` writes + `RunSSE` reads), not observe-only.
> Honesty: public prior art ([cursor-tap](https://github.com/burpheart/cursor-tap), LaiKash) **decrypts + decodes then forwards to origin**. No shipped open MITM that answers RunSSE/Bidi with a local model. Glider StreamChat* fulfill is legacy; this plan is the modern plane.

Live traffic (2026-07 Capture 5): api2 `BidiAppend` + `agent.v1.AgentService/RunSSE`; user text recoverable from context_envelope nested field **2** (TipTap). See [`cursor_agent_rpc_debug_findings.md`](./cursor_agent_rpc_debug_findings.md).

| Phase | Deliverable | Difficulty | Status |
|------:|-------------|------------|--------|
| **1** | **Request extract:** `BidiAppend` context_envelope → `CompletionRequest` (messages + model hint) | Medium — TipTap/printable heuristics; not a full schema | **Done** (`ExtractBidiCompletionRequest`) |
| **2** | **Observe RunSSE response** stream (assistant tokens + tool-call frames) — dump/decode before any rewrite | Medium-Hard — long-lived Connect stream; must not stall passthrough | **Done** (deep classify + `*_RESP.bin` / `_frames.txt`) |
| **3** | **Decision hook:** extract → harness decide (`DecideLocal` / metrics) even while still origin | Easy once Phase 1 works | **Done** (`bidi_fulfill_armed` / hub) |
| **4** | **Local fulfill MVP:** text-only reply encoded as RunSSE `AgentServerMessage` stream | **Hard** — live UI acceptance still unverified | **Shipped experimental** (`WriteRunSSETextResponse` + RunSSE hijack) |
| **5** | **Tool calls:** client-side tools loop on Bidi field-14 stdout / child RunSSE | Hard — defer tools to Path A (`cus-` + gateway) | Deferred (child RunSSE skipped) |
| **6** | **Origin passthrough** remains correct when cloud/rules say so (default off for experimental fulfill) | Easy — already the default | **Must not regress** |

### Phase details

1. **Extract** — Only `role_guess=context_envelope` (inner top field 1). Nested field 2 → TipTap `"type":"text","text":"…"` (and printable fallback) → `messages[{role:user}]`. Model from inspect strings / section labels. Unit-test with synthetic fixtures mirroring dumps (`ping-glider-*`).
2. **Response Observe** — Debug tees first N KB of RunSSE response: Connect frames, gunzip, classify `text_delta` / `heartbeat` / `token_delta` / `turn_ended` / checkpoint. Writes `*_RESP.bin` + `_frames.txt`.
3. **Decision** — Flag `mitm.agent_rpc_fulfill`: extract → `DecideLocal` → `AgentFulfillHub.ArmLocal` (corr via Bidi/RunSSE request UUID). Metric `bidi_fulfill_armed` (+ legacy `would_fulfill_local`).
4. **MVP text fulfill** — Root `RunSSE` waits ≤800ms for hub offer; on local: `CompleteLocal` → `WriteRunSSETextResponse` (heartbeat + empty checkpoint + text_delta + token_delta + turn_ended); **does not** forward origin body. Fail-soft → origin on timeout / CompleteLocal passthrough / errors before write. **Live Cursor UI acceptance not yet proven** — restart + short Agent chat required.
5. **Tools** — Child RunSSE with `X-Parent-Agent-Tool-Call-Id` always origin. Prefer Path A for Agent+tools.
6. **Passthrough** — Default `agent_rpc_fulfill: false`. Opaque paths without extract remain `agent_rpc_opaque` → origin.

### Enable (experimental)

```yaml
mitm:
  debug_agent_rpc: true          # dumps + RunSSE response peek (+ .bin)
  agent_rpc_fulfill: true        # BidiAppend → hub → RunSSE text fulfill
```

Env: `GLIDER_MITM_DEBUG_RPC=1`, `GLIDER_MITM_AGENT_RPC_FULFILL=1`. Restart Glider after changing flags / rebuilding `glider.exe`.

### Parallel Path A (still primary for Agent+tools)

1. Carry `tools` on `CompletionRequest` + Anthropic transform parity.
2. Capability-aware routing (tools → cloud unless local model supports tools).

---

## Near-term next steps (ordered)

1. **Live verify Phase 4** — restart new `glider.exe`, send short root Agent message (no tools); look for `mitm runsse local fulfill` / `action:runsse_local`. If UI blank/errors, capture fuller `*_RESP.bin` and refine checkpoint/text frames.
2. Improve empty `conversation_checkpoint_update` (origin sends large gzip checkpoints).
3. Path A tools first-class on `CompletionRequest`.
4. Path B tool_call frames only after text MVP proven in UI.

---

## Non-goals

- Mutating unknown Bidi frames.
- Claiming a license grant from unlicensed cursor-rpc beyond schema interoperability attribution.
- Replacing gateway Mode A — it remains the supported path for full Agent+tools (article architecture).
- Shipping a fake RunSSE encoder before live response Observe confirms the wire shape.
