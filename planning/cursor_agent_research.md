# Cursor Agent interception — research and wire format

Cursor is one of three fronts Glider intercepts (alongside Claude Code and
Antigravity/`agy`); it gets its own doc because its wire protocol had to be
reverse-engineered rather than read from an official spec. Code authority:
`internal/mitm/`, `internal/cursorrpc/`.

## Two ways Glider sees Cursor traffic

| Path | Mechanism | Use for |
|------|-----------|---------|
| **A — Gateway** | Cursor's own "Override OpenAI Base URL" pointed at `:8080/v1`, model prefixed `cus-…` so Cursor treats it as an unknown/custom model | **Primary path for Agent + tools.** Well-proven pattern across the ecosystem (see prior art below) — Glider normalizes Anthropic-shaped JSON and streams `tool_calls` back on SSE. |
| **B — MITM Connect** | TLS-decrypt `api2`/`*.api5.cursor.sh`, decode Cursor's private Connect-RPC protocol (`BidiAppend` → `RunSSE`) | Text-only local fulfillment + sticky routing for chat/composer traffic. Child/tool-loop `RunSSE` still falls through to origin — nobody, public or in this codebase, fulfills tool calls over this path yet. |

Prefer HTTP/1.1 in Cursor (`cursor.general.disableHttp2: true`) so it honors `http.proxy` for Path B.

### Host map

| Host / pattern | Purpose | Intercept the LLM call? |
|---|---|---|
| `api2.cursor.sh` | Most API / legacy AiService | Yes — chat/OpenAI-shaped + Path B |
| `api3`/`api4` (+ regional) | Cursor Tab | Usually no |
| `api5`/`*.api5.cursor.sh`/`agent*.api5` | Agent + NAL | Yes, for Agent RPCs, if decrypted |

## RunSSE wire format (Path B)

```
HTTP Connect stream (application/connect+proto)
  └─ envelopes: flags(1) + BE size(4) + payload
        bit0 = gzip payload; bit1 = end-stream
  └─ AgentServerMessage
        field 1 InteractionUpdate — text_delta / thinking_delta / token_delta / heartbeat / turn_ended
        field 3 conversation_checkpoint_update  // rare
        field 4 KvServerMessage                 // large gzip / small state blobs — NOT field 3
```

Glider's encoder: heartbeat → thinking_delta → text_delta(s) + token_delta → turn_ended → Connect end-stream.

**Correlation:** `BidiAppend` outer field 2 UUID ≡ `RunSSE`'s `BidiRequestId` ≡ `X-Request-Id`. The fulfill hub waits ≤800ms on root `RunSSE` for a local answer to arrive before falling back to origin.

Schema source: live `*_RESP` peeks captured via `mitm.debug_agent_rpc` + [burpheart/cursor-tap](https://github.com/burpheart/cursor-tap)'s `agent_v1.proto` extract (mirrored at `planning/vendor_ref/agent_v1.proto` for offline reference — not a Go module dependency).

### `agent.v1.AgentService/Run` — the terminal CLI's own RPC, distinct from `BidiAppend`/`RunSSE`

**Open, unresolved, and the current top suspect for a real, severe bug** (2026-07-31): a genuine, interactive `cursor-agent` terminal session — no delegation involved at all, just the CLI's own normal use — hangs indefinitely under Glider's transparent MITM. Killing glider.exe made the same in-flight prompt resolve *immediately*, which rules out "the real backend/model is just slow" — something about relaying this RPC through Glider's MITM specifically prevents it from ever completing. Confirmed via live logging (`internal/mitm/proxy.go`'s temporary diagnostic instrumentation, 2026-07-31): the response body arrives as a slow trickle of tiny (mostly ~9-byte) frames that plateau around 100-200 bytes and then sit until the relay's own 120s timeout kills it — real bytes, real activity, but never enough to become a usable answer. Multiple concurrent `Run` calls (a live multi-step delegate task needs several) each show the identical pattern independently.

**Real, previously undocumented wire-format finding, from live `debug_agent_rpc` dumps in `~/.glider/mitm-debug/*_Run.txt`**: every captured request carries an `X-Blob-Encryption-Key` header (64 hex chars, e.g. `2df31428c0c6f9d7432b6060609a75a4285c6a7d0c0c94207101f4c881cd7592`) alongside `X-Original-Request-Id`. Neither appears anywhere else in this doc's existing RunSSE/BidiAppend research — this is a genuinely separate protocol layer, application-level encryption on top of TLS, not just TLS itself.

Confirmed by direct comparison of multiple dumps from the same session: **both are turn-scoped, not per-call** — two different `Run` requests with two different `X-Request-Id` values shared the exact same `X-Blob-Encryption-Key` and `X-Original-Request-Id`, while a request belonging to a different turn had a different key and a different original-request-id. So one logical delegate/agent turn establishes one blob-encryption key once and reuses it across however many `Run` HTTP calls that turn needs.

**Leading, unconfirmed hypothesis**: if this key (or material it derives from) binds response content to the *specific, original* TLS session between the real client and real origin — a plausible design for anything calling itself "blob encryption," meant to protect large response content end-to-end — then Glider's MITM (which necessarily terminates that original session and opens its own, separate one to origin) could break whatever this key depends on. That wouldn't surface as a clean protocol error — Glider has no reason to fail decoding a Connect envelope it's just relaying — it would surface exactly like what's observed: content the real client can never actually use, read as "nothing happening yet," forever.

**Not yet confirmed.** This needs real protocol-level work before treating it as fact: capture a byte-for-byte comparison of a *stuck* exchange against a genuinely *successful* direct (non-MITM) one for the same prompt shape; check whether `X-Blob-Encryption-Key`'s value is derived from anything TLS-session-specific (a fixed/predictable value across fresh connections would rule this theory out; a value that changes with the underlying TLS session would strengthen it); check whether cursor-agent's response actually contains any separately-encrypted blob content at all for a plain text turn, or whether this header is sent unconditionally regardless of payload shape. None of that happened yet — this section records the lead, not a fix.

## Path B tool codec

`mitm.agent_rpc_tool_codec` (or `GLIDER_MITM_AGENT_RPC_TOOL_CODEC=1`) maps OpenAI/Cursor/Glider tool names onto Cursor's `agent.v1.ToolCall` oneofs so child/tool-loop `RunSSE` can carry tool frames locally instead of falling back to origin. Implementation: `internal/cursorrpc/toolcall_map.go` + `runsse_codec.go`. Unmapped names encode as `TruncatedToolCall` (field 34) rather than failing.

| Wire field | Cursor oneof | Example names | Glider builtin |
|-----------:|--------------|---|---|
| 1 | `shell_tool_call` | `shell`, `shell_exec`, `bash` | `shell_exec` |
| 4 | `glob_tool_call` | `Glob`, `fs_search`, `file_search` | `fs_search` |
| 5 | `grep_tool_call` | `Grep`, `code_grep` | `code_grep` |
| 8 | `read_tool_call` | `Read`, `read_file`, `fs_read` | `fs_read` |
| 12 | `edit_tool_call` | `Write`, `StrReplace`, `fs_write` | `fs_write` |
| 13 | `ls_tool_call` | `Ls`, `list_dir`, `fs_list` | `fs_list` |
| 16 | `sem_search_tool_call` | `SemSearch`, `codebase_search` | `fs_search` |
| 18 | `web_search_tool_call` | `WebSearch` | `web_search` |
| 24 | `fetch_tool_call` | `Fetch`, `http_fetch` | `http_fetch` |
| 26/27 | `exa_search`/`exa_fetch` | `ExaSearch`/`ExaFetch` | `web_search`/`web_fetch` |
| 37 | `web_fetch_tool_call` | `WebFetch` | `web_fetch` |
| 34 | `truncated_tool_call` | any unmapped name | — |

Still `Truncated`-only (no map): `record_screen`, `computer_use`, `setup_vm_environment`, `start_grind_*`, `report_bugbot_results`, and any oneof not yet enumerated. Default is off — production Agent+tools demos should prefer Path A.

## Prior art (why Path A is "just the ecosystem-standard approach")

**Headline:** almost everyone who runs Cursor Agent against a local or alternate model uses Override Base URL + an unknown model prefix (`cus-`, `fx-`, `custom-`, `uai-`, …). Public MITM projects mostly *observe/decode* the modern Agent RPC plane and forward to origin — nobody publishes a local-fulfilling `BidiAppend`→`RunSSE` MITM. Glider's Path B text fulfill is genuine novelty there; Path B tool-loop fulfill is unproven anywhere, including here.

| Project | Approach | Reusable for Glider |
|---|---|---|
| [jacksonkasi1/copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor) | Override Base URL + `cus-` + Anthropic→OpenAI tool transforms | Primary Path A reference; local clone at `_research/copilot-for-cursor` |
| [0xSero/factory-cursor-bridge](https://github.com/0xSero/factory-cursor-bridge) | Override URL + `fx-` + multi-provider translate | Multi-provider routing ideas |
| [burpheart/cursor-tap](https://github.com/burpheart/cursor-tap) | MITM CONNECT decode of `BidiAppend`/`RunSSE`, observe-only | Best public reference for the modern Agent wire format |
| [eisbaw/cursor_api_demo](https://github.com/eisbaw/cursor_api_demo) | Client-side HTTP/2 Connect to Cursor, modern Unified schemas | Field-number cross-check against the cursor-tap extract |
| [everestmz/cursor-rpc](https://github.com/everestmz/cursor-rpc) | Proto extract + Go client for legacy `AiService/StreamChat*` | Vendored as a Go module — see `internal/cursorrpc/THIRD_PARTY.md` |

Full taxonomy and a longer ranked list lived in git history (`planning/cursor_prior_art.md` before this consolidation) if deeper prior-art spelunking is ever needed again.

## Local study clones

`_research/cursor-rpc` and `_research/copilot-for-cursor` are reference clones for offline reading, not build dependencies — Glider only depends on the `cursor-rpc` Go module (see `internal/cursorrpc/THIRD_PARTY.md` for the pinned commit and license notes).
