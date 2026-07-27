# Agent CLI interop: Claude Code / cursor-agent / agy (Antigravity)

> **Status (2026-07-25):** Research spike. Live-traced all three CLIs on this machine (not doc-scraped — see Methodology). Findings inform a proposed common envelope for Glider to translate messages / tool calls / file diffs across engines. No code written yet; this is the design doc to build against.
>
> Related: [MITM_NETWORK.md](../docs/MITM_NETWORK.md) (Cursor IDE protobuf path — the prior art this extends) · `internal/cursorrpc/toolcall_map.go` (existing Cursor wire↔builtin map) · `internal/tools/registry.go` (Glider's canonical builtin schema) · [cursor_agent_research.md](./cursor_agent_research.md) · **[native_glider_orchestration.md](./native_glider_orchestration.md)** — the product vision this feeds: naming this design "NGL" and using it to let one CLI's session transparently delegate to headless sessions of the others (now implemented — see [adapter_boundary.md](./adapter_boundary.md) and [permission_relay_design.md](./permission_relay_design.md))

---

## Why this doc

Glider already reverse-engineers **one** agent wire protocol deeply: Cursor IDE's `agent.v1` protobuf over Connect RPC (`internal/mitm/`, `internal/cursorrpc/`). The ask here is broader: Claude Code, Cursor (both IDE *and* its separate `cursor-agent` terminal CLI), and Google's Antigravity (`agy`) terminal CLI all speak *different* wire protocols but solve the *same* problem — turn a user message into tool calls (read/edit/grep/shell/search) and file diffs, streamed back. A common internal envelope lets Glider's orchestrator/harness stay protocol-agnostic and lets any of these three (or Path A gateway clients) be the "front" while any backend fulfills.

## Methodology

Live traffic tracing only, no doc-scraping for protocol shape (public docs were fetched only to identify install/config, never for wire schema). All three CLIs are installed and authenticated on this dev machine:

| CLI | Binary | Version seen |
|-----|--------|---------------|
| Claude Code | `claude` (`C:\Users\Utsav\.local\bin\claude.exe`) | 2.1.220 |
| Cursor terminal agent | `cursor-agent` (`%LOCALAPPDATA%\cursor-agent\cursor-agent.cmd`) | 2026.07.23-e383d2b |
| Antigravity CLI | `agy` (`%LOCALAPPDATA%\agy\bin\agy.exe`) | (Antigravity CLI, Gemini 3.x models) |

For each, ran an equivalent toy task in a scratch git workspace — *"read `math_utils.py`, add a function, grep to confirm"* — and captured the CLI's own structured trace instead of doing a full CA-trust-store MITM (avoided touching the Windows cert store; each CLI exposes enough on its own):

- **claude**: `--debug api --debug-file <f> --output-format stream-json --dangerously-skip-permissions` → SDK-shaped JSONL of every turn.
- **cursor-agent**: `-p --output-format stream-json --force` → its own JSONL event stream (distinct from, but structurally close to, the IDE's protobuf).
- **agy**: `-p --dangerously-skip-permissions --log-file <f>` → Go-side structured logs (`daily-cloudcode-pa.googleapis.com` calls) **plus** local per-conversation artifacts it writes under `~/.gemini/antigravity-cli/` (a SQLite step ledger + per-message JSON), which turned out to be the richest source. Also extracted ASCII strings from `agy.exe` to recover its full protobuf step-type catalog (`exa.cortex_pb.CortexStep*`) — same technique already used in this repo for `planning/vendor_ref/agent_v1.proto`.

**Follow-up pass** (closing the gaps from the first pass):

- **agy diff payload**: re-ran with `--new-project` to bind the CLI to the scratch workspace (first pass silently fell back to a scratch dir — see workspace-binding finding below — which produced a real tool error and no clean edit to inspect). Located the fresh conversation's `steps` table, dumped each `step_payload` BLOB to a file, and wrote a ~90-line schema-less protobuf field walker (`pbwalk.py`: read tag/wiretype, recurse into length-delimited fields, print printable strings, fall back to hex for opaque bytes — the same technique `internal/cursorrpc`'s `readVarint`/`ToolCallWireVariant` already uses for Cursor, just generalized to walk the whole tree instead of one known field) to decode the actual `replace_file_content` and `view_file` steps without needing the `.proto`. `protoc --decode_raw` wasn't installed, so this was hand-rolled. No embedded-descriptor extraction from the 166 MB `agy.exe` was needed once the SQLite step ledger + raw walk gave clean field-level results directly from real traffic.
- **cursor-agent raw bytes**: stood up a throwaway Go reverse proxy (`captureproxy.exe`, plain-HTTP-in / real-TLS-out, no client-side CA trust needed) and ran `cursor-agent --endpoint http://127.0.0.1:8899 ...` to capture actual wire bytes. Confirmed the CLI's `--endpoint` override only redirects the `aiserver.v1.*` Dashboard/Config/Models plane; found no explicit `AgentService`/`RunSSE`/`StreamChat*` call in the capture — see Gaps for why that's inconclusive rather than a negative result.

---

## Per-CLI protocol profile

### 1. Claude Code (`claude`)

| Dimension | Finding |
|---|---|
| Transport | HTTPS REST+SSE to `api.anthropic.com/v1/messages` (public Messages API — not re-derived, already known ground truth) |
| CLI-observable envelope | `--output-format stream-json`: one JSON object per line — `system/init`, `assistant` (content blocks), `user` (tool_result), `result` (final, with `usage`/`total_cost_usd`) |
| Tool call shape | Anthropic `tool_use` content block: `{"type":"tool_use","id":"toolu_…","name":"Edit","input":{…}}`, paired with a `tool_result` block keyed by the same id |
| Tool names (this trace) | `Read`, `Edit`, `Grep` (built-ins listed at init: `Bash`, `Write`, `Glob`, `NotebookEdit`, `WebFetch`, `WebSearch`, `Task`, …) |
| **File diff representation** | `Edit` tool_result carries `structuredPatch`: array of unified-diff hunks — `{oldStart,oldLines,newStart,newLines,lines:[" ctx","-old","+new"]}` — plus `oldString`/`newString`/`originalFile`. This *is* a standard unified-diff hunk, JSON-shaped. |
| **Whole-file write representation** | `Write` — input `{file_path, content}` — result **reuses `Edit`'s exact schema**: `{type:"create", filePath, content, structuredPatch:[], originalFile:null, userModified:false}`. Empty-array `structuredPatch` + `null` `originalFile` is the CLI's own convention for "this was a create/overwrite, not a diff" — confirms whole-file-write doesn't need a new result *shape*, just this empty/null convention layered on the same one. |
| `Bash` | input `{command, description}`; result `{stdout, stderr, interrupted, isImage, noOutputExpected}`. Wrapped in an async trackable unit even for one call: `system/task_started` → `system/task_notification`, carrying `task_id`/`task_type:"local_bash"`. |
| `Glob` | input `{pattern}` (path optional, defaults to cwd); result `{filenames:[…], durationMs, numFiles, truncated, totalMatches, countIsComplete}`. |
| `WebFetch` | input `{url, prompt}` — `prompt` is a model-extraction instruction, this is fetch-*and-summarize*, not raw HTML retrieval; result `{bytes, code, codeText, result:<model-authored text>, durationMs, url}`. |
| `WebSearch` **(hosted, not local)** | input `{query}`; result executes **server-side on Anthropic's infra**, under a distinct `srvtoolu_…` id (vs. the client-side `toolu_…` used by every other tool here) — `{query, results:[{tool_use_id, content:[{title,url},…]}, <model-synthesized summary>], durationSeconds, searchCount}`. Real categorical split worth carrying into NGL: **hosted tools execute on the vendor's servers and cannot be "delegated" to a headless session the way a local tool can** — there is no process to spin up, the call already isn't local. |
| `NotebookEdit` | input `{notebook_path, new_source, edit_mode:"insert", cell_type:"code"}`; result `{new_source, cell_type, language, edit_mode, cell_id, error, notebook_path, original_file:<full pre-image>, updated_file:<full post-image>}` — full before/after text, same family as `Write`, just JSON-structured content. `Read` is polymorphic: a `.ipynb` path returns `{type:"notebook", file:{filePath, cells:[]}}` instead of the plain-text shape. |
| `Task` **— wire name is actually `Agent`, not `Task`** | input `{description, prompt, subagent_type, run_in_background}`. This is Claude's own existing subagent-delegation primitive and maps almost directly onto `native_glider_orchestration.md`'s `Delegate` struct — `subagent_type` is structurally the same slot as `TargetVendor`, just scoped today to Claude's own agent profiles. Gets a persistent, addressable `agentId`, resumable later. Full async lifecycle: `task_started` → `task_progress` (periodic, carries `last_tool_name` + running token/tool stats) → `task_updated{status:completed}` → `task_notification` (summary + `output_file` pointer to the full sub-transcript). Correlated via `parent_tool_use_id` on every nested event, **sharing the parent's `session_id`** (not a separate session). Final result carries a text summary *and* a structured stats block: `{totalTokens, totalDurationMs, totalToolUseCount, resolvedModel, toolStats:{readCount, searchCount, bashCount, editFileCount, linesAdded, linesRemoved, otherToolCount}}` — see `native_glider_orchestration.md` §3/§6 for why this is treated as the concrete precedent for cross-vendor delegation, not just an analogy. |
| Tool catalog is partly lazy | A `ToolSearch` mechanism (`{"query":"select:WebFetch,WebSearch"}` → tool schema loaded on demand) means the "full tool list" isn't always sent/known eagerly — worth remembering when a vendor pack's tool list looks incomplete: it may be a real partial/lazy catalog, not a capture gap (this exact mechanism is, reflexively, how this research session's own tool list works). |
| Correlation | `tool_use.id` ↔ `tool_result.tool_use_id`; every turn has `session_id` + Anthropic `request_id` |
| Streaming granularity | Anthropic SSE deltas → CLI coalesces into whole content blocks for `stream-json`; raw SSE (`content_block_delta`) available via `--include-partial-messages` |
| Auth | OAuth/keychain or `ANTHROPIC_API_KEY`; per-request billing/attribution headers (`x-anthropic-billing-header`) |
| Workspace binding | None — operates on CLI's `cwd` directly, no project registration step |

### 2. Cursor terminal agent (`cursor-agent`)

| Dimension | Finding |
|---|---|
| Transport | HTTPS to `https://api2.cursor.sh` by default, **overridable** via `--endpoint` / `CURSOR_API_ENDPOINT` — but only partially (see below). Raw capture (proxy, see Methodology follow-up) shows this is **not** a separate simpler REST surface: calls are `connect-es`-client Connect-RPC to `aiserver.v1.{DashboardService,ServerConfigService,AiService,AnalyticsService}/…`, `Content-Type: application/proto` (gzip-encoded), with `X-Cursor-Client-Type: cli` / `X-Cursor-Client-Version: cli-2026.07.23-e383d2b` as the only thing distinguishing it from the IDE. **The terminal CLI and the IDE are the same Cursor Connect-RPC protocol family (`aiserver.v1` / `agent.v1`), not two protocols** — `internal/cursorrpc` is reusable, not IDE-only. One call (`AnalyticsService/TrackEvents`) used `Content-Type: application/json` instead of `application/proto` — Connect negotiates encoding per call, which matters for any future Glider-side origin emulation (JSON is far easier to fake than protobuf). |
| **`--endpoint` scope — definitively resolved** | A second, mutex-fixed capture pass (27 requests, zero interleaving) through the endpoint override confirmed: **only Dashboard/Config/Models/Analytics traffic honors `--endpoint`.** The actual agent-completion call never appeared on the overridden endpoint at all, despite the turn genuinely executing real edits. Conclusion, not a shrug: the completion plane goes to a **fixed host that `--endpoint` does not touch** — almost certainly the same `api2`–`api5.cursor.sh` cluster `docs/MITM_NETWORK.md` already documents for the IDE (consistent with "one shared protocol family" above). Getting its raw bytes needs real TLS MITM (CA trust) — correctly out of scope here per this session's decision not to touch the OS trust store without explicit sign-off; `stream-json` is the practical ceiling for that plane without it. |
| CLI-observable envelope | `--output-format stream-json`: `system/init`, `user`, `thinking` (delta/completed), `assistant`, `tool_call` (`started`/`completed`), `result` |
| Tool call shape | `{"tool_call":{"readToolCall":{"args":{...}}}}` — **the field names (`readToolCall`, `editToolCall`, `grepToolCall`) are the exact same names as the protobuf oneof variants Glider already mapped in `internal/cursorrpc/toolcall_map.go`** (`read_tool_call`, `edit_tool_call`, `grep_tool_call`, wire fields 8/12/5). The terminal CLI and the IDE clearly share one `agent.v1.ToolCall` schema; the CLI just JSON-serializes it instead of protobuf-encoding it. |
| **File diff representation** | `editToolCall` result: `diffString` (standard unified diff, `--- a/… +++ b/… @@ -a,b +c,d @@`), plus **both** `beforeFullFileContent` and `afterFullFileContent` — richer than Claude's (full-file snapshots, not just the hunk). `linesAdded`/`linesRemoved` counters included. |
| `globToolCall` | args `{globPattern}` (path/`target_directory` optional — omitting it searches the whole workspace). |
| `deleteToolCall` | args `{path, toolCallId}`; result.success `{path, deletedFile, fileSize:"<string, not a number>", prevContent}` — **the deleted file's content is preserved in the result**, a real undo-friendly convention worth carrying into NGL's `ToolResult` for any destructive op. |
| `webFetchToolCall` | args `{url, toolCallId}`; result.success `{url, markdown}` — pre-converted to markdown, unlike Claude's model-summarized `WebFetch`. |
| `webSearchToolCall` | args `{searchTerm, toolCallId}`; result.success `{references:[{title,url,chunk}]}`. **Large pages get offloaded to a local file** (`~/.cursor/projects/<hash>/agent-tools/<uuid>.txt`) with a pointer string instead of being inlined — a large-result-by-reference pattern worth adopting generally in NGL's `Part`/`ToolResult` rather than forcing big blobs inline. |
| `updateTodosToolCall` | args `{todos:[{id, content, status:"TODO_STATUS_PENDING"\|"TODO_STATUS_IN_PROGRESS"\|…, createdAt, updatedAt, dependencies:[]}], merge:bool}`; result mirrors it + `{totalCount, wasMerge}`. The `dependencies` array is a real DAG-between-todos capability not previously documented for any of the three CLIs. |
| Tools tried and not observed (model-choice, not confirmed-absent) | Asked for a dedicated directory listing → model ran `shellToolCall{command:"ls"}` instead of a dedicated `lsToolCall`; asked for semantic search → fell back to `grepToolCall`/`readToolCall` instead of `semSearchToolCall`. Both read as the model's own tool-selection behavior on this task, not evidence the tools don't exist — `internal/cursorrpc/toolcall_map.go` already has wire fields reserved for both. |
| Tools not attempted (budget-limited, honestly unresolved) | `read_lints`, `mcp`, `create_plan`, `task`, `switch_mode`, `ask_question`, `apply_agent_diff`, `exa_search`/`exa_fetch`, `generate_image`, `write_shell_stdin`, `reflect` — several of these read as IDE-chrome/interactive-UI-specific (plan panel, mode switch) or need configuration not present on this host (MCP servers, Exa) and may simply not be reachable from headless `-p` at all, but that's an inference, not a tested result. |
| Correlation | Composite id: `"call-<uuid>-<n>\nfc_<uuid>_<n>"` — a Cursor wrapper id concatenated (embedded newline) with an OpenAI-style `fc_…` function-call id, implying an OpenAI-shaped model sits behind at least one routing tier |
| Grep result shape | Nested `workspaceResults[path].content.matches[]` with `lineNumber`/`content`/`isContextLine` — richer than a flat grep line list |
| Auth | `login`/`logout` subcommands, `CURSOR_API_KEY` or stored session; `apiKeySource` reported in init event |
| Workspace binding | `--workspace` / `--add-dir`, defaults to cwd like Claude — no separate "project" registration |

### 3. Antigravity CLI (`agy`)

| Dimension | Finding |
|---|---|
| Transport | Spawns a **local Go "language server" child process** (gRPC on a random localhost port) that itself calls out to `https://daily-cloudcode-pa.googleapis.com/v1internal:{loadCodeAssist,fetchAvailableModels,streamGenerateContent}` — Google's internal Cloud Code Assist backend (`alt=sse`), i.e. Gemini's native `generateContent`/`functionCall`/`functionResponse` envelope, **not** the public `generativelanguage.googleapis.com` surface |
| Lineage | Confirmed via embedded strings: protobuf namespace is **`exa.*`** (`exa.cortex_pb.CortexStep*`, `exa.language_server_pb.LanguageServerService`) — the same "exa" (Exafunction) namespace as Codeium/Windsurf. Antigravity's agent core is the Windsurf **Cascade** engine rebadged, not a clean-room build. |
| CLI-observable envelope | No `--output-format` flag; `-p` just prints final markdown text. Real structure lives in **local artifacts** it writes as a side effect: `~/.gemini/antigravity-cli/conversations/<id>.db` (SQLite: `steps` table, one row per step — `step_type` int enum, `status`, `metadata`/`step_payload` protobuf BLOBs) and `~/.gemini/antigravity-cli/brain/<id>/.system_generated/messages/*.json` (loose JSON for system-generated notices) |
| Tool call shape (captured live) | `sourceMetadata.tool.toolCall = {"id":"tPSyDgZm","name":"run_command","argumentsJson":"{\"CommandLine\":…,\"Cwd\":…,\"toolAction\":…,\"toolSummary\":…}","thinkingSignature":"…","originalName":"run_command"}` — flat JSON args (double-encoded string, not nested object), plus a Claude-thinking-signature-style opaque `thinkingSignature` blob for reasoning continuity, plus UI-facing `toolAction`/`toolSummary` strings baked into the args |
| Wire-declared catalog (from `exa.cortex_pb.CortexStep*` oneof, extracted from binary) — **superset, not the live model toolset, see next row** | `run_command`, `grep_search`, `find`, `view_file`, `view_file_outline`, `view_code_item`, `view_content_chunk`, `list_directory`, `delete_directory`, `move`, `write_to_file`, `propose_code`, `file_change`, `create_file`, `delete_file`, `search_web`, `read_url_content`, `mcp_tool`, `list_resources`, `read_resource`, `lint_diff`, `git_commit`, `open_browser_url`, `execute_browser_javascript`, `click_browser_pixel`, `capture_browser_screenshot`, `capture_browser_console_logs`, `list_browser_pages`, `clipboard`, `checkpoint`, `task_boundary`, `notify_user`, `suggested_responses`, `command_status`, `error_message`, `workspace_api`, `trajectory_search`, `search_knowledge_base` |
| **Correction from a follow-up pass: the catalog above is the wire format's full oneof, not the CLI's actual live toolset** | Proven directly, twice: (1) explicitly instructed "delete this file using your dedicated file-delete tool, not a shell command," the model replied verbatim *"I do not have a dedicated file-delete tool in my available capabilities"*; (2) asked to find/move/delete files, the model spontaneously ran `run_command` (PowerShell `Get-ChildItem`/`Move-Item`/`Remove-Item`) rather than any dedicated `find`/`move`/`delete_file` tool, unprompted. **`run_command` is agy's universal fallback whenever a dedicated fs tool it doesn't actually have would otherwise be needed.** A vendor pack for `agy` should mark most of the ~40 wire-declared names `confirmed: false` rather than treat the binary-extracted catalog as an equally-live tool surface — the oneof is very likely a superset shared with the Antigravity IDE / historical Cascade surface, of which the `agy` CLI product only exposes a subset to the model. |
| **Edit tool args — two confirmed shapes, decoded raw with no `.proto` (see Methodology follow-up)** | `replace_file_content` — line-range edit — carries `{"TargetFile":…, "StartLine":10, "EndLine":12, "TargetContent":"<expected old lines, for verification>", "ReplacementContent":"<new lines>", "Instruction":"…", "AllowMultiple":false}`. **Structurally closer to an LSP `TextEdit{range, newText}` plus an optimistic-concurrency check than to a diff.** Separately, `write_to_file` — confirmed as the tool for *both* creating new files and overwriting existing ones (no distinct `create_file` was ever actually invoked) — carries `{"CodeContent":"<full new file content>", "Description":…, "Overwrite":true, "TargetFile":…, "toolAction":…, "toolSummary":…}`, a **whole-file-write** shape. These are two genuinely different edit models from one vendor, confirmed live — see the updated `EditViews` design in `native_glider_orchestration.md`, which now has a `WholeFile` view specifically because of this finding. `view_file` (read) args are `{"AbsolutePath":…}`; the wire-catalog's separate `view_file_outline` was never actually invoked even when a prompt specifically asked for "the outline" — same read tool, no distinct outline mode observed. The wire-catalog's `list_directory` also turned out not to be the live name: the tool actually invoked is `list_dir`, args `{"DirectoryPath":…, "toolAction":…, "toolSummary":…}`. `propose_code` was tried twice more (a large/risky rewrite without `--dangerously-skip-permissions`, and explicit `--mode plan`) and still never fired: the risky write executed via `write_to_file` and was denied at the permission layer (`status=7`) rather than routing through a distinct propose step, and plan mode stayed pure-text/read-only throughout. Given the fallback pattern above, `propose_code`/`create_file`/`delete_file`/`move`/`find`/`lint_diff`/`file_change` are now believed **not present in this CLI build's actual model-facing tool declaration**, not merely uncaptured — deprioritized rather than still-open. |
| Sidecar wire probe | The local language-server child's plain-HTTP port (random per run, confirmed reachable externally) **does speak Connect-JSON** — `POST /exa.language_server_pb.LanguageServerService/GetAllCascadeTrajectories` with `Content-Type: application/json` returns a valid `200 {}` rather than a transport error, confirming the JSON transport exists — but an empty-body request returns an empty object; a populated response needs either an auth token (`ANTIGRAVITY_CSRF_TOKEN`/session token, not available to an external caller) or correctly-shaped request fields (project/conversation id) not reverse-engineered in the time available. Gated, not a dead end — the transport is real and JSON, which is the harder half of the problem already answered. |
| Generic step envelope (reverse-engineered field map, applies to every `CortexStep*`) | Outer: `1`=step_type (dup of DB column), `4`=status. Nested tool message: `4.1`=short opaque call id, `4.2`=name, `4.3`=`argumentsJson` (**stringified JSON, not a nested message** — same double-encoding as the live JSON capture), `4.7.2.1`=opaque signed reasoning-continuity bytes (`thinkingSignature`), `4.9`=`originalName` (dup of `4.2`). `5`=repeated string listing the JSON key names used in `argumentsJson` (schema self-description / redaction hint?). `6`/`7`/`8`/`22`/`32`=`{unix_sec, nanos}` timestamp pairs at different lifecycle points. `12`=step UUID. `26`=repeated `{index, timestamp}` status-transition log. `30`/`31`=`toolAction`/`toolSummary` duplicated at the top level for UI rendering. All consistent across `view_file` and `replace_file_content` samples — this looks like a fixed step wrapper shared by all ~40 `CortexStep*` variants, with only field `4.3`'s JSON payload varying per tool. |
| Execution model | **Async step/task**, not request/response — steps run as cancelable background "tasks" (`task-N`) with their own log files under `.system_generated/tasks/`; a step can be `context canceled by manage_task` mid-flight (observed live) |
| Auth | Google OAuth via OS keyring, resolves to a **project ID** (`default-cli-project` when none configured) |
| Workspace binding | **Explicit project/workspace registration required** — unlike Claude/cursor-agent's implicit cwd binding. Without an active project, print-mode silently redirected file ops into `~/.gemini/antigravity-cli/scratch/` instead of the invocation cwd (observed live: caused a real `invalid_args` tool error when the model tried to re-read a path that didn't exist there). **This is the sharpest interop gap** — any adapter must resolve/create a project binding before trusting cwd-relative tool args. |

---

## Cross-CLI comparison

| | Claude Code | cursor-agent | agy |
|---|---|---|---|
| Wire transport | REST+SSE, public API | Connect-RPC/protobuf (`aiserver.v1.*`), same family as the IDE; `--endpoint` covers **only** Dashboard/Config/Models/Analytics — the completion plane is a fixed, non-overridable host (confirmed) | gRPC (local sidecar) → REST+SSE (cloud), internal API |
| Structured CLI output | `stream-json` (flag) | `stream-json` (flag) | none live; SQLite + JSON artifacts post-hoc |
| Tool-call id correlation | `tool_use.id` / `tool_use_id` | composite `call-…\nfc_…` | short opaque id (`tPSyDgZm`) |
| Diff/edit fidelity | unified-diff hunks (`structuredPatch`); whole-file write reuses the same shape with `structuredPatch:[]`/`originalFile:null` | unified diff **+ before/after full file**; delete preserves `prevContent` | **two confirmed shapes**: line-range replace + verification snapshot (`replace_file_content`: `StartLine`/`EndLine`/`TargetContent`/`ReplacementContent`) **and** whole-file write (`write_to_file`: `CodeContent`/`Overwrite`) — no unified-diff/hunk representation observed at all |
| Args encoding | native JSON object | native JSON object | **stringified JSON** (`argumentsJson` field, double-encoded) |
| Reasoning continuity token | `thinking.signature` | — (not observed) | `thinkingSignature` |
| Workspace model | implicit cwd | implicit cwd (+ `--workspace`) | explicit project registration, else silent scratch-dir fallback |
| Hosted (non-local) tools | `WebSearch` runs server-side on Anthropic's infra (distinct `srvtoolu_…` id) | not observed to be distinguished | not determined |
| Large-result handling | inline | **offloads to a local file + pointer string** for big web results | inline (not tested at scale) |
| Tool catalog reliability | mostly stable, partly lazy-loaded (`ToolSearch`) | stable per capture | **wire-declared catalog (~40, from binary) is a superset of what the model actually has** — verify liveness before trusting a name, don't assume the binary's oneof is the product's toolset |
| Already mapped in Glider | via public API knowledge, Path A gateway (`internal/api`) | **yes, one adapter covers both fronts** — `internal/cursorrpc/toolcall_map.go` targets the IDE's protobuf wire, but the terminal CLI speaks the same `aiserver.v1`/`agent.v1` Connect-RPC service (confirmed by raw capture); its `stream-json` JSON dialect is a second, friendlier serialization of the *same* schema (field names already match 1:1), not a competing protocol | not mapped |

The load-bearing observation: **all three converge on the same five verbs** — read, write/edit(diff), search(grep/glob), list, execute(shell) — plus a long tail of product-specific steps (todos, browser control, MCP, web search) that don't need first-class canonical types, just passthrough.

---

## Proposed common envelope

### Design principle: don't hardcode the taxonomy

The evidence above rules out a fixed canonical schema. Three data points that make hardcoding actively wrong, not just inelegant:

1. **One vendor, at least two genuinely different edit tools confirmed live.** Antigravity's wire format declares five edit-shaped names (`replace_file_content`, `write_to_file`, `propose_code`, `create_file`, `file_change`); a follow-up pass confirmed only two are actually reachable (`replace_file_content` — line-range + verification snapshot — and `write_to_file` — whole-file, also covers what would've been `create_file`), and confirmed the other three are **not** exposed to the model in this CLI build at all (the model self-reports missing tools rather than the capture simply missing them — see the `confirmed` field below). Two structurally different edit shapes from *one vendor*, proven live, is exactly the evidence that rules out a single canonical `EditTool` type — and also the evidence that "wire-declared" and "live" must be tracked as separate facts, not conflated.
2. **Three genuinely different diff models**, not three serializations of one model: Claude's unified-diff hunks, Cursor's unified-text + full before/after, Antigravity's range-replace-with-verification. There is no lossless "pick one" superset — `StartLine`/`EndLine`/`TargetContent` cannot be derived from a hunk without the full file, and a hunk cannot be derived from a range-replace without the full file either. Forcing one shape means either dropping vendor-native fields or requiring capabilities (full-file access) that not every call site has.
3. **Vendors rename constantly.** `cursor-agent`'s tool-call id is already two ids concatenated (`call-…\nfc_…`) — a fossil of a routing-tier change in progress. `agy`'s tool catalog has ~40 entries extracted from one binary; the next `agy` release will have a different set (Cascade/Windsurf's own tool catalog churns release to release, historically).

So: the canonical layer holds the smallest possible fixed core (correlation id, vendor origin, raw passthrough) and pushes everything else — naming, tagging, diff-view computation — into **data, not Go types**, loaded and reloadable independently of Glider's binary.

```text
claude stream-json  ──┐
cursor-agent JSON    ──┤   vendor pack (data)      canonical core (tiny, stable)      view/tag
agy SQLite+proto     ──┤   name/field aliases  ──▶  {id, vendor, raw, parts[]}    ──▶  resolvers
<next CLI, no code> ──┘   (hot-reloadable)                                            (pluggable)
```

### 1. Vendor packs, not Go switch statements

`internal/cursorrpc/toolcall_map.go`'s `CommonToolNameMappings` is the right *idea* (name aliases → wire variant) but the wrong *medium* — it's a Go `map` literal, so adding a tool name or a fourth CLI means a recompile. Move the mapping to data:

```yaml
# vendorpacks/agy.yaml  (revised after the exhaustive follow-up pass — see agent_cli_interop.md Gaps table)
vendor: agy
observed_cli_version: "antigravity-cli (Gemini 3.6 build, 2026-07)"
tools:
  replace_file_content:
    confirmed: true                   # actually invoked live and decoded — not just wire-declared
    tags: [edit, write]                # open-ended, multi-valued, best-effort — not an exhaustive enum
    args:
      path: [TargetFile]              # alias list → canonical arg name, same fold-then-exact strategy as LookupToolNameMapping
    diff_view: range_replace           # which view-producer (see §2) knows how to read this tool's raw args
  write_to_file:
    confirmed: true
    tags: [edit, write, create]
    args:
      path: [TargetFile]
      content: [CodeContent]
    diff_view: whole_file               # confirmed live — see §2's EditViews.WholeFile
  view_file:
    confirmed: true
    tags: [read]
    args:
      path: [AbsolutePath]
  list_dir:
    confirmed: true                    # NB: live wire name, not the wire-catalog's "list_directory" — see below
    tags: [list]
    args:
      path: [DirectoryPath]
  run_command:
    confirmed: true
    tags: [shell, execute]
    args:
      command: [CommandLine]
      cwd: [Cwd]
  propose_code:
    confirmed: false                   # deliberately re-tried twice (risky edit, plan mode) and never fired — see Gaps
    tags: [edit]
    diff_view: unknown
  delete_file:
    confirmed: false                   # model explicitly stated it has no dedicated delete tool; substitutes run_command
    tags: [delete]
  # ... remaining ~34 wire-declared CortexStep* names default to confirmed: false, tags: [other] —
  # never an error, never assumed live just because the binary declares them (see "wire-declared
  # catalog is not the live toolset" finding) — see "unknown tool" below
unknown_tool_policy: passthrough       # generalizes cursorrpc's TruncatedToolCall (wire field 34) fallback
confirmed_policy: prefer               # DecideVendor (native_glider_orchestration.md §4) should weight confirmed:true
                                        # capabilities over confirmed:false ones when picking a delegate target
```

`confirmed` exists because of a real mistake this research caught in itself: the first pass treated `agy`'s full wire-declared catalog (~40 names, extracted from the binary) as if it were the live toolset, and a follow-up pass proved that's wrong — the model self-reports missing tools that exist on the wire and substitutes `run_command` instead. Every vendor pack, including ones not yet captured, should default new entries to `confirmed: false` until an actual live trace flips them — the wire format and the product's live tool declaration are two different things and should never be conflated again.

One file per vendor, versioned against an observed CLI build string, checked into the repo like any other config, hot-reloadable the same way `routing.turn_family_ttl` already is (`docs/MITM_NETWORK.md` §Config knobs) — or via the reload pattern in `internal/backend/reload.go` / `internal/hotswap`, if that turns out to be the natural place to hang it; not a hard dependency, just the closest existing precedent for "swap config without restart" in this codebase. A new CLI is a new YAML file, not a new Go package. A renamed tool in a point release is a one-line diff to the pack, not a redeploy.

`tags` are deliberately **not** a closed enum. `edit`, `read`, `shell` etc. are just strings two packs happen to agree on; nothing breaks if `agy`'s next tool needs a tag nobody's used yet (`browser`, `knowledge_base`, whatever — the ~30 non-fs `CortexStep*` names already need this). Consumers that only care about "is this an edit" filter on tag membership; consumers that need the exact vendor behavior read `vendor_name` + `raw` and dispatch themselves.

### 2. Diffs as a bag of optional views + lazy converters, not one struct

Instead of one `CanonicalDiff` shape every adapter must populate (which is exactly the hardcoding problem — Antigravity literally cannot populate `Hunks` or `Before`/`After` from what it sends over the wire), model an edit as whichever views the source actually provided, plus a **converter registry** that computes other views on demand when it has enough information:

```go
type EditViews struct {
    Path string
    Raw  map[string]any // untouched vendor args — nothing is ever lossy, even for views we don't understand yet

    RangeReplace *RangeReplace // {StartLine, EndLine, OldSnapshot, New}      — agy's replace_file_content, natively
    Hunks        []DiffHunk    // {oldStart,oldLines,newStart,newLines,lines} — Claude's Edit structuredPatch, natively
    UnifiedText  string        // "--- a/... +++ b/..."                      — cursor-agent's diffString, natively
    Before       string        // full pre-image                             — cursor-agent's beforeFullFileContent, natively
    After        string        // full post-image                            — cursor-agent's afterFullFileContent, natively
    WholeFile    *WholeFile    // {Content, Overwrite bool}                   — agy's write_to_file AND Claude's Write, natively (see below)
    // a fifth vendor's native shape means one more optional field + converter functions,
    // not touching the others — WholeFile itself is proof this scales: it wasn't in the
    // original design, got added once two independent vendors confirmed they need it, and
    // nothing above it changed.
}
```

`WholeFile` is not hypothetical — it's confirmed independently by **two** vendors, which is exactly the kind of evidence this design was built to accommodate cheaply: agy's `write_to_file` (`{CodeContent, Overwrite, TargetFile}`) and Claude's own `Write` tool (`{file_path, content}`, result reusing `Edit`'s shape with `structuredPatch:[]`/`originalFile:null`) are both "replace the whole file, no diff computed" — genuinely distinct from `RangeReplace` (needs a line range) and from `Hunks`/`UnifiedText`/`Before+After` (all presuppose a diff was computed against a known prior state). Claude's own `null`/`[]` convention for this case is worth adopting directly: `WholeFile` populated + `Hunks == nil` + `Before == ""` is a normal, well-formed state, not a partial capture.

```go
// A converter is keyed by (have, want) and returns ok=false rather than lossy-guessing when it
// can't derive the target view from what it has (e.g. RangeReplace -> Hunks needs the full pre-image,
// which a "closer" without file access may not have — the caller decides whether to fetch it).
type Converter func(EditViews) (value any, ok bool)

var Converters = map[[2]string]Converter{
    {"hunks", "unified_text"}:         hunksToUnified,
    {"unified_text", "hunks"}:         unifiedToHunks,
    {"before_after", "hunks"}:         diffBeforeAfter,
    {"range_replace+before", "hunks"}: rangeReplaceToHunks,  // needs Before; if absent, this pair is just unavailable
    {"whole_file+before", "hunks"}:    wholeFileToHunks,      // treats Before as the pre-image, WholeFile.Content as After, diffs the two
    {"whole_file", "before_after"}:    wholeFileToAfterOnly,  // After = Content; Before stays empty unless fetched — an honest partial result, not an error
    // registry, not a fixed set — new converters slot in without touching EditViews or existing adapters
}
```

Consumers ask for a view (`views.Get("hunks")`); the resolver walks the converter graph from whatever the adapter natively populated and returns the best it can, or an explicit "unavailable, need X" rather than fabricating one. This is the same shape as the vendor-pack `unknown_tool_policy: passthrough` idea applied to diffs: **degrade explicitly, never silently guess.**

### 3. Canonical turn/message: thin envelope, vendor blocks pass through whole

```go
type Turn struct {
    Vendor string          // "claude" | "cursor" | "agy" | future
    Raw    json.RawMessage // the adapter's native event, untouched — always kept, always re-emittable to the *same* vendor
    Parts  []Part          // best-effort normalized view for cross-vendor consumers
}

type Part struct {
    Kind string // "text" | "tool_call" | "tool_result" | "reasoning" — open string, not an enum, same reasoning as tags above
    Text string
    ToolCall   *ToolCall
    ToolResult *ToolResult
    ReasoningToken string // Claude's thinking.signature / agy's thinkingSignature — opaque, passthrough only, never parsed
}
```

Two of three sources (Claude natively; Cursor via its `cus-` Anthropic-flavored Base URL path, per `internal/api/anthropic_normalize.go`) already lean Anthropic-block-shaped, and MCP (which Glider already speaks — `internal/mcp/`) uses the same name+JSON-args+content-result shape for tool calls, so `Part`/`ToolCall` deliberately mirror that rather than inventing a fourth convention. `agy`'s Gemini-native `contents[].parts[]` (text / functionCall / functionResponse) needs a real adapter here — same normalization problem `internal/api` already solves for OpenAI↔Anthropic, one more shape.

### 4. Adapters are plugins, not core code

Each vendor pack pairs with a small adapter (parse the CLI's native event stream or artifact format into `Turn{Raw, Parts}`, using the pack for name/tag/arg-alias resolution). Register adapters the same way Glider already registers extensibility — `internal/tools`' `plugin.Registry` / `mcp.Client` pattern already exists for exactly "add a capability without touching core"; an adapter is structurally the same kind of thing an MCP server or a builtin plugin already is. Concretely: **the core (`Turn`, `Part`, `EditViews`, the converter registry, the vendor-pack loader) should not need to change to add a fourth CLI** — only a new pack file + a new small adapter package, both purely additive.

### 5. Workspace binding is per-adapter policy, not core

`agy` requires explicit project registration and silently redirects to a scratch dir otherwise (finding above, reproduced live). This is exactly the kind of vendor-specific quirk that must NOT leak into the canonical core: it's an `agy`-adapter concern — resolve/create a project bound to Glider's existing run-scoped workspace root (`runs/<turn-id>/{work,out}`, per `docs/MITM_NETWORK.md` §Sticky `/cloud` turn family) before trusting any `agy` tool-call path, and do it in the `agy` adapter, not in `Turn`/`EditViews`. Claude and cursor-agent don't need this step at all; the core shouldn't carry a field for it.

---

## Gaps / follow-up

All items from the first two passes are now closed — either resolved with real evidence, or re-scoped from "uncaptured" to "tested and found not present in this CLI build," which is a different, more useful kind of closure than silence. Nothing below is aspirational.

| Topic | Status |
|-------|--------|
| agy `replace_file_content` (edit) payload | **Resolved.** Decoded raw from a live `step_payload` BLOB with a hand-rolled schema-less protobuf walker. Line-range replace + verification snapshot. |
| agy `write_to_file` (edit) payload | **Resolved.** `{"CodeContent", "Overwrite", "TargetFile", "Description", "toolAction", "toolSummary"}` — whole-file write. Confirmed as the tool for *both* create and overwrite; no separate `create_file` invocation ever occurred. |
| agy `view_file` / `list_dir` payloads | **Resolved.** `view_file`: `{"AbsolutePath"}`. `list_dir` (the wire-catalog's `list_directory` name is unused in practice): `{"DirectoryPath"}`. |
| agy generic step envelope (fields shared by every `CortexStep*`) | **Resolved** — field map reverse-engineered from four live samples now (`view_file`, `replace_file_content`, `write_to_file`, `list_dir`), consistent across all four. Not verified against a browser/MCP/knowledge-base step, so field numbers unique to those remain unconfirmed. |
| agy `propose_code`, `create_file`, `delete_file`, `move`, `find`, `view_file_outline`, `lint_diff`, `file_change`, browser/MCP/knowledge-base/git tools | **Closed, re-scoped as "not present," not "uncaptured."** Deliberately tried to force each (explicit delete-tool request → model stated it has no dedicated delete tool; risky rewrite without `--dangerously-skip-permissions` → still went through `write_to_file`, denied at the permission layer, no propose-review step; `--mode plan` → stayed read-only, no file tool at all; find/move/delete tasks → model substituted `run_command` unprompted every time). The wire-declared `exa.cortex_pb.CortexStep*` catalog (~40 entries, extracted from the binary) is confirmed to be a **superset** of what this CLI build actually exposes to the model — likely shared with the IDE / historical Cascade surface. Treat the binary-extracted list as "wire-capable, liveness unconfirmed," not as agy's real toolset. |
| agy sidecar wire probe (local language-server HTTP/gRPC port) | **Advanced, not fully resolved.** Confirmed externally reachable and confirmed to speak Connect-JSON (`POST .../GetAllCascadeTrajectories` with `Content-Type: application/json` → valid `200 {}`) — the harder half (does a JSON transport even exist) is answered yes. Getting a *populated* response needs an auth token or correctly-shaped request fields not reverse-engineered yet. Next step if revisited: try `--dangerously-skip-permissions` combined with reading `ANTIGRAVITY_CSRF_TOKEN`/`ANTIGRAVITY_SIDECAR_UI_TOKEN` out of the running process's environment (own process, should be readable) rather than guessing blind. |
| cursor-agent exact completion-plane RPC name / raw bytes | **Superseded — fully resolved with real bytes (2026-07-27).** An earlier pass (above paragraph, kept for history) correctly identified the completion plane lives outside `--endpoint`'s reach but stopped short of a real MITM capture. This round stood up an isolated capture proxy (`tools/wirecapture`, own process, reuses Glider's CA, never touches `internal/mitm/proxy.go`'s shared code) with **genuine HTTP/2 support** (`net/http.Server`'s own ALPN auto-negotiation on a single-connection listener — the missing piece: the completion host negotiates h2 unconditionally, which is exactly why every prior HTTP/1.1-only capture attempt saw nothing). Confirmed live: `POST /agent.v1.AgentService/Run` to `agentn.global.api5.cursor.sh`, Connect protocol (`application/connect+proto`, `connect-es/1.6.1` client, `Connect-Protocol-Version: 1`), bidi-streaming per `agent_v1.proto`'s own service definition (`rpc Run(stream AgentClientMessage) returns (stream AgentServerMessage)`). The captured request bytes were hand-decoded via protobuf wire-format walking and cross-checked field-for-field against `planning/vendor_ref/agent_v1.proto` — every length prefix matched exactly, including the prompt string's own byte length and the `AgentMode.AGENT_MODE_AGENT=1` enum value alongside it. Confirmed field path to the human prompt: `AgentClientMessage.run_request → AgentRunRequest.action → ConversationAction.user_message_action → UserMessageAction.user_message → UserMessage.text`. Implemented as `internal/ngl`'s cursor-agent `OriginAdapter` (`internal/ngl/adapter_cursor_origin.go`), request-side decode tested against the real captured bytes verbatim (`internal/ngl/adapter_cursor_origin_test.go`); response-side reuses `internal/cursorrpc.WriteRunSSEResponse` unmodified, since `Run` and `RunSSE` return the identical `AgentServerMessage` stream type and that encoder was already live-confirmed-accurate prior to this pass. This also resolves this doc's earlier "no explicit `AgentService`/`RunSSE`/`StreamChat*` call in the capture" note (top of file) — that capture simply couldn't see h2 traffic. |
| cursor-agent exhaustive tool coverage | **Resolved for 9 of ~26 wire variants; the rest triaged, not guessed at.** Confirmed live: `read`, `edit`, `grep`, `shell`, `glob`, `delete`, `web_fetch`, `web_search`, `update_todos` (see per-CLI table for exact schemas — notably `delete` preserves `prevContent`, `web_search` offloads large results to a file+pointer, `update_todos` has a real dependency-DAG field). Tried and not observed (model chose a different tool, not a confirmed gap): dedicated `ls`, `sem_search`. Not attempted at all (budget-limited, explicitly flagged as such rather than silently skipped): `read_lints`, `mcp`, `create_plan`, `task`, `switch_mode`, `ask_question`, `apply_agent_diff`, `exa_search`/`exa_fetch`, `generate_image`, `write_shell_stdin`, `reflect`. |
| Claude exhaustive tool coverage | **Resolved for all built-ins listed at init.** `Read`/`Edit`/`Grep` (pass 1) plus `Bash`/`Write`/`Glob`/`WebFetch`/`WebSearch`/`NotebookEdit`/`Task` (pass 2, this round) — see per-CLI table for exact schemas. Notable: `Task`'s wire name is actually `Agent`; `WebSearch` is server-hosted, not local; the tool catalog is itself partly lazy-loaded (`ToolSearch`), so "complete" here means "everything offered at this session's init," not a provably-exhaustive enumeration of everything Claude Code can ever expose. |
| Claude raw Messages API bytes | Not needed — public, already ground truth. |
| Adapter code | Not started — this doc plus `native_glider_orchestration.md` are the design. The `EditViews` design now has real confirmed requirements from all three vendors (range_replace, hunks, unified_text+before_after, and whole_file — see that doc's update) rather than being sized to one vendor's sample. |
| PII hygiene | Live traces touched a real authenticated Google account (email visible in `agy` logs) and real Anthropic/Cursor/Google billing across two full research passes — no credentials, raw transcripts, capture-proxy output, or SQLite/log files from this session should be committed; only the schema findings above are retained here. |
