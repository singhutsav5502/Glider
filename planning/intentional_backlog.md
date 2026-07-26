# Intentional backlog — deferred by design

> **Home for items left open on purpose.** Matrix status stays in [remaining_gaps.md](remaining_gaps.md); SOLID mechanics in [solid_refactor.md](Depreceated/solid_refactor.md).  
> As of **2026-07-24** (post lock-in `08d1336`). Do not treat these as forgotten P0 bugs.

## How to use

- Prefer this doc when scoping a **deferred** initiative (why / prereqs / approach / acceptance / effort / anti-goals).
- Keep [remaining_gaps.md](remaining_gaps.md) as the short SHIPPED / PARTIAL / DEFERRED matrix.
- Dependency order below is recommended next-pass sequencing, not calendar commitment.

## Dependency order (recommended)

| Order | Item | Effort | Depends on |
|------:|------|--------|------------|
| ~~1~~ | ~~Dashboard Server DIP~~ | — | **SHIPPED** |
| ~~2~~ | ~~`contextgraph.Default()` -> explicit injection~~ | — | **SHIPPED** |
| 3 | Hosted Copilot MCP live PAT production verify | S (ops) | Session harden (done) + live PAT |
| 4 | Full Cursor ToolCall catalog + live UI acceptance | L | Common map (done); Cursor build access |
| ~~5~~ | ~~Backend hot-reload (no restart)~~ | — | **SHIPPED** (MVP; MITM/ports still restart) |
| 6 | ~~Phase 3 cross-hoop / Temporal-class feeds~~ | — | **Moot 2026-07-25** — prerequisite deleted, see Â§5g |
| 7 | Leiden at scale | L | Denser EXTRACTED graph |
| 8 | Tree-sitter on Windows | M–L | SymbolIndexer floor (done) |
| 9 | Temporal-class multi-day HITL | L | Product + durable workflow choice |
| 10 | SIEM audit export | M–L | Retention / compliance product |
| 11 | Chargeback UI | M | Billing product decision |
| 12 | SSO / RBAC | L | IdP + tenancy model |

---

## 1. Full Cursor ToolCall catalog + live UI acceptance

### Why deferred
- Scope/risk: Cursor Agent tool wire shapes and UI chrome churn across builds; full catalog is unbounded RE.
- Platform: Path B is MITM/protocol; Path A already demos Agent+tools cleanly.
- Extended common map ships opt-in (`agent_rpc_tool_codec` + `toolcall_map.go`) covering FS/web + Todos/Lints/MCP/SemSearch/Task/Plan/Mode/Exa/…; grind/VM/computer_use remain Truncated.

### Prerequisites
- Stable Cursor install for checklist runs ([docs/CURSOR_CHECKLIST.md](../docs/CURSOR_CHECKLIST.md)).
- Capture corpus of live ToolCall / Truncated / tool-result frames for tools beyond the mapped set.
- Keep fail-soft Truncated path green (`runsse_codec` + fulfill tests).

### Suggested approach / interfaces
- Extend `internal/cursorrpc` map tables only from **observed** frames / `planning/vendor_ref/agent_v1.proto` (no speculative protobuf).
- Keep codec opt-in; default Truncated for unknown tools.
- Acceptance harness: fixture golden + one manual UI pass per Cursor major.
- Prefer documenting “Path A for tools demos” until live UI coverage is signed off.

### Acceptance criteria
- [x] Documented inventory of mapped vs Truncated-only tools for a pinned schema (`planning/tools_mcp.md` § Path B; pin `vendor_ref/agent_v1.proto`).
- [ ] Live UI: at least common tools round-trip without composer breakage; unknown tools Truncated without hang.
- [x] Regression tests for each newly mapped tool shape (`toolcall_map_test.go`).
- [ ] Checklist section signed off on a real install.

### Progress (2026-07-24)
- Extended map: UpdateTodos / ReadTodos / ReadLints / Mcp / SemSearch / CreatePlan / Task / List+Read MCP resources / AskQuestion / SwitchMode / ApplyAgentDiff / Exa* / GenerateImage / WriteShellStdin / Reflect (+ aliases).
- Still open: live Cursor UI sign-off; Truncated-only grind/VM/computer_use/record_screen/bugbot.

### Effort
**L** (ongoing with Cursor releases).

### What NOT to do
- Do not claim full protocol RE or a native IDE plugin.
- Do not block Path A demos on Path B catalog completeness.
- Do not invent tool argument schemas without wire evidence.

---

## 2. Hosted Copilot MCP live PAT production verify

### Why deferred
- Ops/infra, not missing code: session persist/retry + `X-MCP-Toolsets` + initialize auth refresh already hardened.
- Risk: production quirks (auth headers, rate limits, toolset gating) need a real PAT and network.

### Prerequisites
- Valid GitHub Copilot MCP PAT (or org-approved equivalent) in local `.env` / gitignored store.
- Dashboard MCP tab or status API reachable against hosted endpoint.
- Clear rollback: revoke PAT after verify.

### Suggested approach / interfaces
- Use existing `internal/mcp` HTTP transport + credentials store; no new auth protocol.
- Scripted verify: connect -> list tools -> invoke one read-only tool -> status healthy.
- Record quirks in `planning/tools_mcp.md` (not secrets).

### Acceptance criteria
- [ ] Live connect succeeds with PAT; status reports authenticated.
- [ ] At least one successful tool call; retry path observed or N/A documented.
- [ ] Failure modes (401/403/timeout) surface cleanly in dashboard/status.
- [x] No PAT committed; `.env.example` stays placeholder-only.
- [x] Ops verification checklist documented (commands, success signals, failure modes) — see [tools_mcp.md](tools_mcp.md) § Hosted Copilot MCP — ops verification checklist. **Not marked verified.**

### Progress (2026-07-24)
- Code: handshake retries once on 401/403 after preferring credential-file token; classified auth/timeout errors for status; httptest coverage for session retry + initialize auth refresh.
- Ops live PAT run: **not done** (no fake verified status).

### Effort
**S** (ops half-day) once PAT available.

### What NOT to do
- Do not bake production PATs into configs or tests.
- Do not expand hosted MCP surface area before verify proves session harden sufficient.
- Do not invent a “verified” claim without a real production run.

---

## 3. Dashboard Server DIP (interfaces vs concrete Manager/Runner)

### Why deferred
- Scope: file splits (`api_config` / `api_context` / `api_loop`) already cut SRP; remaining is pure DIP.
- Risk: broad interface churn across handlers/tests without behavior change — schedule as its own pass.

### Prerequisites
- Current handler groups stable (done).
- Inventory of methods each handler group actually calls on `*loop.Manager` / `*swarm.Runner`.

### Suggested approach / interfaces
- Introduce narrow interfaces per handler group, e.g. `LoopAPI`, `SwarmAPI`, `ConfigAPI` (names TBD), implemented by existing Manager/Runner.
- `Server` fields become interfaces; `main`/constructors wire concrete types.
- Prefer compile-time satisfaction (`var _ LoopAPI = (*loop.Manager)(nil)`).
- Tests: fake implementations for handler unit tests where useful.

### Acceptance criteria
- [x] `Server` no longer imports concrete Manager/Runner types as fields (ctors may still accept concretes).
- [x] `go test ./internal/dashboard/...` green; no API behavior change.
- [x] Documented in [solid_refactor.md](Depreceated/solid_refactor.md) as done.

### Effort
**M**.

### What NOT to do
- Do not merge DIP with UI redesign or new endpoints.
- Do not create one mega `Everything` interface.
- Do not move business logic into the dashboard package.

---

## 4. `contextgraph.Default()` global -> explicit injection

### Why deferred
- Mostly already DIP’d via `LoopGraphSink` / injected stores; global remains for legacy call sites and convenience.
- Risk: silent nil / wrong-store bugs if removal is half-done.

### Prerequisites
- `rg` inventory of `contextgraph.Default` / `SetDefault` call sites.
- Confirm hoop/swarm/dashboard/mitm paths already take injected `*Store` or interfaces.

### Suggested approach / interfaces
- Pass `*contextgraph.Store` (or `LoopGraphSink` / query iface) from `cmd/glider` composition root.
- Delete or deprecate `Default()` / `SetDefault()` after last caller migrates.
- Tests construct local stores; no package-level mutation.

### Acceptance criteria
- [x] Zero production callers of `Default()` / `SetDefault()`.
- [x] `go test` packages that touched wiring stay green.
- [x] Startup still seeds one process-wide store via explicit injection only.

### Effort
**S–M**.

### What NOT to do
- Do not keep a “hidden” global under another name.
- Do not couple Store construction to dashboard HTTP package.

---

## 5. Enterprise deferred cluster

### 5a. SSO / RBAC / multi-tenant control plane

| | |
|--|--|
| **Why deferred** | Needs IdP + tenancy model; out of local gateway / single-operator scope. |
| **Prerequisites** | Product decision on tenants; IdP (OIDC/SAML); audit of which APIs are privileged. |
| **Approach** | Authn middleware + role claims; resource scoping on hoop/swarm/MCP; never bolt onto MITM Path B first. |
| **Acceptance** | Login redirect; role-gated dashboard APIs; documented threat model. |
| **Effort** | **L** |
| **Anti-goals** | Homegrown password DB; pretending local `.env` tokens are SSO. |

### 5b. SIEM / hash-chained audit export

| | |
|--|--|
| **Why deferred** | Retention/compliance product undecided; agentlog is operational, not compliance-grade. |
| **Prerequisites** | Retention policy; sink format (CEF/JSON); signing key management. |
| **Approach** | Export adapter over agentlog/event stream; optional hash chain per export batch. |
| **Acceptance** | Export job + verify tool; no PII leakage in default fields. |
| **Effort** | **M–L** |
| **Anti-goals** | Fake “immutable” claims without key custody; blocking core loop on SIEM availability. |

### 5c. Temporal-class multi-day HITL

| | |
|--|--|
| **Why deferred** | Process-local `MachineCursor` ≠ durable workflow engine; multi-day resume is a platform choice. |
| **Prerequisites** | Durability requirements; storage (DB/object); decision Temporal vs lighter checkpoint store. |
| **Approach** | Externalize HITL wait state; workers resume by id; timeouts/escalation policies as data. |
| **Acceptance** | Approve/reject after process restart + wall-clock days; idempotent resume. |
| **Effort** | **L** |
| **Anti-goals** | Embedding Temporal into every hoop; breaking in-process HITL MVP that already works. |

### 5d. Leiden communities at repo scale

| | |
|--|--|
| **Why deferred** | Needs denser EXTRACTED graph + heavier dependency ingest; MVP communities exist. |
| **Prerequisites** | Stable EXTRACTED ingest quality; performance budget on large repos. |
| **Approach** | Optional community pass behind config; benchmark before default-on. |
| **Acceptance** | Community labels on large fixture repo within budget; quality notes vs MVP. |
| **Effort** | **L** |
| **Anti-goals** | Default-on Leiden that OOMs Windows laptops; rewriting graph core for one algorithm. |

### 5e. Live tree-sitter grammars on Windows

| | |
|--|--|
| **Why deferred** | Platform/CGO/grammar packaging pain; `SymbolIndexer` is the pragmatic floor. |
| **Prerequisites** | Windows-friendly grammar build story; CI matrix. |
| **Approach** | Optional indexer backend behind interface; keep SymbolIndexer default. |
| **Acceptance** | Opt-in tree-sitter path on Windows CI or documented skip; parity tests vs SymbolIndexer. |
| **Effort** | **M–L** |
| **Anti-goals** | Making tree-sitter mandatory for contextgraph; blocking Graphify MVP on grammars. |

### 5f. Chargeback UI / billing product

| | |
|--|--|
| **Why deferred** | Spend metrics exist; no billing product decision. |
| **Prerequisites** | Pricing model; tenant/project keys; invoice export needs. |
| **Approach** | Read-only chargeback views over existing budget/spend metrics first. |
| **Acceptance** | Filter by project/time; export CSV; no payment capture required for v1. |
| **Effort** | **M** |
| **Anti-goals** | Building a full billing system or payment gateway inside Glider. |

### 5g. ~~Phase 3 cross-hoop / Temporal feeds~~ — moot, 2026-07-25

Its prerequisite (`internal/loop`'s in-hoop feeds MVP, `feeds.go`/`RelFeeds`) was deleted whole in the v1 CLI-interop strip-down. A cross-workflow feeds concept could resurface for `swarm` waves, but that would be a fresh design against swarm's actual data model, not a continuation of this item — not carried forward as-is.

### 5h. Backend live hot-reload without restart

| | |
|--|--|
| **Why deferred** | ~~Router/aliases/threshold/log/GPU hot-swap already works; live backend/MITM/port reload risks in-flight sessions.~~ **MVP SHIPPED 2026-07-24** for backend/model clients; MITM/ports remain restart. |
| **Prerequisites** | Drain protocol for in-flight completions; config versioning; MITM cert/listener lifecycle design. |
| **Approach** | Quiesce -> swap backend registry -> warm health checks -> resume; keep ports stable when possible. |
| **Acceptance** | Swap Ollama↔vLLM (or model endpoint) without process exit; in-flight requests fail-soft or finish on old client. |
| **Effort** | **L** |
| **Anti-goals** | Killing MITM mid-handshake; pretending YAML edit auto-reloads listeners without drain. |

#### Progress (2026-07-24) — MVP SHIPPED

- `backend.Registry.ReplaceAll` + `backend.Reloader` build-then-swap; failure leaves previous clients.
- Hot-swap module `backends` is **hot**; wired from `cmd/glider` via `buildBackendSnapshot` + `config.Provider.Watch/Swap`.
- Warm ping soft-warns (`last_warnings`); hard fail on empty snapshot / unknown model backend / empty URL.
- Signals: `GET /api/hotswap/modules` (`last_ok`, `last_error`, `generation`); Config save headers `X-Glider-Backend-Reload` (+ Error/Warnings).
- In-flight: holders of the old `InferenceBackend` finish on that client; new requests use the swapped map.
- **Still restart:** MITM CA/hosts/listeners, server listen ports (anti-goals respected).

#### Acceptance checklist

- [x] Swap Ollama↔vLLM (or model endpoint URL) without process exit.
- [x] Clear API/dashboard signal for reload success/failure.
- [x] Thread-safe registry swap; orchestrator bindings (`*Registry` pointer + atomic cloud-fallback flag).
- [x] Tests: success swap + failure leaves previous config working (`reload_test.go`, `hotswap_reload_test.go`).
- [x] Documented (README, Config UI hint, `configs/glider.yaml` comment, this section).
- [ ] Full drain / quiesce before drop (MVP: old client retained by in-flight refs only).
- [ ] MITM / listen-port live reload (explicitly out of MVP).

---

## Explicit non-goals (still)

- Native Cursor IDE plugin.
- Full Cursor protocol reverse-engineering beyond Path B text RunSSE + measured ToolCall map.
- Treating deferred enterprise rows as overnight P0.

## Related

- Matrix: [remaining_gaps.md](remaining_gaps.md) § DEFERRED  
- SOLID: [solid_refactor.md](Depreceated/solid_refactor.md)  
- Tools/MCP: [tools_mcp.md](tools_mcp.md)  
- Loop: [loop_engineering.md](loop_engineering.md)
