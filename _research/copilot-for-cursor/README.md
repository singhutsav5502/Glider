# 🚀 Copilot Proxy for Cursor

> Forked from [jacksonkasi1/copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor) with full Anthropic → OpenAI conversion + Responses API bridge.

**Unlock the full power of GitHub Copilot in Cursor IDE.**

Use **all** Copilot models (GPT-5.4, Claude Opus 4.6, Gemini 3.1, etc.) in Cursor — including Plan mode, Agent mode, and tool calls.

---

## ⚡ Quick Start

### One Command (npm)

```bash
npx copilot-for-cursor
```

> Requires [Bun](https://bun.sh/) installed. First run will prompt GitHub authentication.

This starts both `copilot-api` (port 4141) and the proxy (port 4142) in a single terminal.

### Or from source

```bash
git clone https://github.com/CharlesYWL/copilot-for-cursor.git
cd copilot-for-cursor
bun run start.ts
```

### Enable Max Mode (auto-compact long conversations)

```bash
bun run start.ts --max
```

> **Max mode** automatically compacts conversation history when the estimated token count exceeds 80% of the model's input token limit. It summarizes older messages into a structured summary while keeping the most recent messages intact — letting you have much longer coding sessions without hitting token limits.

> **🛡️ Always-on safety net:** Even without `--max`, the proxy now auto-compacts at **95%** of the model's input limit and falls back to hard truncation of the oldest messages if summarization fails. This prevents Cursor from ever hitting upstream `context_length_exceeded` errors. Use `--max` if you want proactive (80%) compaction for smoother long sessions.

### Then start an HTTPS tunnel

Cursor requires HTTPS. You have two options:

**Option A — One-click tunnel (recommended)**

Open the dashboard at `http://localhost:4142/`, go to the **Tunnel** tab, pick a provider (Cloudflare, ngrok, or bore) and click **Start Tunnel**. The public URL, QR code, and Cursor endpoint will appear instantly. Cloudflare is pre-installed automatically — no signup, no config.

**Option B — Run a tunnel manually**

```bash
# Cloudflare (free, no signup)
cloudflared tunnel --url http://localhost:4142

# Or ngrok
ngrok http 4142
```

Copy the HTTPS URL (e.g., `https://xxxxx.trycloudflare.com`).

---

## 🏗 Architecture

```text
Cursor → (HTTPS tunnel) → proxy-router (:4142) → copilot-api (:4141) → GitHub Copilot
```

*   **Port 4141 (`copilot-api`):** Authenticates with GitHub, provides the OpenAI-compatible API, and natively handles the Responses API for GPT-5.x models.
    *   *Powered by [@jeffreycao/copilot-api](https://github.com/caozhiyuan/copilot-api) (installed via `npx`).*
*   **Port 4142 (`proxy-router`):** Converts Anthropic-format messages to OpenAI format, bridges Responses API for GPT-5.x models, handles the `cus-` prefix, and serves the dashboard.
*   **HTTPS tunnel:** Cursor requires HTTPS — a tunnel exposes the local proxy.

### Proxy Router Modules

| File | Responsibility |
|---|---|
| `proxy-router.ts` | Entrypoint — Bun.serve, routing, CORS, dashboard, model list |
| `anthropic-transforms.ts` | Anthropic → OpenAI normalization (fields, tools, messages) |
| `responses-bridge.ts` | Chat Completions → Responses API bridge for GPT-5.x / goldeneye |
| `responses-converters.ts` | Responses API → Chat Completions format (sync & streaming SSE) |
| `stream-proxy.ts` | Streaming passthrough with chunk logging and error detection |
| `debug-logger.ts` | Request/response debug logging helpers |
| `start.ts` | One-command launcher for copilot-api + proxy-router |
| `max-mode.ts` | Auto-compaction for long conversations (`--max` flag) |
| `usage-db.ts` | Persistent request/token usage tracking |
| `auth-config.ts` | API key generation, validation, and config persistence |
| `upstream-auth.ts` | Upstream copilot-api authentication and key management |

---

## ⚙️ Cursor Configuration

1.  Go to **Settings** (Gear Icon) → **Models**.
2.  Add a new **OpenAI Compatible** model:
    *   **Base URL:** `https://your-tunnel-url.trycloudflare.com/v1`
    *   **API Key:** `dummy` (any value works)
    *   **Model Name:** Use a **prefixed name** — e.g., `cus-gpt-5.4`, `cus-claude-opus-4.6`

> **⚠️ Important:** You **must** use the `cus-` prefix. Without it, Cursor routes the request to its own backend.

> **💡 Tip:** Visit the [Dashboard](http://localhost:4142) to see all available models and copy their IDs.

### Tested Models (19/20 passing)

| Cursor Model Name | Actual Model | Status |
|---|---|---|
| `cus-gpt-4o` | GPT-4o | ✅ |
| `cus-gpt-4.1` | GPT-4.1 | ✅ |
| `cus-gpt-41-copilot` | GPT-4.1 Copilot | ❌ Not supported by GitHub |
| `cus-gpt-5-mini` | GPT-5 Mini | ✅ |
| `cus-gpt-5.1` | GPT-5.1 | ✅ (deprecating 2026-04-15) |
| `cus-gpt-5.2` | GPT-5.2 | ✅ |
| `cus-gpt-5.2-codex` | GPT-5.2 Codex | ✅ |
| `cus-gpt-5.3-codex` | GPT-5.3 Codex | ✅ |
| `cus-gpt-5.4` | GPT-5.4 | ✅ |
| `cus-gpt-5.4-mini` | GPT-5.4 Mini | ✅ |
| `cus-claude-haiku-4.5` | Claude Haiku 4.5 | ✅ |
| `cus-claude-sonnet-4` | Claude Sonnet 4 | ✅ |
| `cus-claude-sonnet-4.5` | Claude Sonnet 4.5 | ✅ |
| `cus-claude-sonnet-4.6` | Claude Sonnet 4.6 | ✅ |
| `cus-claude-opus-4.5` | Claude Opus 4.5 | ✅ |
| `cus-claude-opus-4.6` | Claude Opus 4.6 | ✅ |
| `cus-gemini-2.5-pro` | Gemini 2.5 Pro | ✅ |
| `cus-gemini-3-flash-preview` | Gemini 3 Flash | ✅ |
| `cus-gemini-3.1-pro-preview` | Gemini 3.1 Pro | ✅ |
| `cus-text-embedding-3-small` | Text Embedding 3 Small | N/A (embedding model) |

> All GPT-5.x models now work thanks to the switch to [@jeffreycao/copilot-api](https://github.com/caozhiyuan/copilot-api), which natively supports the Responses API. The proxy also includes its own Responses API bridge as a fallback.

![Cursor Settings Configuration](./cursor-settings.png)

---

## ✨ Features

### What the proxy handles

| Cursor sends (Anthropic format) | Proxy converts to (OpenAI format) |
|---|---|
| `system` as top-level field | System message |
| `tool_use` blocks in assistant messages | `tool_calls` array |
| `tool_result` blocks in user messages | `tool` role messages |
| `input_schema` on tools | `parameters` (cleaned) |
| `tool_choice` objects (`auto`/`any`/`tool`) | OpenAI format (`auto`/`required`/function) |
| `stop_sequences` | `stop` |
| `thinking` / `cache_control` blocks | Stripped |
| `metadata` / `anthropic_version` | Stripped |
| Images in Claude requests | `[Image Omitted]` placeholder |
| GPT-5.x `max_tokens` | Converted to `max_completion_tokens` |
| GPT-5.x Responses API | **Bridge built in** (needs `copilot-api` support) |

### Supported Workflows

*   **💬 Chat & Reasoning:** Full conversation context with all models
*   **📋 Plan Mode:** Works with tool calls and multi-turn conversations
*   **🤖 Agent Mode:** File editing, terminal, search, MCP tools
*   **📂 File System:** `Read`, `Write`, `StrReplace`, `Delete`
*   **💻 Terminal:** `Shell` (run commands)
*   **🔍 Search:** `Grep`, `Glob`, `SemanticSearch`
*   **🔌 MCP Tools:** External tools (Neon, Playwright, etc.)
*   **🗜️ Max Mode:** Auto-compact long conversations to stay within token limits (`--max`)

---

## 🔒 Security

### Dashboard Password

The dashboard is password-protected. On first visit, set a password to prevent unauthorized access.

### API Key Management

Manage API keys directly from the **Endpoint** tab in the dashboard:

1. Toggle **"Require API Key"** to enable authentication
2. Click **"+ Create Key"** to generate a new `cpk-xxx` key
3. Copy the key (shown only once!) and paste it into Cursor's **API Key** field
4. Enable/disable or delete keys as needed

When enabled, all `/v1/*` requests must include `Authorization: Bearer <your-key>`.

![Dashboard](./dashboard-preview.png)

| Usage Tab | Console Log Tab |
|---|---|
| ![Usage](./screenshot-usage.png) | ![Console](./screenshot-console.png) |

---

## 📊 Dashboard

Access the dashboard at **[http://localhost:4142](http://localhost:4142)**

Three tabs:
- **Endpoint** — Proxy URL, API key management, model list
- **Usage** — Request stats, token counts, per-model breakdown, recent requests
- **Console Log** — Real-time proxy logs with color-coded levels

---

## ⚠️ Known Limitations

| Feature | Status |
|---|---|
| Basic chat & tool calling | ✅ Works |
| Streaming | ✅ Works |
| Plan mode | ✅ Works |
| Agent mode | ✅ Works |
| All GPT-5.x models | ✅ Works |
| Max mode (long session compaction) | ✅ Works (`--max` flag) |
| Extended thinking (chain-of-thought) | ❌ Stripped |
| Prompt caching (`cache_control`) | ❌ Stripped |
| Claude Vision | ❌ Not supported via Copilot |
| Tunnel URL changes on restart | ⚠️ Use paid plan for fixed subdomain |

---

## 📝 Troubleshooting

**"Model name is not valid" in Cursor:**
Make sure you're using the `cus-` prefix (e.g., `cus-gpt-5.4`, not `gpt-5.4`).

**Plan mode response cuts off:**
Ensure `idleTimeout: 255` is set in `proxy-router.ts` (already configured). Slow models like Opus need longer timeouts.

**GPT-5.x returns "use /v1/responses":**
The proxy auto-routes these. Make sure you're running the latest version.

**"connection refused":**
Ensure services are running: `bun run start.ts` or check `http://localhost:4142`.

**Max mode not compacting:**
Compaction only triggers when estimated tokens exceed 80% of the model's limit and there are at least 15 messages. Check the console log for `🗜️ Max mode` messages.

---

> ⚠️ **DISCLAIMER:** This project is **unofficial** and for **educational purposes only**. It interacts with undocumented internal APIs of GitHub Copilot and Cursor. Use at your own risk. The authors are not affiliated with GitHub, Microsoft, or Anysphere (Cursor). Please use your API credits responsibly and in accordance with the provider's Terms of Service.