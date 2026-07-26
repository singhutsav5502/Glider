# Glider documentation

> Start here for guides, reference, and deep dives.

## Guides

| Doc | Description |
|-----|-------------|
| **[SETUP.md](SETUP.md)** | Step-by-step: install prerequisites, build, pull models, start Glider, run your first hoop, configure Cursor (Mode A / Mode B) |
| **[CURSOR_CHECKLIST.md](CURSOR_CHECKLIST.md)** | Verification checklist for Mode A (BYOK gateway) and Mode B (MITM Agent fulfill) |
| **[MITM_NETWORK.md](MITM_NETWORK.md)** | MITM / Cursor networking: ports, Path A vs B, CONNECT + TLS forge, allowlist, CA, BidiAppend→RunSSE, `/cloud` sticky |

## Hostable site (`site/`)

Static HTML product docs — serve locally or host anywhere (GitHub Pages, Netlify, etc.):

```bash
powershell -File scripts/serve-docs.ps1
# → http://127.0.0.1:8090
```

Or with Glider running from the repo root: [http://127.0.0.1:8081/docs/](http://127.0.0.1:8081/docs/)

| Page | Topic |
|------|-------|
| [site/index.html](site/index.html) | Home / navigation hub |
| [site/architecture.html](site/architecture.html) | Shared harness (model alias → route → execute) vs hoop mission shape |
| [site/routing.html](site/routing.html) | Routing rule priority + sticky logic |
| [site/context.html](site/context.html) | Context graph, event log, episode store |
| [site/path-a-b.html](site/path-a-b.html) | Gateway (Mode A) vs MITM (Mode B) |
| [site/loop-engineering.html](site/loop-engineering.html) | Hoops, stages, fan-out, swarm |
| [site/pure-local.html](site/pure-local.html) | Ollama-only configuration |
| [site/api.html](site/api.html) | Dashboard HTTP API reference |
| [site/samples.html](site/samples.html) | Runnable sample hoops walkthrough |
| [site/orchestration-blog.html](site/orchestration-blog.html) | Orchestration blog hub + diagram links |

## Deep dives

| Doc | Description |
|-----|-------------|
| [blog/orchestration-deep-dive.md](blog/orchestration-deep-dive.md) | Orchestration architecture deep dive (+ [Excalidraw diagrams](blog/diagrams/)) |

## Samples

| Path | Description |
|------|-------------|
| [samples/hoops/](../samples/hoops/) | Runnable hoop YAML: enterprise incidents, compliance packs, security audits, smoke tests |
| [samples/hoops/README.md](../samples/hoops/README.md) | Sample index with load/run instructions |

## Configuration

| File | Purpose |
|------|---------|
| [configs/glider.yaml](../configs/glider.yaml) | Default dual-mode profile (gateway + MITM + dashboard) |
| [configs/glider.cloud.yaml](../configs/glider.cloud.yaml) | Cloud-oriented gateway (BYOK bias, MITM off) |
| [.env.example](../.env.example) | Template for API keys and secrets |

## Planning & engineering notes

| Doc | Description |
|-----|-------------|
| [planning/README.md](../planning/README.md) | Planning index (start here for internals) |
| [planning/remaining_gaps.md](../planning/remaining_gaps.md) | Feature status matrix — 16 areas |
| [planning/intentional_backlog.md](../planning/intentional_backlog.md) | Deferred-by-design decisions |
| [planning/loop_engineering.md](../planning/loop_engineering.md) | Loop Engineering canonical reference |
| [planning/tools_mcp.md](../planning/tools_mcp.md) | Tools, ScopeRel sandbox, web search, MCP |
