# Planning docs — index

Design rationale and architecture notes. Code + tests are the authority on current behavior; these docs explain *why* it's built this way. User-facing docs live under [`../docs/`](../docs/README.md); current build status is [`../STATUS.md`](../STATUS.md).

## Core design

| Doc | Covers |
|---|---|
| [glider_high_level_design.md](glider_high_level_design.md) | System-level HLD: component map, request-flow walkthroughs, platform matrix, cross-cutting concerns — start here for the whole-system view |
| [adapter_boundary.md](adapter_boundary.md) | The two adapter layers — NGL (wire format) vs `VendorAdapter` (execution behavior) — and exactly what touches a file when adding a 4th vendor CLI |
| [native_glider_orchestration.md](native_glider_orchestration.md) | NGL (Native Glider Language): the canonical `Turn`/`Part` envelope every vendor's wire format translates through — the vision |
| [ngl_interface_reference.md](ngl_interface_reference.md) | NGL exhaustive interface reference: every type/method/contract, per-vendor implementation matrix, real call sites — the precise detail |
| [permission_relay_design.md](permission_relay_design.md) | Cross-CLI delegation: headless run → denial detection → relay to the user → resume, including live-test findings and known limits per vendor |
| [transparent_redirector_design.md](transparent_redirector_design.md) | OS-level, zero-cooperation traffic interception — WinDivert (Windows) and iptables + `SO_ORIGINAL_DST` (Linux), both shipped and live-verified |
| [agent_cli_interop.md](agent_cli_interop.md) | Live-traced protocols for claude / cursor-agent / agy CLIs that the above designs are built on |
| [cursor_agent_research.md](cursor_agent_research.md) | Cursor-specific wire format research (RunSSE, BidiAppend, tool codec) and prior-art survey |

## Routing and context

| Doc | Covers |
|---|---|
| [smart_routing_and_local_tools.md](smart_routing_and_local_tools.md) | Task classifier, complexity scoring, local-context handling |
| [routing_session_policy.md](routing_session_policy.md) | Turn-family sticky routing and its analytics definitions |
| [context_management.md](context_management.md) | `contextgraph`: event log, turn index, episode store, prune/export |

## Tools

| Doc | Covers |
|---|---|
| [tools.md](tools.md) | Builtin tool registry, workspace sandbox, MCP (GitHub OAuth/PAT, dashboard UI) |

## Reference material

`vendor_ref/` — mirrored `.proto` schemas (Cursor's `agent.v1`/`aiserver.v1`) kept for offline reading; not a build dependency (see `internal/cursorrpc/THIRD_PARTY.md` for the actual vendored Go module).
