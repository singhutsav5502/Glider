# Cursor Agent RPC debug findings (Path B) — archival

> ## Current status (2026-07-18 late) — read this first
>
> | Item | Status |
> |------|--------|
> | Path B text fulfill (`agent_rpc_fulfill`) | **Shipped** — canned UI-proven; Ollama when backends healthy |
> | `/cloud` hard-force + turn-family sticky | **Shipped** — summaries / titles / subagents stay origin |
> | Composer chrome (`user_visible_high_level_summary`) | **Shipped** — `IsSystemSummaryChrome` / `bidi_sticky_cloud_summary` |
> | `contextgraph` sticky correlation | **Shipped MVP** |
> | Child / tool RunSSE | **Still origin** (`tool_followup_would_local` only) |
>
> Live product status: [`cursor_agent_protocol_interception.md`](./cursor_agent_protocol_interception.md) · Policy: [`routing_session_policy.md`](./routing_session_policy.md) · Index: [`README.md`](./README.md).
>
> **This file is archival capture notes** (Cursor **3.12.17** glass, 2026-07-18). Earlier Captures 1–5 timelines were pruned; dumps remain under `%USERPROFILE%\.glider\mitm-debug\`.

---

## Verdict (Capture 6 — `ping-glider-5`)

| Layer | Status |
|-------|--------|
| MITM TLS + classify | Works |
| BidiAppend → user text | Works (`bidi_hint=ping-glider-5`) |
| DecideLocal / hub arm | Works |
| RunSSE wait ↔ arm | Works |
| CompleteLocal | Failed as expected — Ollama off → fail-soft origin |
| Local RunSSE write | Did not fire on ping-5 (fail-soft) |
| Origin RESP peeks | Yes — wire ground truth |
| Text-only local fulfill | Canned proven in UI; Ollama when backends work |
| Tools / child RunSSE | Origin only |

**ping-glider-5 is Path B success through arm + attempt; Ollama-down fail-soft is not a Path B bug.**

Dump dir: `%USERPROFILE%\.glider\mitm-debug\` · API: `GET http://127.0.0.1:8081/api/mitm/debug/recent`

---

## RunSSE wire format (keep)

**Source:** live `*_RESP` peeks + [cursor-tap `agent_v1.proto`](./vendor_ref/agent_v1.proto).

```
HTTP Connect stream (application/connect+proto)
  └─ envelopes: flags(1) + BE size(4) + payload
        bit0 = gzip payload; bit1 = end-stream
  └─ AgentServerMessage
        field 1 InteractionUpdate — text_delta / thinking_delta / token_delta / heartbeat / turn_ended
        field 3 conversation_checkpoint_update  // rare in early peeks
        field 4 KvServerMessage                 // large gzip / small state blobs
```

**Glider encoder:** heartbeat → thinking_delta → text_delta(s) + token_delta → turn_ended → Connect end-stream.

**Correlation:** BidiAppend outer field 2 UUID ≡ RunSSE `BidiRequestId` ≡ `X-Request-Id`. Hub waits ≤800ms on root RunSSE for `ArmLocal`.

**Correction:** early large gzip frames are **`KvServerMessage` (field 4)**, not `conversation_checkpoint_update` (field 3).

---

## How to test canned Path B (no Ollama)

1. `agent_rpc_fulfill: true` + `agent_rpc_canned_on_error: true` (or `GLIDER_MITM_AGENT_RPC_CANNED=1`).
2. Agent text-only message e.g. `ping-glider-6`.
3. Success: slog `runsse local fulfill` (`canned=true`) + chat shows `pong from glider (canned Path B)`.
4. Prefer `agent_rpc_canned_on_error: false` for real Ollama once backends are up.
