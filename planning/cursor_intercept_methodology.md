# Cursor Agent interception methodology

> Research deliverable for Glider local routing when cloud is not needed.
> Date: 2026-07-18. Status: **research complete** (implementation still dual-path).
>
> Companion status doc: [`cursor_agent_protocol_interception.md`](./cursor_agent_protocol_interception.md).
> Prior art survey: [`cursor_prior_art.md`](./cursor_prior_art.md).
> Local clones for study (not a product dependency): `_research/cursor-rpc`, `_research/copilot-for-cursor`.

---

## Sources consulted (mandatory + related)

| Source | Role | Key facts used |
|--------|------|----------------|
| [everestmz/cursor-rpc](https://github.com/everestmz/cursor-rpc) (`aiserver.proto`, Connect stubs) | Schema pin for older AiService chat | `GetChatRequest` / `GetComposerChatRequest` → `stream StreamChatResponse`; pin Cursor **0.43.5** / commit `83d3631` (2024-12-17). Services in pin: **AiService**, **RepositoryService** only. **No** `BidiService`, **no** `ChatService`. |
| [jacksonkasi DEV.to](https://dev.to/jacksonkasi/how-i-reverse-engineered-cursor-ide-to-run-on-github-copilot-a-proxy-architecture-deep-dive-2jin) + [copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor) (fork studied: CharlesYWL) | Proven Agent+tools via OpenAI Base URL | `cus-` bypasses Cursor model hijack; Anthropic→OpenAI tool/schema mutation; often needs **HTTPS tunnel**; Responses API bridge for GPT-5.x |
| [eisbaw/cursor_api_demo](https://github.com/eisbaw/cursor_api_demo) (Cursor **2.6.22**) | Modern Agent plane | Primary chat: `/aiserver.v1.ChatService/StreamUnifiedChatWithTools`; hosts `api2` + `agent*.api5`; auth/checksum header profile; Connect envelope; Bidi client experimental |
| [Cursor Network Configuration](https://cursor.com/docs/enterprise/network-configuration) | Official host map + streaming constraints | `api2` most API; `api3`/`api4` Tab; `api5` / `agent*.api5` Agent + NAL; HTTP/2 bidi vs HTTP/1.1 SSE fallback; SSL inspection breaks Agent |
| [Speedscale blog](https://speedscale.com/blog/peeking-under-the-hood-of-cursor/) | Early gRPC capture | Workspace context (snippets, dir walk, git) inside ChatService dry-run / chat protos |
| Glider code | Ground truth of what ships | `internal/mitm/*`, `internal/cursorrpc/*`, `internal/api/anthropic_normalize.go`, `internal/orchestrator/pipeline.go`, `configs/glider.yaml` |

---

## A. Cursor traffic map

### A.1 Hosts (what each is for)

From Cursor’s enterprise network docs + eisbaw 2.6.22 + Glider allowlist:

| Host / pattern | Purpose | Glider MITM default (`configs/glider.yaml`) | Intercept LLM? |
|----------------|---------|---------------------------------------------|----------------|
| `api2.cursor.sh` | **Most API**: OAuth refresh, `AiService.*`, older chat, many control RPCs, Connect HealthStream | allowlisted | Yes — chat/OpenAI-shaped only |
| `api3.cursor.sh` | **Cursor Tab** (HTTP/2); eisbaw also notes telemetry-ish use | allowlisted | Usually no (Tab / control) |
| `api4.cursor.sh` (+ regional `*.gcpp.cursor.sh`) | **Cursor Tab** by region (HTTP/2) | `api4` allowlisted; regional GCP not in default list | No (Tab) |
| `api5.cursor.sh` / `*.api5.cursor.sh` | **Agent + network access layer (NAL)** | `*.api5.cursor.sh` | Yes for Agent RPCs if decrypted |
| `agent.api5.cursor.sh` | Agent (privacy mode) | covered by `*.api5` | Yes (Agent) |
| `agentn.api5.cursor.sh` | Agent (non-privacy) | covered by `*.api5` | Yes (Agent) |
| `agent.us.api5` / `agentn.us.api5` / `agent.global.api5` / `agentn.global.api5` | Regional Agent/NAL | covered by `*.api5` | Yes (Agent) |
| `repo42.cursor.sh` | Codebase indexing (HTTP/2) | **not** in default allowlist | Blind tunnel (good — heavy, non-LLM) |
| Auth hosts (`authenticate` / `authenticator` / `authentication` / `prod.authentication`) | Login / JWT | not allowlisted | Blind tunnel — **must stay origin** |
| CDN / marketplace | Updates, extensions | not allowlisted | Blind tunnel |

**Implication:** Modern Agent inference often lands on **`agent*.api5.cursor.sh`**, while classic Ask / AiService StreamChat and many control RPCs stay on **`api2.cursor.sh`**. Glider’s allowlist already covers both families; Tab/index hosts should stay blind-tunneled unless debugging.

### A.2 Protocol layers

```text
Cursor client
  → HTTP CONNECT to Glider MITM :8082   (or direct TLS if no proxy)
  → TLS 1.2+ to allowlisted host        (MITM: leaf cert from Glider CA)
  → HTTP/1.1 or HTTP/2                  (Cursor prefers h2 for Agent/Tab;
                                         enterprise proxies often force HTTP/1.1 /
                                         Cursor “HTTP Compatibility Mode”)
  → ConnectRPC / gRPC-Web style POST
       Content-Type: application/connect+proto  (or application/proto / grpc)
       Connect-Protocol-Version: 1
       Body: Connect envelopes  OR  raw protobuf (unary)
  → OR (BYOK / Override Base URL path)
       HTTPS to custom OpenAI Base URL
       JSON: /v1/chat/completions and/or /v1/responses
       Often Anthropic-shaped tools (input_schema, system, content blocks)
```

**Connect framing** (cursor-rpc / Glider / eisbaw agree):

```text
[1 byte flags][4 bytes big-endian length][payload]
  flags bit0 = compressed (gzip)
  flags bit1 = end-stream (JSON trailer, often `{}` or `{error:{code,message}}`)
```

Glider implements this in `internal/cursorrpc/connect.go` (`WriteConnectProtoFrame`, `WriteConnectEndStream`, unwrap on decode).

**HTTP/2 note:** Glider’s MITM currently speaks **HTTP/1.1** on the decrypted side (`http.ReadRequest` / `req.Write`). Cursor docs recommend HTTP/1.1 compatibility when proxies break h2 bidi. That is the practical MITM operating mode; true h2 CONNECT multiplexing is a future gap (see §E).

### A.3 Auth headers that must be preserved for origin passthrough

When MITM returns `handled=false`, Glider restores the original body and `req.Write`s upstream (`passthroughHTTPS`). **Do not strip or re-sign** Cursor auth on passthrough. Observed / documented required headers (eisbaw 2.6.22 + cursor-rpc client patterns):

| Header | Role |
|--------|------|
| `Authorization: Bearer …` | Access token (from Cursor SQLite / refresh via `POST api2…/oauth/token`) |
| `Content-Type` | `application/connect+proto` (or proto/grpc variants) |
| `Connect-Protocol-Version` | `1` |
| `x-cursor-checksum` | Time-based obfuscation + machine id (must match client) |
| `x-cursor-client-version` / `-type` / `-os` / `-arch` / `-os-version` / `-device-type` | Client fingerprint |
| `x-cursor-config-version` | Config UUID |
| `x-cursor-timezone` | IANA TZ |
| `x-ghost-mode` | Privacy flag |
| `x-session-id` | Session (often UUID-v5 of token) |
| `x-request-id` / `x-amzn-trace-id` | Correlation |

**Local fulfill path:** these headers are **not** needed toward Ollama/vLLM — Glider answers the client directly with Connect `StreamChatResponse` frames or OpenAI SSE/JSON. Never invent Cursor checksums for local responses.

**Gateway Path A:** Cursor sends whatever OpenAI-compat key it has configured; Glider ignores Cursor subscription auth for local backends.

---

## B. Exact intercept surface (checklist)

Legend: **I** = intercept (classify + try harness), **D** = decode body to harness request, **L** = can local-fulfill end-to-end today, **O** = must origin (or should).

### B.1 OpenAI-compatible / BYOK plane

| Path | Host(s) | I | D | L | O | Rationale |
|------|---------|---|---|---|---|-----------|
| `/v1/chat/completions` (and `*/chat/completions`) | Override Base URL **or** rare MITM on allowlisted host | ✓ | ✓ JSON | ✓ | if harness → cloud | Path A primary. Glider: `NormalizeAnthropicShapedJSON` + `ParseCompletionRequest` + harness |
| `/v1/responses` (and `*/responses`) | Same | ✓ | ✓ | ✓ | if cloud | Cursor may send Responses shape; gateway/MITM translate via `ResponsesToCompletion` |
| Embeddings / other `/v1/*` | Override URL | optional | — | — | usually | Out of Glider LLM harness scope today |

### B.2 AiService (cursor-rpc schema pin 0.43.5 — Glider Path B)

Connect path prefix: `/aiserver.v1.AiService/…` on `api2` (historically).

| RPC | I | D | L | O | Rationale |
|-----|---|---|---|---|-----------|
| `StreamChat` | ✓ | ✓ `GetChatRequest` | ✓ text stream | if cloud / empty | Glider decode + `WriteStreamChatResponse` |
| `StreamChatWeb` | ✓ | ✓ same | ✓ | if cloud | Same codec |
| `StreamChatTryReallyHard` | ✓ | ✓ same | ✓ | if cloud | Same codec |
| `StreamNotepadChat` | ✓ | ✓ `GetNotepadChatRequest` ≈ chat | ✓* | if cloud | Path matched; uses GetChat-shaped decode path |
| `StreamComposer` | ✓ | ✓ `GetComposerChatRequest` | ✓ text | if cloud | Text-only `StreamChatResponse`; **no** tool/edit encoding |
| `StreamComposerContext` | ✓ classify | ✗ | ✗ | ✓ | Different request type — explicitly excluded |
| `StreamChatToolformer` / `Continue` | ✓ classify | partial possible | ✗ | ✓ today | Needs `StreamChatToolformerResponse` (thoughts/tool actions) — **not encoded** |
| `StreamEdit` / `StreamFastEdit` / `StreamInlineEdits` / `SlashEdit*` | classify as agent or control | ✗ | ✗ | ✓ | Different response messages |
| `StreamCpp` / Tab-adjacent | control-ish | ✗ | ✗ | ✓ | Tab plane — leave origin |
| `AvailableModels` / `GetUserInfo` / Health* | control | ✗ | ✗ | ✓ | Auth/billing/model catalog — origin |
| `WarmChatCache` / index / task / docs RPCs | control | ✗ | ✗ | ✓ | Side-channel — origin |

\*Notepad: classified fulfillable; treat as chat text stream.

**Request fields useful for harness (from `GetChatRequest`):**
`model_details.model_name`, `conversation[]` (HUMAN/AI text), `explicit_context`, `current_file` (path + contents), `request_id`, `desired_max_tokens`, flags (`is_composer`, `long_context_mode`, `use_web`).

**Minimal response Cursor accepts for text chat (proven in Glider):** streaming `StreamChatResponse{ text }` frames + Connect end-stream `{}`.

### B.3 Modern Agent plane (eisbaw 2.6.22 — **not** in cursor-rpc pin)

| RPC / path | Host | I | D | L | O | Rationale |
|------------|------|---|---|---|---|-----------|
| `ChatService/StreamUnifiedChatWithTools` | `api2` and/or `agent*.api5` | ✓ (marker) | ✗ | ✗ | ✓ | Primary modern Agent+tools stream; schema newer than pin |
| `BidiService/BidiAppend` | api2 / api5 | ✓ | ✗ | ✗ | ✓ | Multiplex / SSE-poll fallback; opaque bytes |
| `BidiService/BidiPoll` | same | ✓ | ✗ | ✗ | ✓ | Companion to Append |
| Other `ChatService/*` (e.g. dry-run) | api2 | ✓/control | ✗ | ✗ | ✓ | Context packing / pricing — origin |
| Client-side tool result posts | Agent hosts | ✓ | ✗ | ✗ | ✓ | Without unified codec, cannot close tool loop locally |

Glider behavior today: classify `agent_rpc` → `DecodeChatRequest` returns nil → metrics `agent_rpc_opaque` → **origin passthrough** (correct fail-soft).

### B.4 Control / analytics (skip)

| Marker / service | I | D | L | O |
|------------------|---|---|---|---|
| `DashboardService`, `AnalyticsService`, `MetricsService`, `TelemetryService` | skip_control | ✗ | ✗ | ✓ |
| `AuthService`, plugins, `BackgroundComposerService` list, `CppService`, `FileService`, `CmdKService` | skip | ✗ | ✗ | ✓ |
| Heartbeats / `report` / `GetEffectiveUserPlugins` | skip | ✗ | ✗ | ✓ |

### B.5 Summary matrix (operator view)

| Surface | Intercept? | Decode? | Local fulfill today? | Must origin? |
|---------|------------|---------|----------------------|--------------|
| Override URL + `cus-*` chat/completions | Yes (gateway) | Yes | Yes (text + partial tools JSON) | When harness says cloud |
| MITM `/v1/*` on allowlisted host | Yes | Yes | Yes | When cloud |
| MITM AiService StreamChat* / StreamComposer | Yes | Yes | Yes (text only) | When cloud / empty / fail |
| MITM Toolformer / edits / CPP | Classify | No | No | Yes |
| MITM ChatService unified / Bidi* | Classify | No | No | Yes |
| Dashboard / analytics / auth | Skip | No | No | Yes |

---

## C. Two methodologies compared

### C.1 Path A — Gateway / Override OpenAI Base URL + `cus-` (article-proven)

**What Cursor does**

1. User sets Models → OpenAI-compatible **Override Base URL** (+ API key).
2. User picks a model id. If the id matches a **known** Cursor model (`claude-…`, `gpt-4o`, …), Cursor’s router **hijacks** the request to `api2.cursor.sh` / Agent hosts and ignores the Override URL.
3. If the id is **unknown** (e.g. `cus-codellama:7b`), Cursor treats it as custom BYOK and POSTs OpenAI-shaped (often Anthropic-flavored) JSON to the Override URL.
4. Agent/Composer still run **client-side tool loops**; the model only sees tool schemas + results over that HTTP API.
5. Some Cursor builds require **HTTPS** for Override URL (copilot-for-cursor uses Cloudflare/ngrok tunnel). HTTP `127.0.0.1` often works on desktop but is version-sensitive.
6. Some models/paths emit **Responses API** bodies (`/v1/responses`) instead of chat.completions — proxy must bridge or translate.

**What the proxy must mutate** (Glider + article)

| Step | Mutation |
|------|----------|
| 1 | Strip `cus-` / `glider-` → real model id (`NormalizeGatewayModel`) |
| 2 | Flatten Anthropic `system` → `messages[0]` |
| 3 | Flatten content blocks (`text` / `tool_result`) → string content |
| 4 | Tools: `input_schema` → OpenAI `function.parameters`; strip `additionalProperties`, `$schema`, `title` |
| 5 | (Gap) Full Anthropic `tool_use` ↔ OpenAI `tool_calls` / role=`tool` — partial in Glider vs full in copilot-for-cursor `anthropic-transforms.ts` |
| 6 | Stream OpenAI SSE (or Responses SSE) back so Cursor’s Agent UI continues |

**Streaming format:** standard OpenAI chat.completions SSE (`data: {choices:[{delta:{content}}]}`) or Responses events after translation. Tools appear as `tool_calls` deltas when the local/cloud model supports them.

**Tools:** Client owns execution (edit_file, terminal, MCP). Proxy only needs to preserve tool **definitions** and **call/result** message shapes. Glider’s `CompletionRequest` currently has **no `Tools` field** — normalize cleans JSON for parsing, but harness/backends may drop tools on re-emit (see §E).

**Strengths:** Full modern Agent+tools without protobuf archaeology; same harness as other gateways.  
**Weaknesses:** Override URL is often **global** (forum: breaks mixing Cursor Pro + custom); `cus-` discipline required; HTTPS tunnel friction; Anthropic/Responses edge cases.

### C.2 Path B — MITM Connect/protobuf (cursor-rpc)

**What Cursor does**

1. Subscription / built-in models talk to Cursor hosts with Connect protobuf.
2. Older/simple chat: `AiService/StreamChat*` with `GetChatRequest`.
3. Modern Agent: `ChatService/StreamUnifiedChatWithTools` and/or `BidiService/BidiAppend`+`BidiPoll` (HTTP/2 bidi or SSE fallback).

**Frame / message requirements for local accept**

| Direction | Requirement |
|-----------|-------------|
| Request | Unwrap Connect envelope → `proto.Unmarshal` into `GetChatRequest` or `GetComposerChatRequest` |
| Response CT | `application/connect+proto` |
| Response body | Repeated data frames: marshaled `StreamChatResponse` with `text` set from chunk content |
| End | End-stream frame with `{}` or structured Connect error JSON |
| Failure | Prefer Connect end-stream error (`FailClosedConnect`) over hanging |

**Glider implementation:** `handleAgentRPC` → `DecodeChatRequest` → `CompleteLocal` → either `WriteStreamChatResponse` or restore bytes + origin.

**Strengths:** Works with built-in Cursor models / subscription traffic; preserves Cursor auth on passthrough; no `cus-` UX.  
**Weaknesses:** Schema pin is **~1.5 years behind** modern Agent; tool loops need Toolformer/Unified codecs; MITM is HTTP/1.1; certificate trust + `disableHttp2` operational cost.

### C.3 Which methodology for Glider (decision)

| Goal | Primary method |
|------|----------------|
| **Local when not needed for Agent + tools (today)** | **Path A** |
| Text-only Ask/Chat on legacy StreamChat while still on subscription models | Path B (already shipped for StreamChat*) |
| Full local Agent on built-in models without Override URL | Path B **future** (need ChatService/Bidi schemas from live capture or newer extract) |
| Mix: small prompts local, heavy/cloud tools origin | Path A for BYOK models; Path B passthrough for opaque Agent RPCs on subscription models |

**Recommended primary methodology: Path A (gateway + `cus-`), with Path B as complementary MITM for fulfillable AiService text RPCs and safe opaque passthrough.**

---

## D. Decision algorithm — “local when not needed”

Shared harness: `PipelineCompleter.Handle`  
- Gateway: `Complete` → local **or** cloud BYOK execute  
- MITM: `CompleteLocal` → local execute **or** `ErrOriginPassthrough` (origin TLS, auth intact)

### D.1 Inputs available after decode/normalize

| Signal | Source | Notes |
|--------|--------|-------|
| Model id | JSON `model` or `ModelDetails.model_name` | After `cus-`/`glider-` strip + aliases |
| Estimated tokens | `Tokenizer.EstimateRequestTokens` | Drives context_size rules |
| Explicit commands | `/local`, `/fast`, `/cloud`, `/heavy` in user text | Highest priority in default `glider.yaml` |
| Script triggers | Starlark (e.g. refactor detect) | Mid priority |
| Adapter tag | `cursor_agent_rpc` on Path B | Observability; can become a rule input later |
| Tools present | Path A JSON only (not on `CompletionRequest` yet) | **Should** force cloud or specialized local tool-capable model once carried |
| RPC kind | StreamChat vs opaque | Opaque never reaches harness |

### D.2 Mapping default `glider.yaml` rules → Agent traffic

```text
priority order (first match wins):
  100 Explicit /local|/fast     → CompleteLocal / local execute
   99 Explicit /cloud|/heavy    → gateway: cloud BYOK
                                 MITM: ErrOriginPassthrough
   50 Script (refactor, …)      → local (if match)
   10 context_size > 8000       → cloud / passthrough
    5 context_size ≤ 8000       → local
    0 always                    → cloud / passthrough
```

### D.3 Pseudocode for Agent paths

```text
on POST:
  kind = ClassifyPath(path)

  if kind == cursor_control or other:
    → origin (skip)

  if kind == openai_compat:          # Path A or rare MITM JSON
    body = NormalizeAnthropicShapedJSON(body)
    req = ParseCompletion or ResponsesToCompletion
    req.Model = NormalizeGatewayModel(req.Model)
    // FUTURE: if tools non-empty && local model lacks tools → force cloud/passthrough
    return harness(req)

  if kind == agent_rpc:
    decoded = DecodeChatRequest(path, body)
    if decoded == nil:               # Bidi, ChatService, …
      metric agent_rpc_opaque
      → origin                       # MUST
    if no messages:
      → origin
    req = decoded.Request (+ adapter cursor_agent_rpc)
    chunks, err = CompleteLocal(req)
    if ErrOriginPassthrough:
      → origin (original bytes)
    if !Fulfillable():               # should not happen for StreamChat*
      FailClosedConnect
    WriteStreamChatResponse(chunks)  # text-only
```

### D.4 When cloud **is** needed (force origin / cloud)

- Harness rule says `target: cloud` (explicit `/cloud`, overflow, default).
- Opaque Agent RPC (cannot decode / cannot encode tools).
- Tool-heavy Agent turn on Path B (Toolformer/Unified) — **always origin today**.
- Path A with tools but local backend cannot emit `tool_calls` — should passthrough/cloud (once tools are first-class).
- Auth, AvailableModels, billing, Tab, indexing — always origin.

---

## E. Gaps vs Glider today

| Capability | Status | Evidence |
|------------|--------|----------|
| Gateway OpenAI chat + Responses | **Implemented** | `handlers.go`, responses translate |
| `cus-` / `glider-` strip | **Implemented** | `NormalizeGatewayModel` |
| Anthropic system/content/tools clean | **Partial** | `anthropic_normalize.go`; missing full tool_use↔tool_calls / tool_choice / thinking strip vs copilot-for-cursor |
| `CompletionRequest.Tools` + cloud re-emit | **Missing** | `backend.CompletionRequest` has no Tools |
| MITM CONNECT decrypt + host allowlist | **Implemented** | `proxy.go`, `hosts.go`, yaml |
| MITM OpenAI JSON local/passthrough | **Implemented** | `handleOpenAI` |
| MITM StreamChat* / StreamComposer decode+local | **Implemented** | `cursorrpc` + `handleAgentRPC` |
| MITM opaque Agent metrics | **Implemented** | `agent_rpc_opaque` |
| ChatService / Bidi / Unified tools codec | **Missing** | Not in cursor-rpc 0.43.5 pin; eisbaw has newer |
| StreamChatToolformer encode | **Missing** | Proto exists in pin; Glider does not fulfill |
| HTTP/2 MITM | **Missing** | HTTP/1.1 only — rely on Cursor HTTP/1.1 mode |
| Auth header mutation / checksum | **N/A local**; passthrough preserves | Correct |
| HTTPS tunnel helper for Path A | **Missing** | Document workaround (cloudflared) |
| Live capture / schema refresh tooling | **Missing** | Upstream `make extract-schema` is macOS Cursor.app |
| CURSOR_CHECKLIST Path B banner | **Updated** | Text-only Bidi+RunSSE when `agent_rpc_fulfill`; tools → Path A |

### Prioritized next engineering steps

**Path B — “Fulfill everything” (modern Agent on MITM)** — see protocol doc §Fulfill everything. Ordered:

1. **Extract** BidiAppend context_envelope → `CompletionRequest` (TipTap nested field 2).
2. **Observe RunSSE responses** (not just requests) — required before rewrite; cursor-tap is observe-only reference.
3. **DecideLocal hook** + `would_fulfill_local` while response codec missing (safe origin passthrough).
4. **MVP text fulfill** into RunSSE once codec is known (**hardest**; no public proof).
5. **Tools loop** on Bidi / child RunSSE — or keep tools on Path A initially.
6. Keep **origin passthrough** correct by default.

**Path A / shared**

1. **Path A tools first-class** — carry `tools` / tool_calls through `CompletionRequest` → Ollama/OpenAI backends; expand Anthropic transform to match copilot-for-cursor parity.
2. **Live path capture** — **shipped** as `mitm.debug_agent_rpc` / `GLIDER_MITM_DEBUG_RPC=1` (+ experimental `mitm.agent_rpc_fulfill`).
3. **Schema refresh** — re-extract or vendor eisbaw/ChatService / cursor-tap protos; extend fulfill paths only when response codec is known.
4. **Routing signal: tools / adapter** — if tools present or `adapter=cursor_agent_rpc` with large context → prefer cloud/passthrough unless model capability says tools-ok.
5. **Ops polish** — Path A HTTPS tunnel docs; keep `disableHttp2: true` in checklist.
6. **HTTP/2 MITM** — only if capture shows Agent refuses HTTP/1.1 through Glider (Cursor usually falls back).

---

## F. Practical user recipe

### F.1 Working **today** — local routing for Agent-ish work (Path A)

**Glider**

```bash
# Ollama with models from configs/glider.yaml
ollama pull codellama:7b
go build -o glider.exe ./cmd/glider
.\glider.exe --config configs\glider.yaml
# Gateway :8080  Dashboard :8081  MITM :8082 (optional for this recipe)
```

**Cursor Settings → Models**

1. Enable OpenAI-compatible / Override OpenAI Base URL: `http://127.0.0.1:8080/v1`  
   - If Cursor rejects HTTP: `cloudflared tunnel --url http://127.0.0.1:8080` and use `https://….trycloudflare.com/v1`.
2. API key: any non-empty string (e.g. `glider`).
3. Model name **must** be prefixed: `cus-codellama:7b` (or `cus-gpt-4o` if you rely on `model_aliases` → local).
4. Prefer Agent/Composer with that custom model selected (not a built-in Claude/GPT id).

**Harness behavior**

- Prompt `/local` or small context (≤8000 tok) → Ollama.
- `/cloud` or large context → OpenAI BYOK (`OPENAI_API_KEY`) on gateway — **not** Cursor subscription.
- Watch dashboard / logs for route decisions.

**Limits today:** tool loops may degrade if tools are dropped after normalize; use simpler Agent turns or cloud-capable upstream until step E.1 lands.

### F.2 Working **today** — subscription Agent stays cloud; MITM safe (Path B ops)

1. Trust Glider CA (`~/.glider/mitm/ca.crt`); set `NODE_EXTRA_CA_CERTS`.
2. Cursor `settings.json`: `http.proxy` → `http://127.0.0.1:8082`, `http.proxySupport`: `override`, **HTTP Compatibility → HTTP/1.1** / `disableHttp2`.
3. Built-in Agent models → expect `agent_rpc_opaque` / passthrough → Cursor cloud (must keep working).
4. If any traffic still hits `AiService/StreamChat*` with small prompts + `/local`, MITM can fulfill locally as Connect text stream.

### F.2b Path B R&D — observe opaque Agent RPCs (no local fulfill)

```yaml
# configs/glider.yaml
mitm:
  enabled: true
  debug_agent_rpc: true
```

Or: `$env:GLIDER_MITM_DEBUG_RPC="1"` then restart Glider.

After an Agent turn: check slog `mitm agent rpc debug`, files in `%USERPROFILE%\.glider\mitm-debug\`, and `GET http://127.0.0.1:8081/api/mitm/debug/recent`. Headers are redacted; body dumps are hex previews only. Origin passthrough is unchanged.

### F.3 After next milestones

| Milestone | User-visible unlock |
|-----------|---------------------|
| Tools on `CompletionRequest` + transform parity | Path A Agent+tools reliably local |
| ChatService/Bidi codec | Built-in models local without Override URL |
| Capability-aware routing | Auto “tools → cloud, chat → local” without `/cloud` |

### F.4 Minimal config snippet (Path A emphasis)

```yaml
# configs/glider.yaml — already sufficient for Path A
server:
  proxy_port: 8080
routing:
  rules:
    - name: Explicit Local
      priority: 100
      trigger: { type: explicit, commands: ["/local", "/fast"] }
      action: { target: local, model: codellama:7b }
    - name: Small Context Local
      priority: 5
      trigger: { type: context_size, operator: "<=", value: 8000 }
      action: { target: local, model: codellama:7b }
model_aliases:
  # After cus- strip, Cursor-ish names can map to local
  gpt-4o: codellama:7b
mitm:
  enabled: true   # optional for Path A-only users
  hosts: [api2.cursor.sh, api3.cursor.sh, api4.cursor.sh, "*.api5.cursor.sh"]
```

Cursor model picker: **`cus-codellama:7b`** or **`cus-gpt-4o`**.

---

## Appendix — cursor-rpc pin inventory (fulfillable subset)

**Module:** `github.com/everestmz/cursor-rpc` @ `83d363192331` — Cursor stamp **0.43.5**.

**Services in proto:** `AiService` (large), `RepositoryService`.

**Glider fulfillable paths** (`IsFulfillableAgentPath`):

- `/aiserver.v1.AiService/StreamChat`
- `/aiserver.v1.AiService/StreamChatWeb`
- `/aiserver.v1.AiService/StreamChatTryReallyHard`
- `/aiserver.v1.AiService/StreamNotepadChat`
- `/aiserver.v1.AiService/StreamComposer` (not `StreamComposerContext`)

**Response message used:** `aiserver.v1.StreamChatResponse` field `text = 1` (+ ignored optional citation/status fields).

**Explicitly out of pin / out of Glider codec:** `BidiService`, `ChatService`, `StreamUnifiedChatWithTools`, modern api5 Agent multiplex.

---

## Appendix — Prior art / related projects

> Full survey (10+ projects, ranked): [`cursor_prior_art.md`](./cursor_prior_art.md).

**Ecosystem summary (2026-07):** Successful Agent→local/alternate model setups are almost all **Override OpenAI Base URL + unknown model prefix** (`cus-` / `fx-` / `custom-`). Public **MITM** work (notably [burpheart/cursor-tap](https://github.com/burpheart/cursor-tap), [LaiKash/cursor-aiserver-interceptor](https://github.com/LaiKash/cursor-aiserver-interceptor)) decrypts and **decodes** `BidiAppend` / `RunSSE` / Connect frames but **forwards to Cursor origin** — no proven open local-fulfill of modern Agent gRPC. Reverse gateways ([eisbaw/cursor_api_demo](https://github.com/eisbaw/cursor_api_demo), [kaitranntt/ccs](https://github.com/kaitranntt/ccs), [timxx/Cursor-To-OpenAI](https://github.com/timxx/Cursor-To-OpenAI)) use Cursor *as* the model upstream.

| Project | Approach | Glider takeaway |
|---------|----------|-----------------|
| jacksonkasi / CharlesYWL [copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor) | Override + `cus-` + Anthropic tools | Path A reference |
| [0xSero/factory-cursor-bridge](https://github.com/0xSero/factory-cursor-bridge) | Override + `fx-` multi-provider | Same Path A family |
| [burpheart/cursor-tap](https://github.com/burpheart/cursor-tap) | MITM observe RunSSE↔BidiAppend | Best modern Agent decode; not local fulfill |
| [eisbaw/cursor_api_demo](https://github.com/eisbaw/cursor_api_demo) | Client Unified/Bidi (2.6.22) | Schema gap vs cursor-rpc pin |
| [everestmz/cursor-rpc](https://github.com/everestmz/cursor-rpc) | AiService StreamChat client | Glider Path B pin |
| [punnerud/cursor_ollama_proxy](https://github.com/punnerud/cursor_ollama_proxy) | Override → Ollama | Minimal Path A Ollama |
| Speedscale writeup | Early ChatService capture | Context packing notes |
