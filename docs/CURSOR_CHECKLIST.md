# Cursor verification checklist

## Prerequisites

- [ ] Ollama running with at least one model from `glider.yaml` (e.g. `qwen2.5-coder:14b`) if testing local routes
- [ ] `go build -o glider.exe ./cmd/glider && .\glider.exe --config configs\glider.yaml`
- [ ] Gateway `http://localhost:8080/healthz` → `ok`
- [ ] Dashboard `http://localhost:8081` loads (Overview shows Path B text-only Agent banner; LOCAL/CLOUD % includes `origin_passthrough`)
- [ ] Optional: `GET http://localhost:8081/api/metrics` returns `distribution.local_pct` / `cloud_pct`
- [ ] MITM listening on `:8082` (log: `glider MITM proxy listening`)
- [ ] CA exists at `%USERPROFILE%\.glider\mitm\ca.crt`

## Mode A — BYOK gateway (recommended for Agent + tools)

- [ ] Settings → Models: OpenAI API key set; Override Base URL = `http://localhost:8080/v1`
- [ ] Ask / Chat with an OpenAI-path model → response returns
- [ ] Model id uses `cus-` prefix when forcing Agent onto the gateway (e.g. `cus-qwen2.5-coder:14b`)
- [ ] Prompt with `/local` → dashboard shows local route
- [ ] Prompt with `/cloud` → dashboard shows cloud (needs `OPENAI_API_KEY`)
- [ ] Responses-shaped body → no `missing messages` error (gateway translates)

## Mode B — MITM (TLS decrypt + Path B text fulfill)

- [ ] CA trusted in Windows Trusted Root store (Cursor-only proxy — does not change normal Windows internet for apps not using Glider)
- [ ] `NODE_EXTRA_CA_CERTS` points at Glider `ca.crt`
- [ ] `settings.json` has `http.proxy` → `http://127.0.0.1:8082`, `proxySupport: override`, `disableHttp2: true`
- [ ] Fully quit + relaunch Cursor
- [ ] `mitm.agent_rpc_fulfill: true` and `agent_rpc_canned_on_error: false` (prefer real Ollama)
- [ ] Agent **text-only** chat (no tools) with a built-in Cursor model → local fulfill when rules say local (`mitm runsse local fulfill` in process logs)
- [ ] With `debug_agent_rpc: true`, dumps appear under `%USERPROFILE%\.glider\mitm-debug`
- [ ] Agent **with tools** still reaches Cursor origin for child RunSSE when codec **off** (expected); use Mode A for tools locally
- [ ] Optional Path B tool codec: `mitm.agent_rpc_tool_codec: true` — mapped tools emit Cursor ToolCall oneofs; unknown → Truncated without hang (see `planning/cursor_agent_research.md` inventory). **Live UI sign-off still open.**
- [ ] Dashboard Overview does **not** flood with CONNECT `decrypt` 0-token rows
- [ ] If a rare OpenAI-shaped POST appears on an allowlisted host with `/local`, local model answers
- [ ] Non-LLM Cursor HTTPS still works (blind tunnel for non-allowlisted hosts)

## Known limits

- **Path B MITM — text-only Agent** when `agent_rpc_fulfill` is on (BidiAppend → root RunSSE). Child/tool RunSSE → origin; Agent+tools → Mode A (`cus-` + Override Base URL). See [cursor_agent_research.md](../planning/cursor_agent_research.md); routing roadmap [smart_routing_and_local_tools.md](../planning/smart_routing_and_local_tools.md).
- Some Cursor versions ignore `http.proxy` on Agent HTTP/2 paths — stay on HTTP/1.1
- Proprietary Cursor envelopes that are not chat/Responses / fulfillable StreamChat* / Path B text RunSSE always passthrough
