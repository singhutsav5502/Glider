# Cursor Agent RPC debug findings (Path B)

> Captured: **2026-07-18** · Cursor **3.12.17** (win32/x64, client-type `glass`) · Glider MITM with `mitm.debug_agent_rpc: true`
> Dump dir: `%USERPROFILE%\.glider\mitm-debug\` · API: `GET http://127.0.0.1:8081/api/mitm/debug/recent`
> Binary rebuilt ~15:23 IST (Phase 4) · **canned-pong + encoder crack rebuild** after Capture 6 verify.

---

## Verdict (updated — Capture 6 / ping-glider-5)

| Layer | Status |
|-------|--------|
| MITM TLS + classify | Works |
| BidiAppend context_envelope → user text | Works (`bidi_hint=ping-glider-5`) |
| DecideLocal / hub arm | Works (`bidi_fulfill_armed` / `would_fulfill_local`) |
| RunSSE wait ↔ arm correlation | Works (offer found; no `runsse_wait_origin`) |
| CompleteLocal | **Failed as expected** — Ollama off (`runsse_complete_err`, history `action:error` route=local) |
| Local RunSSE write (`runsse_local`) | **Did not fire** on ping-5 (fail-soft → origin) |
| Origin RunSSE RESP dumps | **Yes** — `*_RESP.bin` / `_frames.txt` written (ground truth) |
| Text-only local fulfill codec | Implemented; **canned proven in UI**; Ollama when backends work (`agent_rpc_canned_on_error: false` preferred) |
| Tools / child RunSSE | Origin only (`runsse_skip_tool_loop`) |

**ping-glider-5 is Path B success through arm + attempt; Ollama-down fail-soft is not a Path B bug.**

---

## Capture 6 — `ping-glider-5` live verify (~15:31–15:32 IST)

| Check | Result |
|-------|--------|
| `glider.exe` mtime | **15:23:10** |
| Glider process start | **15:31:27** (pid 23832) — **≥ rebuild ✓** |
| Distinctive string | **`ping-glider-5`** in dump `000004` `bidi_hint` + TipTap |
| Metrics (session) | `bidi_extract`≥1, `bidi_fulfill_armed`≥1, `would_fulfill_local`≥1, `runsse_complete_err`≥1, **no** `runsse_local` |
| History jsonl | `id=090a1500-…` path=RunSSE `action=error` `route=local` `model=codellama:7b` `rule=Small Context Local` ~529ms |
| Root RunSSE RESP | `20260718T100203.817_000022_*` — origin text `"Checking whether ping-glider-…"` |

### Timeline

1. **10:01:47Z** — gzip context `BidiAppend` `000004` — extract + **ArmLocal** (`ping-glider-5`)
2. **10:01:48Z** — root `RunSSE` `000006` — Wait hit pending offer → CompleteLocal → Ollama error → **origin fail-soft**
3. **10:02:03Z** — origin RESP peek `000022` (64 KiB) with real frames

---

## Crack plan (how we crack Path B)

**Goal:** prove Cursor glass accepts a Glider-authored RunSSE stream **without Ollama**, then harden codec from origin peeks.

1. **Ground truth** — treat origin `*_RESP.bin` / `_frames.txt` as the wire oracle (Ollama off = more origin traffic — good).
2. **Diff vs `agent_v1.proto`** — classify every frame oneof correctly (done for thinking/KV).
3. **Minimum UI sequence** — emit the smallest stream Cursor accepts (currently: heartbeat → thinking_delta → text → token → turn_ended; KV optional later).
4. **Canned dry-run** — `agent_rpc_canned_on_error: true` → on CompleteLocal failure write canned `"pong from glider (canned Path B)"` instead of origin (proves codec).
5. **Iterate** — if UI blank/hangs: clone sanitized KV blob shape from RESP; if UI shows pong: Path B text codec is cracked; next = real local model + tools.

### Origin frame truth (Capture 6 RESP `000022`)

```
heartbeat
KvServerMessage (field 4, often gzip)   ← NOT conversation_checkpoint field 3
KvServerMessage (small; may echo TipTap user text)
heartbeat
thinking_delta + token_delta  (×N)      ← InteractionUpdate field 4
text_delta + token_delta      (×N)
… (peek truncates before turn_ended)
```

**Correction vs earlier notes:** early large gzip frames are **`KvServerMessage` (AgentServerMessage field 4)**, not `conversation_checkpoint_update` (field 3). Field 3 rarely appears in 64 KiB peeks. Prior “empty checkpoint” MVP was wrong-shaped; encoder now omits it and emits a brief `thinking_delta` first.

---

## RunSSE response wire format (Phase 2→4, corrected)

**Source:** live `*_RESP` peeks + [cursor-tap `agent_v1.proto`](./vendor_ref/agent_v1.proto).

```
HTTP response (Connect stream, Content-Type application/connect+proto)
  └─ repeated Connect envelopes: flags(1) + big-endian size(4) + payload
        flags bit0 = gzip-compressed payload
        flags bit1 = end-stream (JSON `{}` or error)
  └─ uncompressed payload = agent.v1.AgentServerMessage
        field 1 InteractionUpdate
          field 1  text_delta
          field 4  thinking_delta   { string text = 1; ThinkingStyle style = 2 }
          field 8  token_delta
          field 13 heartbeat        // live: 0a026a00
          field 14 turn_ended
        field 3 conversation_checkpoint_update  // rare in early peeks
        field 4 KvServerMessage                 // large gzip / small state blobs
```

**Glider encoder (post-crack):** heartbeat → thinking_delta → text_delta(s) + token_delta → turn_ended → Connect end-stream.

**Correlation:** BidiAppend outer field 2 UUID ≡ RunSSE `BidiRequestId.request_id` ≡ `X-Request-Id`. Hub waits ≤800ms on root RunSSE for `ArmLocal`.

---

## How to test canned Path B (no Ollama)

1. Stop old Glider; start new `D:\___repos\Glider\glider.exe` (mtime ≥ canned rebuild).
2. Config already has `agent_rpc_fulfill: true` + `agent_rpc_canned_on_error: true` (or env `GLIDER_MITM_AGENT_RPC_CANNED=1`).
3. In Cursor Agent, send a short text-only message, e.g. `ping-glider-6`.
4. Success signals:
   - slog: `mitm bidi fulfill armed` → `CompleteLocal failed → canned fulfill` → `mitm runsse local fulfill` (`canned=true`)
   - metrics: `action:runsse_canned` + `action:runsse_local`
   - Chat shows **`pong from glider (canned Path B)`** (not origin prose)
5. If UI blank: dump newest `*_RESP*` / compare; next crack step = inject sanitized KV frame from origin.

---

## Capture 5 — prompt-hint binary + `ping-glider-4` (~14:51–14:52 IST)

| Check | Result |
|-------|--------|
| `glider.exe` mtime | **14:50:36** (`D:\___repos\Glider\glider.exe`) |
| Glider process start | **14:51:34** (pid 23428) — **≥ rebuild ✓** |
| Distinctive string | **`ping-glider-4` recovered** in dump inspect + `bidi_hint` |
| Inflate | **658 082** B from **121 210** wire — no truncate |

### Timeline (user Agent message = `ping-glider-4`)

1. **09:21:41Z** — root `RunSSE` dump `000001`, request_id `aa79d4ab-…`
2. **09:21:41Z** — **gzip context `BidiAppend` dump `000002`** — **`ping-glider-4` found** (see below)
3. **09:21:47Z** — child `RunSSE` dump `000011` + tool-loop BidiAppends; dump `000014` re-surfaces the string via Agent reading this findings task (parent tool call) — not the primary user turn

### Primary recovery — dump `000002`

| Field | Value |
|-------|--------|
| File | `20260718T092141.401_000002_api2.cursor.sh_BidiAppend.txt` |
| Path | `/aiserver.v1.BidiService/BidiAppend` |
| `content_encoding` / sizes | gzip · wire **121210** · inflate **658082** |
| `payload_kind` | `ascii_hex` |
| `inner_top_field` / `role_guess` | **1** / **`context_envelope`** |
| `nested_kind_guess` | **`context_sections`** |
| nested wire (abbrev) | `1:ld(71384),2:ld(256816),4:ld(0),5:ld(36),9:ld(9),10:varint,14:ld×N…` |
| `bidi_hint` | **`ping-glider-4`** |
| `bidi_inspect.strings` (redacted) | includes bare `ping-glider-4` and ProseMirror fragment `"type":"text","text":" ping-glider-4"` plus env crumbs (`win32 10.0.26200`, …) |

### Envelope structure confirmation (user text path)

```
HTTP gzip body
  └─ outer proto field 1 (ascii_hex) → inner top field **1** = context_envelope
        └─ nested field **2** (~256 KiB) = conversation / prompt pack
              └─ printable extract → user text as TipTap/ProseMirror JSON
                    `"type":"doc"… "type":"text","text":" ping-glider-4"`
        └─ nested field **14**×N = section labels (unchanged from Capture 4)
```

**Field path that held the user text:**  
`BidiAppend.outer[1] → hex-inner[1] (context_envelope) → nested[2] (prompt pack) → printable / JSON text node`  
Also mirrored as dump line `bidi_hint=ping-glider-4` (from `PrintableHint` / string pick).

API ring at verify time had already rotated past dump `000002` (50-entry window filled with later tool BidiAppends); **disk dump is authoritative**.

---

## Capture 4 — post-4MiB-rebuild Agent turn (~14:44–14:46 IST)

| Check | Result |
|-------|--------|
| `glider.exe` mtime | **14:43:40** (`D:\___repos\Glider\glider.exe`) |
| Glider process start | **14:44:45** (pid 13080) — **≥ rebuild ✓** |
| Inflate truncations (post-restart BidiAppends) | **0** (`inflated_bytes=262144` absent; `inflate_truncated` never set) |
| Big gzip append inspect | **SUCCESS** — see dump `000003` |

### Timeline (user said “hi!” after restart)

1. **09:14:45Z** — dump seq reset (`000001` rgstr) as Glider comes up
2. **09:14:49Z** — root `RunSSE` dump `000002`, request_id `b4e9eb79-…`
3. **09:14:49Z** — **huge gzip `BidiAppend` dump `000003`**: wire **120 171** B → inflate **653 610** B (was capped at 262 144 in Capture 3) → **`payload_kind=ascii_hex`**, **`inner_top=1`**, nested wire populated, strings at least model id
4. **09:14:52+** — parent-turn acks (`inner` 3/7) until child tool loop
5. **09:15:06Z** — child `RunSSE` dump `000022`, request_id `b0a69d51-…`, `X-Parent-Request-Id=b4e9eb79-…`, **`X-Parent-Agent-Tool-Call-Id` set**
6. **09:15:06+** — child BidiAppend storm; content/`tool_text`/`path_blob`; additional large gzip content blobs inflate ~496 KiB **without truncate**

### Big append `000003` (the Capture-3 failure case — now fixed)

| Field | Value |
|-------|--------|
| `content_encoding` | gzip |
| `body_bytes` / `inflated_bytes` | 120171 / **653610** |
| `inflate_truncated` | **false** (absent) |
| `payload_kind` | ascii_hex |
| `inner_top_field` | **1** (new discriminator vs prior 2/3/5/7-only view) |
| `role_guess` (at capture) | `unknown` → code now labels **`context_envelope`** |
| nested wire | `1:ld(69169),2:ld(256795),4:ld(0),5:ld(36),9:ld(9),10:varint,14:ld×N…` |
| `nested_kind` (at capture) | empty → code now **`context_sections`** |
| strings (at capture) | `["gemini-3-flash"]` only — field-14 labels + field-2 prompt hints added in follow-up R&D |

Same envelope shape also seen on a slightly earlier (pre-restart) dump with richer labels: `system_prompt`, `Tool definitions`, `summarized_conversation`, … — confirms field **14** under top=1 = **section labels**, while nested field **2** (~256 KiB) is the conversation/prompt pack.

### Post-restart inspect mix (244 BidiAppend dumps)

| Metric | Count |
|--------|------:|
| HTTP gzip bodies | 30 |
| `inflated > 262144` (would have failed old cap) | **12** |
| inflate still truncated at 256 KiB | **0** |
| `inner_top=1` (context envelope) | 2 |
| `inner_top=2` (content) | 61 |
| `nested_kind=tool_text` | 31 |

### Redacted content hints (no secrets pasted)

- Model: `gemini-3-flash`
- Context section labels (sibling dump / same shape): `system_prompt`, `Tool definitions`, `Subagent definitions`, `summarized_conversation`
- Tool / env blobs: `win32 10.0.26200`, `D:\___repos\Glider`, terminals path, timezone `Asia/Calcutta`, MCP tool name `cursor-app-control-move_agent_to_root`
- Path blob: agent-transcript UUID path under `~\.cursor\projects\…\agent-transcripts\`
- Tokens/JWTs: redacted (`Bearer eyJh…`)

---

## Capture 3 — post-restart Agent turn (~14:37–14:40 IST)

| Check | Result |
|-------|--------|
| `glider.exe` mtime | **14:36:27** |
| Glider process start | **14:37:44** (pid 1092) — **new binary confirmed** |
| Inspect on dumps/API | **`bidi_inspect.*` + `runsse_inspect.*` present** (not stale) |
| Dump seq | Reset at RunSSE `000001` @ 09:07:54Z |

### Timeline (this user Agent message)

1. **09:07:54Z** — `RunSSE` dump `000001`, request_id `9c1a2430-…` (root turn; no parent tool id)
2. **09:07:54Z** — huge gzip `BidiAppend` dump `000002` (~120 KiB wire, inflate hit **old 256 KiB cap** → inspect failed / `role=unknown`)
3. **09:07:56+** — parent turn `bidi_seq` 1…18 mostly **ack/control** (inner top **3/7/5**); one **content** at seq 8 (`path_blob` / agent-transcripts path)
4. **09:08:01Z** — child `RunSSE` dump `000011`, request_id `e6ddb0ee-…`, `X-Parent-Request-Id=9c1a2430-…`, **`X-Parent-Agent-Tool-Call-Id` set** (tool loop)
5. **09:08:01+** — child turn dominates: **214** BidiAppends, `bidi_seq` 1…225; tool stdout of Shell listing mitm-debug / process / API JSON

### Inner top-field mix (post-restart dumps with successful inspect)

| Inner top | Count | Role guess |
|----------:|------:|------------|
| **3** | 81 | ack_or_empty |
| **5** | 66 | control_small (often) |
| **2** | 49 | content_blob |
| **7** | 36 | ack_or_empty |
| *(failed)* | 13 | unknown — **all** hit inflate=262144 under old 256 KiB cap |

### Encoding rates (this turn’s BidiAppends)

| Kind | Count |
|------|------:|
| HTTP gzip bodies | 30 |
| uncompressed bodies | 215 |
| `payload_kind=ascii_hex` | 232 |
| `payload_kind=raw` | 0 |
| inspect empty/failed | 13 (truncated gunzip) |

### Nested shapes under content (`inner_top=2`)

| Nested wire pattern | Count | Guess |
|---------------------|------:|-------|
| `1:varint,14:ld(N),39:varint` | ~27 | **tool_text** — field **14** = shell/tool stdout |
| `1:varint,3` / tiny | many | control under field 2 |
| `28:ld,39:varint` | 1+ | **path_blob** — transcript / path payloads |

### Redacted content hints (no secrets pasted)

- Tool stdout: `ProcessName : glider`, `StartTime : 7/18/2026 2:37:44 PM`, PowerShell `Get-ChildItem` of `~\.glider\mitm-debug\`, `/api/mitm/debug/recent` JSON keys (`enabled`, `dump_dir`, `path_counts`).
- Paths: `D:\___repos\Glider\planning\cursor_agent_rpc_debug_findings.md`, `~\.cursor\projects\…\agent-transcripts\…`, `~\.glider\mitm-debug\*RunSSE*` / `*BidiAppend*` dumps being **re-read by the Agent**.
- Model / prompt text: **not clearly isolated** in successful small appends; the likely **user/context blob** is the first ~120 KiB gzip append that **failed inspect under the 256 KiB inflate cap**.
- Tokens/JWTs: redacted in dumps (`Bearer eyJh…`, checksums length-only).

### API ring (`/api/mitm/debug/recent`)

- `enabled: true`; `bidi_inspect` on BidiAppend entries; RunSSE may fall out of the 50-entry ring quickly (disk dumps still have `runsse_inspect.*`).
- Sample mix in ring window: ascii_hex dominant; inner tops 2/3/5/7 as above.

---

## Decode pipeline (what we know)

```
HTTP body
  └─ [optional] Content-Encoding: gzip  → inflate (debug cap **4 MiB**; Capture 4: 653 KiB OK)
       └─ outer protobuf (application/proto)
            ├─ field 1 ld  → append payload
            │     └─ usually ASCII-hex digits of an INNER protobuf
            │           └─ inner top field = type discriminator (hypotheses below)
            ├─ field 2 ld  → { field 1 = request UUID }   // length often 38
            └─ field 3 varint → append sequence index
```

**Known (high confidence):**
- Outer fields **1 / 2 / 3** roles above — stable across hundreds of dumps.
- Field 1 is **ASCII-hex** for both tiny and many medium/large (post-gunzip) appends; hex length ≈ `2 × inner_bytes`.
- RunSSE **request** is Connect-framed (`application/connect+proto`): one frame, flags=`0`, size=`38`, proto `1:ld(36)` = UUID string. Correlates with `X-Request-Id` / BidiAppend field-2 UUID.
- Child RunSSE carries `X-Parent-Request-Id` + `X-Parent-Agent-Tool-Call-Id` for tool loops.
- Small inner patterns recur as heartbeats/acks, not chat text.
- Content under inner **2** with nested **`14:ld`** is tool/shell text (Agent reading mitm-debug / env).
- **Capture 4:** gzip context appends inflate fully under 4 MiB; **inner top 1** = context envelope with nested field **2** (~conversation pack) + many small field **14** section labels.
- **Capture 5:** distinctive user Agent text (`ping-glider-4`) recovered from that nested field **2** as TipTap/ProseMirror JSON (`"type":"text","text":"…"`) and as `bidi_hint` / `bidi_inspect.strings`.

**Guess (medium / needs JS proto extract):**
| Inner top field | Observed shape | Role guess |
|----------------:|----------------|------------|
| **1** | `1:ld(N)` N ~70k–300k+ | **context_envelope** — model + sections + conversation pack |
| **2** | `2:ld(N)` N from ~8 to 9k+ | **content_blob** — tool results, FS paths, context snippets |
| **3** | `3:ld(4)` (very common) | **ack_or_empty** — tiny control |
| **5** | `5:ld(4)` | **control_small** |
| **7** | `7:ld(0)` | **ack_or_empty** — empty marker |

Nested under content (`2:ld`):
| Nested | Guess |
|--------|--------|
| `1:varint,14:ld(N),39:varint` | **tool_text** (stdout/stderr / tool result body) |
| `28:ld(N),39:varint` | **path_blob** (paths / transcript refs) |

Nested under context envelope (`1:ld`):
| Nested | Guess |
|--------|--------|
| `1:ld` + **`2:ld(large)`** + many **`14:ld(small)`** | **context_sections** — field 14 = labels (`system_prompt`, …); field 2 = prompt/conversation blob (**user text** in TipTap JSON — Capture 5) |

**Unknown / not claimed:**
- Exact `.proto` message names for inner oneofs.
- How to **author** a valid BidiAppend or RunSSE **response** Cursor accepts (fulfill).
- RunSSE **response** frame layout (request-only Observe today).
- Stable single protobuf scalar for the user message (text lives inside nested field-2 JSON, not a lone string field).

---

## RunSSE request notes

| Property | Value |
|----------|-------|
| Path | `/agent.v1.AgentService/RunSSE` |
| Content-Type | `application/connect+proto` |
| Body | 43 bytes typical |
| Connect | flags=`0`, size=`38` |
| Proto | `1:ld(36)` UUID |
| Role | Opens long **read** stream; model tokens expected on **response** (not dumped yet) |

---

## Session correlation (unchanged)

| Header | Role |
|--------|------|
| `X-Request-Id` / `X-Original-Request-Id` | Shared RunSSE ↔ BidiAppend turn |
| `X-Root-Parent-Request-Id` / `X-Parent-Request-Id` | Parent agent turn |
| `X-Session-Id` | Stable chat session |
| `X-Parent-Agent-Tool-Call-Id` | Tool-loop correlation |
| `connect_session` | Glider CONNECT tunnel id |

---

## Code shipped this pass (Phase 4 + canned crack)

| Piece | Purpose |
|-------|---------|
| `WriteRunSSETextResponse` | heartbeat → thinking_delta → text/token → turn_ended (no empty field-3) |
| `EncodeAgentThinkingDelta` + KV/thinking classifiers | Match origin InteractionUpdate field 4 / AgentServerMessage field 4 |
| `AgentFulfillHub` | Correlate BidiAppend UUID ↔ RunSSE (≤800ms wait) |
| `tryRunSSEFulfill` + `CannedOnError` | Root hijack; on CompleteLocal err → canned pong stream |
| `mitm.agent_rpc_canned_on_error` / `GLIDER_MITM_AGENT_RPC_CANNED` | Dry-run without Ollama |
| `testdata/runsse_resp_text_synth.bin` | Anonymized synthetic fixture |
| `planning/vendor_ref/agent_v1.proto` | cursor-tap schema reference |

**Still open:** live Cursor UI acceptance of canned stream; sanitized KV blob replay; tool_call frames; Path A tools.

---

## Next concrete R&D steps

1. **Restart Glider on canned rebuild; Agent `ping-glider-6`** — expect UI text `pong from glider (canned Path B)` + metrics `runsse_canned`.
2. If blank UI: clone/sanitize origin `KvServerMessage` frame from RESP into encoder.
3. When Ollama is back: disable canned (or leave as fallback) and prove real local model text.
4. Keep Path A for Agent+tools until tool_call encode exists.
