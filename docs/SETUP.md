# Glider — step-by-step setup

Get Glider running locally (Ollama + dashboard + optional Cursor). Windows-focused; Linux/macOS commands are analogous (`./glider` instead of `.\glider.exe`).

---

## 0. Prerequisites

| Tool | Why |
|------|-----|
| **Go 1.22+** (1.25 OK) | Build `cmd/glider` |
| **Git** | Clone this repo |
| **Ollama** | Local inference (default model `qwen2.5-coder:14b`) |
| Optional: **OpenAI / Anthropic API keys** | Gateway “cloud” / BYOK |
| Optional: **Cursor** | Path A (Base URL) and/or Path B (MITM proxy) |

Check:

```powershell
go version
ollama --version
```

---

## 1. Clone and build

```powershell
cd D:\___repos\Glider   # or your clone path
go test ./internal/loop/ ./internal/tools/ -count=1   # smoke
go build -o glider.exe ./cmd/glider
```

---

## 2. Pull the default local model

```powershell
ollama serve          # if not already running as a service
ollama pull qwen2.5-coder:14b
```

Configs probe `http://127.0.0.1:11434` (prefer `127.0.0.1` over `localhost` on Windows).

---

## 3. Choose a config profile

| Profile | File | Use when |
|---------|------|----------|
| **Dual mode** (default) | `configs/glider.yaml` | Gateway + MITM + dashboard |
| **Pure local** | `configs/glider.local.yaml` | Ollama only, no cloud fallback |
| **Gateway-oriented cloud** | `configs/glider.cloud.yaml` | Bias toward BYOK cloud |

Optional env file (not committed): copy [`.env.example`](../.env.example) → `.env.local` for API keys (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, web search keys, etc.). Glider loads dotenv on start when present.

---

## 4. Start Glider

From the **repo root** (so relative config + `docs/site` resolve):

```powershell
.\glider.exe --config configs\glider.yaml
```

You should see listeners roughly:

| Port | URL | Role |
|------|-----|------|
| **8080** | http://127.0.0.1:8080 | OpenAI-compatible gateway |
| **8081** | http://127.0.0.1:8081 | Dashboard |
| **8082** | MITM proxy | Cursor `http.proxy` (if MITM enabled) |

Health check:

```powershell
curl http://127.0.0.1:8080/healthz
# → ok
```

Open the dashboard: [http://127.0.0.1:8081](http://127.0.0.1:8081)

---

## 5. First hoop (no Cursor required)

### Seed sample hoops + swarm templates

```powershell
powershell -File scripts\seed-samples.ps1
```

### Start the smoke hoop

```powershell
go run ./scripts/loadhoop -file samples/hoops/hello-critic.yaml -start
```

Or in the dashboard: **Hoops & Swarm** → find `hello-critic` → **Start**.

Watch:

1. **Agent log** on the hoop card / graph rail  
2. **Workspace** tab → `runs/hello-critic/work` and `out`  
3. HITL **Approve** if it pauses on `human_gate`

Clear stale results before re-runs if CONTEXT/plan facts look poisoned: hoop **Clear results**.

---

## 6. Workspace mental model

Tools are sandboxed under:

```text
%USERPROFILE%\.glider\workspace\
  runs\<hoop-or-turn-id>\
    work\     ← clones, scratch, intermediate
    out\      ← final reports
```

- Drop a **workspace** stage early in the graph (`run` = fresh boxes, `existing` = reuse a path under the sandbox).
- Sample: `samples/hoops/workspace-existing-bind.yaml`.

---

## 7. Optional — Cursor Mode A (BYOK gateway)

Best path for **Agent + tools** against local/BYOK models.

1. Glider running with `configs/glider.yaml` (or cloud profile).
2. Cursor → **Settings → Models**:
   - Enable OpenAI API Key (can be a dummy if you only hit local via aliases — better: set a real key for cloud fallback).
   - **Override OpenAI Base URL** → `http://127.0.0.1:8080/v1`
3. Prefer model ids with `cus-` prefix when forcing Agent onto the gateway (e.g. `cus-qwen2.5-coder:14b`).
4. Try prompts: `/local …` and `/cloud …` — dashboard Overview should reflect route mix.

Full checklist: [`docs/CURSOR_CHECKLIST.md`](CURSOR_CHECKLIST.md).

---

## 8. Optional — Cursor Mode B (MITM / Path B)

Use when you want Cursor **Agent** TLS (subscription hosts) to hit Glider first.

1. Trust Glider MITM CA (`%USERPROFILE%\.glider\mitm\ca.crt`) in Windows Trusted Root.
2. Point `NODE_EXTRA_CA_CERTS` at that `ca.crt`.
3. Cursor `settings.json` (sketch):

```json
{
  "http.proxy": "http://127.0.0.1:8082",
  "http.proxySupport": "override",
  "http2": { "disabled": true }
}
```

4. Fully quit + relaunch Cursor.
5. Config: `mitm.agent_rpc_fulfill: true`, keep `agent_rpc_canned_on_error: false`.
6. Expect **text-only** local fulfill; tool-heavy Agent still prefers Mode A unless you opt into `mitm.agent_rpc_tool_codec`.

`/cloud` in TipTap = **StickyCloud** turn family → Cursor origin for that turn (+ wrap-up), with `runs/<turn-id>/{work,out}` still associated in the sandbox.

Deep dive (CONNECT, allowlist, BidiAppend→RunSSE, sticky TTL, file map): [`docs/MITM_NETWORK.md`](MITM_NETWORK.md).

---

## 9. Optional — GitHub MCP

1. Dashboard → **MCP**.
2. Connect GitHub (device flow or PAT per UI).
3. Declare tools on hoop stages (`kind: mcp`, `server: github`).

See [`docs/site/mcp.html`](site/mcp.html) and [`planning/tools_mcp.md`](../planning/tools_mcp.md).

---

## 10. Config hot-reload vs restart

**Hot** (Config save or edit `glider.yaml` while running): routing, model aliases, context threshold, log level, **backend/model clients**.

**Restart required:** listen ports, MITM CA/hosts.

Check: **Hoops → hot-swap modules** (`backends` row) or response header `X-Glider-Backend-Reload` after Config save.

---

## 11. Docs and deeper reading

| Doc | Topic |
|-----|--------|
| [docs/MITM_NETWORK.md](MITM_NETWORK.md) | MITM / Cursor networking brief |
| [docs/blog/orchestration-deep-dive.md](blog/orchestration-deep-dive.md) | Orchestration blog + Excalidraw |
| [docs/site/](site/) | Hostable product docs (`scripts\serve-docs.ps1`) |
| [planning/remaining_gaps.md](../planning/remaining_gaps.md) | Feature matrix |
| [planning/intentional_backlog.md](../planning/intentional_backlog.md) | Deferred enterprise items |

Serve docs locally:

```powershell
powershell -File scripts\serve-docs.ps1
# → http://127.0.0.1:8090
```

With Glider running from repo root: [http://127.0.0.1:8081/docs/](http://127.0.0.1:8081/docs/).

---

## 12. Troubleshooting (fast)

| Symptom | Fix |
|---------|-----|
| Local Completes hang / truncate | Raise `thresholds.request_timeout` / `default_max_tokens`; use 14b coder model |
| Tools write into Glider repo | Workspace should be `~/.glider/workspace` — check Config → tools.workspace |
| Clone OK but `fs_list` misses | Use ScopeRel paths (`audit-target`); ensure same run id |
| Critic chatty / no score | Critic must emit `SCORE:`; leave tools empty on critic |
| HITL marked success wrongly | Upgrade past Success=`success && !waitHuman` fix; Approve then resume |
| MITM Agent tools broken | Use Mode A for tools; Path B codec is opt-in / partial |

Still stuck: process logs (`log_level: debug`) + dashboard agent log for the hoop id.
