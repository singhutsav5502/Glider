# Prior art / related projects

> Web research for Glider local routing (2026-07-18).
> Companion: [`cursor_intercept_methodology.md`](./cursor_intercept_methodology.md).
>
> **Honest headline:** Almost everyone who successfully runs Cursor Agent on local/alternate models uses **Override OpenAI Base URL + an unknown model prefix** (`cus-`, `fx-`, `custom-`, …). **MITM of modern Agent gRPC exists for observation/decoding**, but **no public project clearly demonstrates local fulfill / reroute of BidiAppend + ChatService/RunSSE tool loops** the way Glider Path B aspires to. Reverse clients (Cursor-as-upstream) are common; MITM-as-local-LLM is rare.

---

## Verdict for Glider

| Question | Answer |
|----------|--------|
| Has someone else done this? | **Yes** — dozens of Override Base URL proxies; a handful of MITM decoders; several Cursor→OpenAI reverse gateways. |
| Does anyone truly MITM modern Agent → local LLM? | **Not publicly proven.** Best MITM work (**cursor-tap**, **LaiKash**) decrypts and **parses** `BidiAppend` / `RunSSE` / Connect frames, then **forwards to origin**. No shipped “replace model with Ollama” path on that plane. |
| What actually works for Agent+tools? | **Path A:** Override Base URL + prefix + Anthropic→OpenAI tool transforms (copilot-for-cursor lineage). Client still owns tools. |
| What’s reusable now? | Path A transforms + HTTPS tunnel ops; cursor-tap proto extract / RunSSE↔BidiAppend architecture notes; eisbaw Unified/Bidi schemas for Path B *future*. |

---

## Ranked by usefulness to Glider

### Tier 1 — Steal patterns now (Path A / transforms)

| # | Project / post | Approach | What works | License / activity | Reusable for Glider |
|---|----------------|----------|------------|--------------------|---------------------|
| 1 | [jacksonkasi DEV.to](https://dev.to/jacksonkasi/how-i-reverse-engineered-cursor-ide-to-run-on-github-copilot-a-proxy-architecture-deep-dive-2jin) + [jacksonkasi1/copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor) (+ [CharlesYWL fork](https://github.com/CharlesYWL/copilot-for-cursor)) | **Override Base URL** + `cus-` strip + Anthropic→OpenAI tools + Responses bridge; ngrok/cloudflare HTTPS | **Agent+tools** (edit, terminal, search, MCP) via client tool loop; Copilot upstream | No LICENSE on main; active ~2026-04; ~29★ | **Primary Path A reference.** Full `anthropic-transforms.ts` / Responses bridge parity targets for Glider. |
| 2 | [0xSero/factory-cursor-bridge](https://github.com/0xSero/factory-cursor-bridge) | Override URL + **`fx-`** prefix + multi-provider config + OpenAI↔Anthropic translate + tunnel | BYOK multi-backend into Cursor (incl. Ollama-shaped URLs) | No LICENSE; created 2026-03; ~86★ | Same architecture as Glider Path A with alternate prefix convention; multi-provider routing ideas. |
| 3 | [xFurti/cursor-custom-provider](https://github.com/xFurti/cursor-custom-provider) | Override URL + **`custom-`** prefix + auto HTTPS tunnel (ngrok/cloudflare/pinggy) | BYOK to arbitrary OpenAI-compat; documents “private network forbidden” | Check repo; active enough to be cited in 2026 guides | Tunnel automation + prefix discipline docs; SSRF/localhost constraints. |
| 4 | [AdnanSattar/cursor-openrouter-proxy](https://github.com/AdnanSattar/cursor-openrouter-proxy) | LiteLLM gateway + Cloudflare tunnel as Override URL | Chat/coder/vision aliases; documents Agent/tool format breakage and Override side-effects | Check repo | Ops notes: prefer stable named tunnel; disable unused Cursor models; Override can break native models. |
| 5 | [rinadelph/CursorCustomModels](https://github.com/rinadelph/CursorCustomModels) | Flask/FastAPI-style OpenAI-compat proxy → Anthropic/Gemini/Groq/Ollama | Chat + streaming; optional auto-continuation after tools | Check repo | Older multi-provider mapping pattern; Ollama as one backend. |

### Tier 2 — Schema / Agent plane research (Path B future)

| # | Project | Approach | What works | License / activity | Reusable for Glider |
|---|---------|----------|------------|--------------------|---------------------|
| 6 | [burpheart/cursor-tap](https://github.com/burpheart/cursor-tap) | **MITM CONNECT** + CA; proto extract from Cursor JS; Connect frame parse; WebUI | **Observe/decode** modern traffic: `AiService/RunSSE`, `BidiService/BidiAppend`, StreamCpp, telemetry. **Does not reroute to local models.** | No LICENSE; Go; ~375★; pushed ~2026-03; notes updated | **Best public MITM of modern Agent.** Architecture: RunSSE = long read stream; BidiAppend = short writes (user + tool results). Steal proto extract + frame mirroring (zero-block forward). |
| 7 | [eisbaw/cursor_api_demo](https://github.com/eisbaw/cursor_api_demo) | **Client** (not MITM): HTTP/2 Connect to Cursor 2.6.22 | `ChatService/StreamUnifiedChatWithTools`, auth/checksum, AvailableModels; Bidi client **experimental** | No LICENSE; ~70★; pushed ~2026-04 | Modern Unified + tool enum field numbers; confirms api5 / agent hosts; gap vs cursor-rpc 0.43.5 pin. |
| 8 | [everestmz/cursor-rpc](https://github.com/everestmz/cursor-rpc) | Proto extract + Go Connect client for Cursor backend | `AiService/StreamChat*` etc. for **non-Cursor editors** talking *to* Cursor cloud | No LICENSE; last push **2024-12**; pin **0.43.5**; ~60★ | **Already vendored by Glider** for Path B StreamChat. No Bidi/ChatService. |
| 9 | [LaiKash/cursor-aiserver-interceptor](https://github.com/LaiKash/cursor-aiserver-interceptor) + [blog](https://laikash.com/learning-and-tech/reversing/cursor-api-emulation/) | mitmproxy `--mode local` + cursor-rpc protos | **Decode** AiService (esp. StreamCpp); log grpc frames | **GPL-3.0**; ~16★; pushed 2025-04 | MITM decode recipe on Windows; Tab/StreamCpp focus — observe-only, older surface. |
| 10 | [kaitranntt/ccs](https://github.com/kaitranntt/ccs) CursorExecutor | OpenAI/Anthropic local daemon → **Cursor cloud** via protobuf | Chat via `AiService/StreamChat` (+ newer schema work in tree); checksum “Jyh cipher” | **MIT**; very active; ~2.7k★ | Reverse of Glider intent (consume Cursor, don’t replace). Checksum/header builders + StreamUnified field map useful if Path B encodes. |
| 11 | [timxx/Cursor-To-OpenAI](https://github.com/timxx/Cursor-To-OpenAI) | OpenAI-compat **server** wrapping Cursor Agent HTTP/2 bidi | Agent tools executed **locally** while model runs on **Cursor** | Check repo; low visibility | Opposite direction; documents `isAgentic` / `ClientSideToolV2` / Unified stream loop. |

### Tier 3 — Local Ollama / simple Override (chat-oriented)

| # | Project | Approach | What works | License / activity | Reusable for Glider |
|---|---------|----------|------------|--------------------|---------------------|
| 12 | [punnerud/cursor_ollama_proxy](https://github.com/punnerud/cursor_ollama_proxy) | Flask OpenAI→Ollama + Docker + ngrok | Chat completions + stream + `/logs` UI | **MIT**; pushed 2025-04; ~31★ | Minimal Path A Ollama bridge; traffic viz idea. |
| 13 | [Alexeyisme/CursorOllamaBridge](https://github.com/Alexeyisme/CursorOllamaBridge) | Shell/ngrok tunnel straight to Ollama `/v1` | Expose local Ollama as HTTPS Override URL | Check repo; thin | Tunnel-only recipe; Basic Auth on ngrok. |
| 14 | [S1M0N38/re-cursor](https://github.com/S1M0N38/re-cursor) | mitmproxy dump/redact scripts | Capture HTTP flows for analysis | **MIT**; 2025-04 | Generic capture toolkit; no protobuf decode. |

### Articles / docs (not full products)

| Source | Relevance |
|--------|-----------|
| [Cursor forum — local LLM](https://forum.cursor.com/t/how-can-i-use-a-local-llm-on-my-desktop-ai-computer/152419) | Official-ish: localhost blocked; requests still go through Cursor for prompt build; tunnel + Override required. |
| [Cursor forum — connect local fails](https://forum.cursor.com/t/connecting-local-ai-server-to-cursor-does-not-work/152940) | Model-name sanitization (`:`/`-` stripped) can hijack routing back to Cursor registry. |
| [UAI Cursor docs](https://uai.sh/docs/cursor) | Commercial Override setup; falls back to `cus-` + local `cursor-proxy.mjs` when Agent ignores Base URL. |
| [Speedscale — peeking under Cursor](https://speedscale.com/blog/peeking-under-the-hood-of-cursor/) | Early ChatService context capture (proxymock). |
| [Cursor enterprise network config](https://cursor.com/docs/enterprise/network-configuration) | Host map; SSL inspection breaks Agent — relevant to MITM ops. |

---

## Approach taxonomy

```text
A. Override OpenAI Base URL + unknown model id
   Cursor → HTTPS tunnel? → local OpenAI-compat proxy → Ollama / Copilot / LiteLLM / …
   Prefixes seen: cus- | fx- | custom- | uai- | glider- (Glider)
   ★ Dominant success path for Agent+tools

B. MITM TLS on api2 / api5 Connect-RPC
   B1. Observe/decode only  → cursor-tap, LaiKash, re-cursor
   B2. Local fulfill text   → Glider StreamChat* (rare elsewhere)
   B3. Local fulfill Agent  → **no public proof**

C. Reverse gateway (other clients → Cursor subscription)
   cursor-rpc, eisbaw demo, ccs CursorExecutor, Cursor-To-OpenAI
   Useful schemas; opposite product direction

D. Patch Cursor binary / workbench.js
   Not found as a maintained open project in this search (anecdotal private hacks only)
```

---

## MITM of modern Agent — what exists vs what doesn’t

### Exists (public)

1. **Decrypt + parse** Connect envelopes and protos for `BidiAppend`, `RunSSE`, StreamCpp, Batch, etc. ([cursor-tap](https://github.com/burpheart/cursor-tap) reverse notes: read/write split — RunSSE long poll/stream for model output; BidiAppend short POSTs for user/tool payloads).
2. **Extract fresher protos** from Cursor’s JS bundle (cursor-tap) or reverse Unified request fields (eisbaw, ccs schema TS).
3. **Legacy AiService** decode/fulfill patterns (cursor-rpc, Glider Path B StreamChat*).

### Does **not** appear to exist (public)

1. MITM that **answers** `BidiAppend` / `RunSSE` / `StreamUnifiedChatWithTools` with a **local** model while keeping Cursor’s Agent UI happy.
2. MITM that **rewrites** subscription Agent traffic to Ollama without Override Base URL.
3. Stable shared library of modern Agent response codecs with license clarity.

**Implication:** Glider’s bet that Path A is primary and Path B opaque-passthrough for modern Agent is **aligned with the entire public ecosystem**. Path B local Agent would be **novel**, not catching up to a known open implementation.

---

## Search notes

Queries used: `cursor ollama`, `cursor local llm`, `cursor mitm`, `cursor proxy`, `aiserver.v1`, `StreamUnifiedChatWithTools`, `BidiAppend`, `cus-`, `cursor continue.dev`, `cursor custom openai`, plus seed repos (cursor-rpc, copilot-for-cursor, jacksonkasi article).

`gh` CLI was unavailable in this environment; GitHub discovery used WebSearch + `api.github.com` repo metadata.

---

## Suggested clones for deeper study (optional)

Already noted in methodology: `_research/cursor-rpc`, `_research/copilot-for-cursor`.

Worth adding if Path B Unified work starts:

- `burpheart/cursor-tap` (proto extract + RunSSE/BidiAppend notes)
- `eisbaw/cursor_api_demo` (2.6.22 Unified + Bidi experiments)
