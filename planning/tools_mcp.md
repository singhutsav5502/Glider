# Tools and MCP

Glider unifies builtins, plugins, and MCP under `internal/tools`. Hoop stages and swarm workers declare `tools:` refs; the registry dispatches and (when wired) feeds results into the model via an agentic tool loop / OpenAI-compatible `tools[]`.

## Builtins

`fs_read`, `fs_write`, `fs_list`, `fs_search`, `code_grep`, `http_fetch`, `web_search`, `web_fetch`, `git_*`, `artifact_write`, `context_query`, `datetime`, `calculator`, `shell_exec`.

| Tool | Role |
|------|------|
| `web_search` | Query → ranked `title` / `url` / `snippet` (agent-loop only) |
| `web_fetch` | URL → readable plain text, size-capped, host allowlist |
| `http_fetch` | Raw HTTP GET (`status=` + body); prefer `web_fetch` for reading pages |
| `artifact_write` | Write under active run `work/` or `out/` (`kind=work\|out`) |

### Workspace + ScopeRel + artifacts

Default sandbox: `~/.glider/workspace` (`orchestration.tools.workspace`). Each hoop/swarm run binds a layout:

```
~/.glider/workspace/
  runs/<hoop-or-turn-id>/
    work/   ← clones, scratch, intermediate files
    out/    ← final reports / packs
```

**ScopeRel** (used by `fs_*`, `code_grep`, `git_clone`): when a run is active, bare paths land under that run’s `work/`:

```text
audit-target          →  runs/clone-repo-security-audit/work/audit-target
runs/foo/work/x       →  unchanged (already under runs/)
.                     →  runs/<id>/work
```

```yaml
# Hoop stage tools — clone dest is bare; ScopeRel does the rest
tools:
  - { name: git_clone, kind: builtin }
  - { name: fs_list, kind: builtin }
  - { name: artifact_write, kind: builtin }
```

```text
# artifact_write input (text form)
out findings.md
# Severity: high
# ...
```

Dashboard **Workspace** tab and `GET /api/workspace?run=<id>` browse the same tree. Do **not** set `workspace: "."` for audits (that points tools at the Glider source tree).

`shell_exec` is **off by default**. Enable in config:

```yaml
orchestration:
  tools:
    # Agent fs/git/shell sandbox (clones go here). Default: ~/.glider/workspace
    # workspace: ~/.glider/workspace
    allow_shell: true
    shell_allowlist: ["git", "go", "rg"]
    # allow_hosts: ["github.com"]   # empty = any host for http_fetch/web_fetch
    web_search:
      provider: auto   # auto|duckduckgo|brave|tavily|serpapi|searxng
      max_results: 5
      # searxng_url: http://127.0.0.1:8080
```

### Web search setup

**Provider `auto`:** Brave (`BRAVE_SEARCH_API_KEY` / `BRAVE_API_KEY`) → Tavily (`TAVILY_API_KEY`) → SerpAPI (`SERPAPI_KEY`) → SearXNG (`SEARXNG_URL`) → DuckDuckGo HTML (no key). On provider failure, the next in the chain is tried. Forcing a keyed provider with an empty env returns a clear error (no invented results).

```bash
# .env.local (see .env.example)
BRAVE_SEARCH_API_KEY=BSA...
# or TAVILY_API_KEY= / SERPAPI_KEY= / SEARXNG_URL=http://127.0.0.1:8080
```

```yaml
# configs/glider.yaml
orchestration:
  tools:
    web_search:
      provider: auto
      max_results: 5
```

`web_fetch` is the readable-page companion to `http_fetch` (raw status+body); both honor `allow_hosts` when set.

Builtins cannot escape this workspace root (`safeJoin`).
## GitHub MCP (live)

**Glider running ≠ GitHub MCP connected.** The tools panel shows a *documented catalog* when the MCP **session** is down (no token / connect failed). After a live Connect you should see `source=live`.

### UI-driven connect (Cursor-like)

Dashboard **MCP** tab:

1. **Sign in with GitHub** — OAuth **device flow** (enter code at github.com/login/device). Requires a GitHub OAuth App or GitHub App with Device Flow enabled.
   Put the client id in a local env file (gitignored), then start Glider from the repo root:

   ```bash
   copy .env.example .env.local   # Windows
   # edit .env.local:
   GLIDER_GITHUB_OAUTH_CLIENT_ID=Iv1.xxx
   # optional: GLIDER_GITHUB_OAUTH_CLIENT_SECRET / GLIDER_GITHUB_OAUTH_SCOPES
   ```

   Glider loads `.env` then `.env.local` at startup (shell env still wins). See `.env.example`.
2. **Paste PAT** — stores token in `~/.glider/credentials/github_token` (mode 0600), sets process env, connects HTTP `github`.
3. **Forget token** — deletes credential file and disconnects sessions.

APIs: `POST /api/mcp/github/token`, `DELETE /api/mcp/github/token`, `POST /api/mcp/github/device/start`, `POST /api/mcp/github/device/poll`.

### Env PAT (still supported)

Never put a PAT in YAML. Use env only (first non-empty wins), or the credential file above:

```bash
export GITHUB_PERSONAL_ACCESS_TOKEN=ghp_...   # preferred
# or GITHUB_TOKEN / GH_TOKEN
```

**HTTP (hosted Copilot MCP)** — server id `github`:

```go
cfg := mcp.DefaultGitHubConfig() // URL https://api.githubcopilot.com/mcp/
mgr.Connect(ctx, cfg)
```

Glider’s Streamable HTTP client now:

- Persists `Mcp-Session-Id` from responses (also accepts lowercase `mcp-session-id`)
- Sends `Mcp-Session-Id` + `MCP-Protocol-Version` on every follow-up
- Sends `X-MCP-Toolsets` from `ServerConfig.Toolsets` (hosted Copilot filter)
- On 401/403 or session-shaped 400: clears session, re-resolves token from env/credential file, re-`initialize`s once, retries the call

**Docker stdio (official image)** — server id `github-stdio`:

```bash
docker pull ghcr.io/github/github-mcp-server
# Glider: mcp.DefaultGitHubStdioConfig() — connect from dashboard MCP tab
docker run -i --rm -e GITHUB_PERSONAL_ACCESS_TOKEN ghcr.io/github/github-mcp-server
```

Config validation rejects `auth.token` inline secrets; use `token_env`.

At process start Glider hydrates token from `~/.glider/credentials/github_token` if env empty, `Configure`s both presets, and soft-connects HTTP GitHub when a token is present.

### Hosted Copilot MCP — ops verification checklist (needs live PAT)

> **Status: NOT production-verified.** Code-side session/auth harden is tested against `httptest`. Do **not** mark this SHIPPED until a human runs the steps below with a real Copilot-capable PAT. Record quirks here (never secrets).

**Prereqs**

- Glider running (`go run ./cmd/glider -config configs/glider.local.yaml`); dashboard `http://127.0.0.1:8081`
- PAT in env **or** Dashboard MCP → Paste PAT (writes `~/.glider/credentials/github_token`, mode 0600)
- Account has GitHub Copilot entitlement that includes hosted MCP
- `.env.example` stays placeholder-only; never commit real PATs

**Commands / API signals**

```powershell
# Optional: put PAT in gitignored env (shell still wins over dotenv)
# $env:GITHUB_PERSONAL_ACCESS_TOKEN = "ghp_..."   # do not commit

# After Paste PAT or env set:
Invoke-RestMethod http://127.0.0.1:8081/api/mcp/github
# expect: token_configured=true, token_source=credentials_file|env:..., hint may say not connected yet

Invoke-RestMethod -Method POST http://127.0.0.1:8081/api/mcp/servers/github/connect
Invoke-RestMethod http://127.0.0.1:8081/api/mcp/servers/github
# expect: connected=true, health_ok=true, tool_count>0

Invoke-RestMethod http://127.0.0.1:8081/api/mcp/servers/github/tools
# expect: source=live (not catalog)

# Cheap tool twice — second call must not 400 on missing session
# (Dashboard MCP tools panel → get_me, or hoop/stage CallTool)
```

**Success signals**

| Step | Pass if |
|------|---------|
| Connect | `connected=true`, `health_ok=true`, `health_error` empty |
| Tools list | `source=live`, non-empty tools |
| `get_me` ×2 | both succeed; no `mcp http 400` session error on 2nd |
| Status after | `GET /api/mcp/github` → `http_connected=true`, hint mentions live session |
| Auth errors | 401/403 surface as `authentication failed` / `forbidden` in connect/status (not a hang) |

**Failure modes (expected surfaces)**

| Symptom | Likely cause | Operator action |
|---------|--------------|-----------------|
| `authentication failed (401)` | Bad/expired PAT, or no token | Paste PAT / device flow; Reconnect |
| `forbidden (403)` | Token lacks Copilot MCP / org policy | Use Copilot-capable account; or Docker `github-stdio` |
| `timeout talking to hosted MCP` | Network / VPN / edge | Retry; check URL `https://api.githubcopilot.com/mcp/` |
| Connect OK then tools `source=catalog` | Session dropped | Reconnect; confirm token still configured |
| 401 after rotating PAT in UI without Disconnect | Stale session auth | Reconnect (handshake refreshes credential file) |
| Toolsets subset empty / tiny | `toolsets:` filter too narrow | Clear toolsets or set `[repos, issues]` intentionally |

**Rollback:** Dashboard **Forget token** or delete `~/.glider/credentials/github_token` and revoke the PAT on GitHub.

**Still unverified without live PAT:** whether Copilot’s edge requires a newer `MCP-Protocol-Version` than Glider’s `2024-11-05`, or extra Copilot-only headers beyond `X-MCP-Toolsets`.

### Hosted Copilot MCP — manual verify (short)

Automated tests cover headers + session retry + initialize auth refresh against `httptest`. Against production still confirm with the checklist above.
## Dashboard MCP UI

Tab **MCP** (`http://127.0.0.1:8081`):

- Server list: id, transport, connected/health, tool count, token yes/no
- Connect / Disconnect / Reconnect / Refresh
- Tools catalog (live when connected; GitHub documented catalog fallback)
- GitHub status card: token configured? HTTP / stdio connected?

APIs (live `internal/mcp.Manager`):

| Method | Path |
|--------|------|
| GET | `/api/mcp/servers` |
| GET | `/api/mcp/servers/{id}` · `.../tools` |
| POST | `/api/mcp/servers/{id}/connect` · `disconnect` · `reconnect` · `refresh` |
| GET | `/api/mcp/github` |
| POST | `/api/mcp/github/token` |
| DELETE | `/api/mcp/github/token` |
| POST | `/api/mcp/github/device/start` |
| POST | `/api/mcp/github/device/poll` |

Hostable docs: [`docs/site/mcp.html`](../docs/site/mcp.html), [`docs/site/api.html#mcp`](../docs/site/api.html).

## Blind pre-pass vs agent tool loop

Hoop stages and swarm fan-out **do not** `InvokeAllParallel` every declared tool with the goal string.

| Path | What runs |
|------|-----------|
| Blind pre-pass | Only `FilterBlindSafe` tools (`datetime`, `fs_list`, `git_status`/`diff`/`log`, MCP `list_tools`) with input `"."` |
| Agent loop (`RunAgentLoop`) | Everything else: `git_clone`, `fs_write`, `artifact_write`, `code_grep`, `fs_search`, `web_search`, `web_fetch`, MCP tools — needs structured `tool_calls` **or** text JSON tool lines the loop can parse |
| Parallel actors (`parallel: N`) | Default `parallel_mode: fanout` — each worker runs the agent loop, then CritiqueMerge |
| Nested swarm (`parallel_mode: swarm`) | Same stage nests `swarm.Runner.Run` / `RunWaves` (requires swarm enabled; no silent fanout fallback) |

**Safety knobs:**

- Undeclared tools are rejected (`tool %q not allowed in this stage` — loop continues; do not treat rejection as plan/output).
- MCP `name: "*"` is always `ExpandRefs`'d before CallTool (never invokes literal `"*"`).
- MaxSteps: sequential stages ≥ 20, parallel workers ≥ 28 (`toolLoopMaxSteps*` in `internal/loop/cycle.go`; default agent loop ≥ 20). Critic stages get read-only tools only.

```yaml
# parallel fanout (default) vs nested swarm Runner
- id: audit_workers
  kind: actor
  parallel: 3
  parallel_mode: fanout   # or: swarm
  roles: [quality, security, secrets]
  tools:
    - { name: context_query, kind: builtin }
    - { name: code_grep, kind: builtin }
    # no git_clone — undeclared clone is rejected
```

```json
{"name":"fs_list","arguments":{"input":"audit-target"}}
```

Local models that emit the JSON object above in text (instead of native `tool_calls`) are still parsed into tool invocations.

## Path B tool codec (opt-in) — **SHIPPED** (extended common map)

> Verified 2026-07-24: `internal/cursorrpc/toolcall_map.go` + `runsse_codec.go`; flag `mitm.agent_rpc_tool_codec` / `GLIDER_MITM_AGENT_RPC_TOOL_CODEC=1`.  
> Schema pin: `planning/vendor_ref/agent_v1.proto` (Cursor `agent.v1.ToolCall` oneofs).  
> Live UI acceptance on a real Cursor install remains **DEFERRED** (see [intentional_backlog.md](intentional_backlog.md) §1).

With `mitm.agent_rpc_tool_codec: true` (or `GLIDER_MITM_AGENT_RPC_TOOL_CODEC=1`), child/tool-loop `RunSSE` can emit tool frames locally. OpenAI/Cursor/Glider names map to Cursor `agent.v1.ToolCall` oneofs; **unknown names → `TruncatedToolCall` (field 34)**.

| Layer | Status |
|-------|--------|
| Name → Cursor oneof map (FS/web + Todos/Lints/MCP/SemSearch/Task/Plan/Mode/Exa/…) | **SHIPPED** (`toolcall_map.go` + tests) |
| TruncatedToolCall fallback | **SHIPPED** |
| Live UI sign-off on pinned Cursor build | **DEFERRED** — prefer Path A (`cus-` + Override Base URL) for Agent+tools demos |

### Mapped vs Truncated inventory (schema pin: `vendor_ref/agent_v1.proto`)

| Wire field | Cursor oneof | Example names | Glider builtin |
|-----------:|--------------|---------------|----------------|
| 1 | `shell_tool_call` | `shell`, `Shell`, `shell_exec`, `bash` | `shell_exec` |
| 3 | `delete_tool_call` | `Delete`, `delete_file` | — |
| 4 | `glob_tool_call` | `Glob`, `fs_search`, `file_search` | `fs_search` |
| 5 | `grep_tool_call` | `Grep`, `code_grep` | `code_grep` |
| 8 | `read_tool_call` | `Read`, `read_file`, `fs_read` | `fs_read` |
| 9 | `update_todos_tool_call` | `TodoWrite`, `update_todos` | — |
| 10 | `read_todos_tool_call` | `TodoRead`, `read_todos` | — |
| 12 | `edit_tool_call` | `Write`, `StrReplace`, `fs_write` | `fs_write` |
| 13 | `ls_tool_call` | `Ls`, `list_dir`, `fs_list` | `fs_list` |
| 14 | `read_lints_tool_call` | `ReadLints` | — |
| 15 | `mcp_tool_call` | `CallMcpTool`, `mcp` | — |
| 16 | `sem_search_tool_call` | `SemSearch`, `codebase_search` | `fs_search` |
| 17 | `create_plan_tool_call` | `CreatePlan` | — |
| 18 | `web_search_tool_call` | `WebSearch`, `web_search` | `web_search` |
| 19 | `task_tool_call` | `Task` | — |
| 20 | `list_mcp_resources_tool_call` | `ListMcpResources` | — |
| 21 | `read_mcp_resource_tool_call` | `ReadMcpResource` | — |
| 22 | `apply_agent_diff_tool_call` | `ApplyAgentDiff` | — |
| 23 | `ask_question_tool_call` | `AskQuestion` | — |
| 24 | `fetch_tool_call` | `Fetch`, `http_fetch` | `http_fetch` |
| 25 | `switch_mode_tool_call` | `SwitchMode` | — |
| 26 | `exa_search_tool_call` | `ExaSearch` | `web_search` |
| 27 | `exa_fetch_tool_call` | `ExaFetch` | `web_fetch` |
| 28 | `generate_image_tool_call` | `GenerateImage` | — |
| 31 | `write_shell_stdin_tool_call` | `WriteShellStdin` | — |
| 32 | `reflect_tool_call` | `Reflect` | — |
| 37 | `web_fetch_tool_call` | `WebFetch`, `web_fetch` | `web_fetch` |
| 34 | `truncated_tool_call` | **any unmapped name** | — |

**Still Truncated-only (no map):** `record_screen` (29), `computer_use` (30), `setup_vm_environment` (33), `start_grind_*` (35–36), `report_bugbot_results` (38), and any future oneofs not in the table.

Default remains **off** (child RunSSE → origin). Implementation: `internal/cursorrpc/toolcall_map.go` + `runsse_codec.go`.

## Shared context engine (hoop)

> **2026-07-25:** `internal/loop` (the stage runner that executed `kind: context`/`memory`/`feeds` YAML stages below) was removed in the v1 CLI-interop strip-down — the YAML stage examples in this section no longer have a runner and won't execute. The underlying mechanism they describe (`contextgraph`'s `RecordHoopContext`/`LookupHoopContext`/`HoopContextDigest`/`context_query`) is still real, live code, just currently only reachable from `internal/swarm`, not from hoop stages. Kept for reference until this section is rewritten swarm-only.

| Piece | Behavior |
|-------|----------|
| `kind: context` (and `kind: memory`) | Upserts goal / plan / actor excerpts + artifact hints into `contextgraph` for the hoop turn (`RecordHoopContext` keys: `goal`, `plan`, `clone_path`, `file-tree`) |
| Prompt seed | Later LLM stages get a short `[context_digest]` / `CONTEXT:` block (PathSummary + Query) |
| **`graph_edges kind=feeds`** | Data-only edge: when producer stage finishes, its summary is upserted (`RecordHoopContext` key `feed_<stageId>` + `RelFeeds` entity edge). Consumer `stagePrompt` gets a `FEEDS:` block with producer summary + artifact paths. Alternate: `kind: flow` + `label: feeds`. Not control-flow (skipped by SM `WalkOrder` / `FromLoopStages`). See `samples/hoops/feeds-edge-mvp.yaml`. |
| `context_query` | Always available on actor/planner (and parallel/swarm workers via filter defaults). Critic defaults to **no tools**. |
| Critic | Default: `completeOnce` (no tool loop). Must emit `SCORE:` / `REASON:`. Missing SCORE → eval_score=0 + err `critic missing SCORE`. |
| Plan poison filter | Planner text with `not allowed in this stage` / git_clone errors is not seeded into CONTEXT; reseed clears prior poison. |
| Clone verified | When `clone_path` exists, digest includes `Clone verified: YES at <path>`. |

```yaml
stages:
  - id: context_seed
    kind: context
    name: Seed shared contextgraph
  - id: workers
    kind: actor
    parallel: 2
    parallel_mode: swarm   # see samples/hoops/parallel-swarm-mode.yaml
    prompt: |
      Call context_query key=clone_path (or goal / plan / file-tree).
      Do not re-clone. Use [context_digest] if present.
```

### Local request timeout

`thresholds.request_timeout` (default `10m`) sets the HTTP client timeout for Ollama/vLLM `Complete` calls (headers + body). Parallel 14b + 20–28 tool steps need minutes — the old `120s` default caused `Client.Timeout exceeded while awaiting headers`.

```yaml
thresholds:
  request_timeout: 10m   # or 15m for heavy local tool loops
```

### `context_query` filters

```
<turn_id> [key=clone_path|goal|plan|file-tree] [kind=note|file|dir]
  [prov=RUNTIME|EXTRACTED|INFERRED] [path=from->to] [neigh=id] keyword…
```

- Bare `clone_path` / `goal` / `plan` / `file-tree` (or `goal OR plan`) resolve as key/keyword filters.
- Keywords may use `OR` / `|` (any-term match).
- Helpers: `Store.RecordHoopContext(turnID, key, value)`, `LookupHoopContext`, `HoopContextDigest`.

**Clone audit / parallel:** after verify + `kind: context`, workers share `runs/<hoop-id>/work/audit-target` (ScopeRel). Call `context_query key=clone_path` — do **not** re-clone. See `samples/hoops/clone-repo-security-audit.yaml` and `samples/hoops/parallel-swarm-mode.yaml`.

## Skills, worktrees, CapHooks

```yaml
# hoop YAML
skill: security-audit              # → skills/security-audit/SKILL.md
# skill: skills/audit/SKILL.md     # explicit path
# skill: "Always cite file paths"  # plain-string fallback

# configs/glider.yaml
orchestration:
  loops:
    skills_dir: ~/.glider/skills   # optional; also searches tools workspace + cwd
    worktrees: false               # true → parallel>1 workers under runs/<id>/work/wN/
```

| Piece | Behavior |
|-------|----------|
| **Skills** | `ResolveSkillContent` loads SKILL.md by id/path; injects `[skill]` into the effective prompt. Unresolved refs keep the string as `[skill: …]`. |
| **Worktrees** | Opt-in `orchestration.loops.worktrees`. Parallel workers get isolated `wN/` dirs (`git worktree add --detach` when sandbox is a git repo; else plain subdirs + `WORKER_ROOT` hint). |
| **CapHooks** | Plugins with `CapHooks` get `DispatchStageEnter` / `DispatchStageExit` around each stage (`Manager.Plugins`). No-op when nil / no providers. |

## Agent-log `after_seq` + dashboard chips

```bash
GET /api/agent-logs?scope=hoop&id=<id>&after_seq=<n>&limit=50   # Seq > n only
GET /api/context/episodes                                       # Overview episode chips
```

Hoop `governance:` soft/hard spend drives **budget chips** on cards / live rail. Entries always include monotonic `seq`; omit `after_seq` for a newest-limit snapshot.

## HITL gate + on_fail_n

```yaml
human_gate: true          # hoop-level: pause for operator before continue
eval:
  goal: Critic score meets bar
  min_score: 0.7
  on_fail_n: 3            # after N consecutive critic fails → stop (no infinite HITL reopen)
```

Resume: Dashboard approve/deny, or `POST /api/loops/{id}/approve` (and `/reject`), then `/resume` if needed. Gate payload includes `ask` (critic/actor/plan excerpts). After `on_fail_n` consecutive fails, the cycle stops instead of re-opening HITL forever.

## Workspace dashboard API

| Query | Meaning |
|-------|---------|
| `GET /api/workspace` | Sandbox root listing |
| `GET /api/workspace?run=<id>` | `work` + `out` trees for that run |
| `GET /api/workspace?path=runs/<id>/out` | List a relative path |
| `GET /api/workspace?file=runs/<id>/out/report.md` | Capped text preview |
| `GET /api/workspace?diff=1&a=…&b=…` | Unified diff of two paths |

UI: Dashboard → **Workspace** tab.
## Node binding (graph editor)

Stage edit dialog:

1. Check MCP servers (+ optional specific tools)
2. Advanced Tools JSON for builtins
3. Persisted on `StageSpec.tools` (wildcards expand to `list_tools` / server bind on create)

Hoops / Graph help panels link to the MCP tab.

## Seed all samples

```powershell
powershell -File scripts\seed-samples.ps1
```

Loads every top-level YAML under `samples/swarms/` → `~/.glider/hoops`. See [`docs/site/samples.html#seed`](../docs/site/samples.html).

## Governance MVP

Per-hoop `governance:` soft/hard token, latency, cost, MaxRPM, tool denylist. Soft → prefer local / skip optional tools; hard → stop `budget_exceeded`.

Deferred: SSO, RBAC, SIEM hash-chain, Temporal multi-day HITL.

## Context window vs Glider truncate vs max_tokens

Mid-sentence cutoffs in hoop logs (artifact_write JSON, parallel merge, critic/actor summaries) usually come from one of three layers:

| Layer | What it caps | Configure |
|-------|----------------|-----------|
| **Model context (`num_ctx`)** | How much prompt+history the local model can see | Ollama Modelfile / `ollama run` / model `max_context` in `configs/glider*.yaml` (e.g. qwen2.5-coder:14b → 32768). If context is full, generation stops early. |
| **Completion length (`max_tokens` / `num_predict`)** | How long one assistant turn can be | `thresholds.default_max_tokens` (default **8192** for hoop Completes). Raise for long audit reports. Ollama OpenAI-compat maps `max_tokens` → `num_predict`. |
| **Glider truncate helpers** | What is stored / re-injected (not the model stop) | Stage text ~48k, outcome summaries ~32k, tool-result prompt inject `ToolResultPromptCap` (24k), CritiqueMerge per-worker ~16k. Dashboard **Full output** / **Copy logs** read `attrs.text` / outcome summaries — not the short log row. |

If TOOL_CALLS / `artifact_write` JSON dies mid-arguments, Glider retries once or twice asking the model to re-emit complete JSON (does not treat the truncated blob as the final stage answer). Still prefer raising `default_max_tokens` and ensuring `num_ctx` is high enough for tool-heavy audit prompts.
