# Cursor verification checklist

## Prerequisites

- [ ] Ollama running with at least one model from `glider.yaml` (e.g. `codellama:7b`) if testing local routes
- [ ] `go build -o glider.exe ./cmd/glider && .\glider.exe --config configs\glider.yaml`
- [ ] Gateway `http://localhost:8080/healthz` → `ok`
- [ ] Dashboard `http://localhost:8081` loads
- [ ] MITM listening on `:8082` (log: `glider MITM proxy listening`)
- [ ] CA exists at `%USERPROFILE%\.glider\mitm\ca.crt`

## Mode A — BYOK gateway

- [ ] Settings → Models: OpenAI API key set; Override Base URL = `http://localhost:8080/v1`
- [ ] Ask / Chat with an OpenAI-path model → response returns
- [ ] Prompt with `/local` → dashboard shows local route
- [ ] Prompt with `/cloud` → dashboard shows cloud (needs `OPENAI_API_KEY`)
- [ ] Agent with Responses-shaped body → no `missing messages` error (gateway translates)

## Mode B — MITM

- [ ] CA trusted in Windows Trusted Root store (Cursor-only proxy — does not change normal Windows internet for apps not using Glider)
- [ ] `NODE_EXTRA_CA_CERTS` points at Glider `ca.crt`
- [ ] `settings.json` has `http.proxy` → `http://127.0.0.1:8082`, `proxySupport: override`, `disableHttp2: true`
- [ ] Fully quit + relaunch Cursor
- [ ] Agent chat with a built-in Cursor model (Claude / Cursor model) completes
- [ ] Without `/local`, traffic reaches Cursor origin (subscription works)
- [ ] With `/local` on a chat/completions or Responses body, local model answers
- [ ] Non-LLM Cursor HTTPS still works (blind tunnel for non-allowlisted hosts)

## Known risks

- Some Cursor versions ignore `http.proxy` on Agent HTTP/2 paths — stay on HTTP/1.1
- Proprietary Cursor envelopes that are not chat/Responses always passthrough
