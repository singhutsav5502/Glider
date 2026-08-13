# Glider documentation

> Start here for guides and reference. Design rationale and internals live under [`planning/`](../planning/README.md).

## Guides

| Doc | Description |
|---|---|
| [SETUP.md](SETUP.md) | Install, build, first run, and CLI integration (gateway mode, MITM mode, delegation) |
| [instructions.md](instructions.md) | Auto-delegation: make your CLI hand tasks to another CLI on its own. Also on the site as [Auto-rules](site/instructions.html) — same template, prose in Simplified Technical English |
| [CURSOR_CHECKLIST.md](CURSOR_CHECKLIST.md) | Verification checklist for gateway (Mode A) and MITM (Mode B) with Cursor |
| [MITM_NETWORK.md](MITM_NETWORK.md) | MITM / transparent-interception networking: ports, CONNECT + TLS forge, host allowlist, CA, delegation |

## Hostable site (`site/`)

Static HTML product docs — serve locally or host anywhere:

```bash
powershell -File scripts/serve-docs.ps1
# → http://127.0.0.1:8090
```

Or with Glider running from the repo root: [http://127.0.0.1:8081/docs/](http://127.0.0.1:8081/docs/)

| Page | Topic |
|---|---|
| [site/index.html](site/index.html) | Home / navigation hub |
| [site/tutorial.html](site/tutorial.html) | Walkthrough: empty machine to a cross-CLI handoff, in 12 steps |
| [site/architecture.html](site/architecture.html) | Surfaces, shared pipeline, package map, failure modes |
| [site/configuration.html](site/configuration.html) | Every config key with type, default and function; profiles; hot-reload vs restart |
| [site/routing.html](site/routing.html) | Rule schema, all six trigger types, the Starlark contract, priority ladder, Cursor sticky logic |
| [site/delegation.html](site/delegation.html) | Cross-CLI delegation, permission relay, vendor registry, clean vs raw |
| [site/instructions.html](site/instructions.html) | Auto-delegation template (generated from `instructions.md`) |
| [site/mitm.html](site/mitm.html) | Gateway vs MITM, the CA and how to trust it, transparent interception, Cursor's Agent RPC plane |
| [site/ngl.html](site/ngl.html) | The three extension points — `OriginAdapter`, `DelegateRenderer` and the `ParseXTurn` functions — and how they fit together |
| [site/context.html](site/context.html) | Context graph, event log, and the continuity record every delegate receives |
| [site/pure-local.html](site/pure-local.html) | Ollama-only configuration, and the two paths that still reach the network |
| [site/api.html](site/api.html) | Dashboard HTTP API, the WebSocket events, worked examples |
| [site/mcp.html](site/mcp.html) | All 17 internal tools with their inputs, the sandbox, MCP integration |

## Configuration

| File | Purpose |
|---|---|
| [configs/glider.yaml](../configs/glider.yaml) | Default dual-mode profile (gateway + MITM + dashboard) |
| [configs/glider.local.yaml](../configs/glider.local.yaml) | Ollama-only, no cloud fallback |
| [configs/glider.cloud.yaml](../configs/glider.cloud.yaml) | Cloud-oriented gateway (BYOK bias, MITM off) |
| [configs/glider.transparent-test.yaml](../configs/glider.transparent-test.yaml) | Profile used to test transparent interception |
| [configs/vendor_candidates.yaml](../configs/vendor_candidates.yaml) | CLI vendors Glider discovers/delegates to (claude, cursor-agent, agy) |
| [.env.example](../.env.example) | Template for API keys and secrets |

## Planning & design docs

Start at [planning/README.md](../planning/README.md) — the design-doc index covers NGL, the VendorAdapter boundary, permission relay, transparent interception, and routing/context internals.
