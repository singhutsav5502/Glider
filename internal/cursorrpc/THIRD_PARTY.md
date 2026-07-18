# Third-party: cursor-rpc

Glider uses [everestmz/cursor-rpc](https://github.com/everestmz/cursor-rpc) as a Go module dependency for Cursor `aiserver.v1` protobuf schemas and generated types.

- Module: `github.com/everestmz/cursor-rpc`
- Pinned commit: `83d363192331ea3299cabe2c843b3a0d4e9fd7bc` (2024-12-17)
- Cursor version stamped in upstream: `0.43.5`
- License: **no LICENSE file published** in that repository as of integration. We depend on the public Go module for interoperability schemas only; we do not re-license or claim ownership of reverse-engineered protos. Attribute upstream authorship to Everest Munro-Zeisberger / everestmz.
- Related article (architecture, not a dependency): [How I Reverse-Engineered Cursor IDE to Run on GitHub Copilot](https://dev.to/jacksonkasi/how-i-reverse-engineered-cursor-ide-to-run-on-github-copilot-a-proxy-architecture-deep-dive-2jin) / [jacksonkasi1/copilot-for-cursor](https://github.com/jacksonkasi1/copilot-for-cursor)
- RunSSE / `agent.v1` field map (reference only, not a Go module): [burpheart/cursor-tap](https://github.com/burpheart/cursor-tap) `cursor_proto/agent_v1.proto` — mirrored under `planning/vendor_ref/agent_v1.proto` for offline R&D. cursor-tap observes/forwards; Glider hand-encodes a minimal `AgentServerMessage` subset.

If upstream adds a license incompatible with Glider's distribution, revisit this dependency.
