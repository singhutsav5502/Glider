# Cursor Agent interception methodology — archival

> Research deliverable (2026-07-18). **Frozen / archival.**  
> **Implementation status:** Path A tools + Path B **text** fulfill + sticky/contextgraph shipped; Path B tool loops still origin.  
> Authority: [`cursor_agent_protocol_interception.md`](./cursor_agent_protocol_interception.md) · [`README.md`](./README.md).  
> Prior art survey: [`cursor_prior_art.md`](./cursor_prior_art.md).  
> Local study clones (not a product dependency): `_research/cursor-rpc`, `_research/copilot-for-cursor`.

---

## Sources consulted

| Source | Role |
|--------|------|
| [everestmz/cursor-rpc](https://github.com/everestmz/cursor-rpc) | Older AiService `StreamChat*` pin (0.43.5); no Bidi/ChatService |
| [jacksonkasi / copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor) | Path A: `cus-` + Anthropic→OpenAI tools |
| [eisbaw/cursor_api_demo](https://github.com/eisbaw/cursor_api_demo) | Modern Unified / Bidi / api5 Agent plane |
| [burpheart/cursor-tap](https://github.com/burpheart/cursor-tap) | MITM observe/decode modern Agent (no local fulfill) |
| [Cursor Network Configuration](https://cursor.com/docs/enterprise/network-configuration) | Host map + streaming constraints |
| Glider code | Ground truth: `internal/mitm/*`, `internal/cursorrpc/*` |

---

## Traffic map (hosts)

| Host / pattern | Purpose | Default MITM | Intercept LLM? |
|----------------|---------|--------------|----------------|
| `api2.cursor.sh` | Most API / older AiService | allowlisted | Yes — chat/OpenAI-shaped + Path B |
| `api3` / `api4` (+ regional) | Cursor Tab | partially allowlisted | Usually no |
| `api5` / `*.api5.cursor.sh` / `agent*.api5` | Agent + NAL | allowlisted | Yes for Agent RPCs if decrypted |

Prefer HTTP/1.1 (`cursor.general.disableHttp2: true`) so Cursor honors `http.proxy`.

---

## Dual-path summary (see protocol doc for detail)

| Path | Mechanism | Glider use |
|------|-----------|------------|
| **A** | Override OpenAI Base URL + unknown model prefix (`cus-…`) | **Primary for Agent+tools** |
| **B** | MITM CONNECT decrypt + BidiAppend → RunSSE | **Text fulfill shipped**; tools → origin |

Full architecture, codec notes, and remaining G13 work: [`cursor_agent_protocol_interception.md`](./cursor_agent_protocol_interception.md). Capture wire notes: [`cursor_agent_rpc_debug_findings.md`](./cursor_agent_rpc_debug_findings.md).
