# Planning docs — index

Design rationale and architecture notes. **Code and tests are the authority on
current behavior**; these docs explain *why* it's built this way, and record
findings that took real live testing to establish. User-facing docs live under
[`../docs/`](../docs/README.md).

**Start here:** [glider_high_level_design.md](glider_high_level_design.md) for
the whole-system view.

## Core design

| Doc | Covers |
|---|---|
| [glider_high_level_design.md](glider_high_level_design.md) | System-level HLD: component map, request-flow walkthroughs, platform matrix, cross-cutting concerns |
| [ngl_and_adapters.md](ngl_and_adapters.md) | How Glider supports multiple CLIs without vendor-specific core code: the rule, both adapter layers (NGL wire-format + `VendorAdapter` execution), every interface contract, the per-vendor matrix, the add-a-vendor checklist, and the wider orchestration vision this is one slice of |
| [permission_relay_design.md](permission_relay_design.md) | Cross-CLI delegation: headless run → denial detection → relay to the user → resume, including live-test findings and known limits per vendor |
| [transparent_redirector_design.md](transparent_redirector_design.md) | OS-level, zero-cooperation traffic interception — WinDivert (Windows) and iptables + `SO_ORIGINAL_DST` (Linux), both shipped and live-verified |
| [routing_and_context.md](routing_and_context.md) | Local-vs-cloud routing, turn-family sticky, tool-step re-decide, the context graph, and session memory |
| [tools.md](tools.md) | Builtin tool registry, workspace sandbox, MCP (GitHub OAuth/PAT, dashboard UI) |

## Protocol research

Live-traced vendor behavior the designs above are built on. These are
**findings, not plans** — each claim was confirmed against real captured
traffic, and the docs say so explicitly where something is inferred rather than
observed.

| Doc | Covers |
|---|---|
| [agent_cli_interop.md](agent_cli_interop.md) | claude / cursor-agent / agy CLI protocols, flags, and headless permission behavior |
| [cursor_agent_research.md](cursor_agent_research.md) | Cursor's private Connect-RPC wire format (`BidiAppend` / `RunSSE` / `AgentService/Run`), the tool codec, and a prior-art survey |

## Reference material

`vendor_ref/` — `.proto` schemas (Cursor's `agent.v1` / `aiserver.v1`) kept for
offline reading; not a build dependency. `internal/cursorrpc` reads and writes
both protocols on the wire, with no generated types.

---

**Consolidated 2026-07-31**, from 14 docs to 8:
`adapter_boundary.md` + `native_glider_orchestration.md` +
`ngl_interface_reference.md` → **`ngl_and_adapters.md`**;
`smart_routing_and_local_tools.md` + `routing_session_policy.md` +
`context_management.md` → **`routing_and_context.md`**;
`glider_orchestration_roadmap.canvas.tsx` deleted (it described the
hoop/swarm orchestration runtime removed in the v1 CLI-interop pass).
