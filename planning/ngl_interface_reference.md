# NGL interface reference — exhaustive

This is the precise, exhaustive technical reference for `internal/ngl`: every type, every interface method, every registration mechanism, every per-vendor implementation, and every real call site — cross-checked directly against the current source, not against an earlier design intent. For the *why this exists* narrative and the vision this package is one slice of, read [native_glider_orchestration.md](native_glider_orchestration.md) first; for how NGL relates to the separate execution-layer `VendorAdapter` interface, read [adapter_boundary.md](adapter_boundary.md). This doc assumes both and goes deep instead of wide.

Package doc (verbatim, because it states the actual motivating incident precisely): NGL exists because on 2026-07-26, a delegate-flag detector that searched raw wire-format JSON for a substring anywhere in "the last user-role message" got tripped by Claude Code's own auto-injected `<system-reminder>` scaffolding, silently hijacking real conversation turns. The fix isn't a better regex on raw JSON — it's not treating `role: user` as "text the human typed" at all. Anthropic-shaped wire format conflates at least three different things under one `role="user"` envelope: genuine human input, `tool_result` content blocks, and vendor-specific auto-injected scaffolding living inside an ordinary `type="text"` block, invisible to block-type filtering alone. NGL's job is separating those, per vendor, so callers only ever see genuine human intent.

## 1. Four interfaces, four different questions

| Interface | Question it answers | Data source | Direction | File |
|---|---|---|---|---|
| `OriginAdapter` | Is this HTTP request a vendor CLI's own live traffic to its own backend — and how do I answer as if I were that backend? | A live HTTP request Glider intercepted (MITM or gateway) | Incoming — network → Glider | `origin.go` |
| `DelegateRenderer` | What did a completed headless delegate run actually print, cleaned to just the final answer? | A completed subprocess's captured stdout | Outgoing (rendering) — subprocess → chat reply | `render.go` |
| `ParseXTurn` family | What does one line of a vendor's own stream-json output mean, structurally? | One event line of a vendor's headless/stream output | Outgoing (structuring) — subprocess → `Turn`/`Part` | `adapter_<vendor>.go` |
| `VendorPack` | What tools does this vendor have, which are confirmed live (not just wire-declared), and how do their arg names map to a canonical name? | A hand-authored YAML file per vendor | Static data, not a runtime dispatch interface | `vendorpack.go`, `vendorpacks/*.yaml` |

A fifth, genuinely separate boundary lives one layer down in `internal/vendors`: `VendorAdapter` handles execution-time side effects (denial detection, permission grants, session-id extraction) — a different question (process execution) from all four above (wire format). See `adapter_boundary.md`.

**Registration discipline, identical across all three registry-backed interfaces** (`OriginAdapter`, `DelegateRenderer`): each adapter registers itself from its own file's `init()` (`RegisterOriginAdapter`/`RegisterDelegateRenderer`), never from shared dispatch code. `ResolveOriginAdapter`/`ResolveDelegateRenderer` are linear scans over package-level slices (`originAdapters`, `delegateRenderers`) — fine at three vendors, would want reconsidering well before dozens. Nothing in `internal/mitm` or `internal/vendors` compares a vendor name literally anywhere in the dispatch path; every literal vendor-name comparison lives inside that vendor's own adapter file.

## 2. Core data model (`turn.go`, `ngl.go`)

```go
type Turn struct {
    Vendor string          // "claude" | "cursor-agent" | "agy" | future
    Raw    json.RawMessage // the adapter's native event, untouched
    Parts  []Part
}
```

`Raw` is always kept — normalization is best-effort and additive, never a replacement for the vendor's own native event, so a `Turn` can always be re-emitted to the same vendor untouched.

```go
type PartKind string

const (
    PartUserText   PartKind = "user_text"   // ngl.go
    PartToolResult PartKind = "tool_result" // ngl.go
    PartOther      PartKind = "other"       // ngl.go
    PartToolCall   PartKind = "tool_call"   // turn.go
    PartReasoning  PartKind = "reasoning"   // turn.go
)

type Part struct {
    Kind PartKind
    Text string

    ToolCall       *ToolCall  // populated iff Kind == PartToolCall
    ToolResultData *ToolResult // populated iff Kind == PartToolResult
    ReasoningToken string      // Claude's thinking.signature / agy's thinkingSignature — opaque, passthrough only, never parsed
}
```

Deliberately an open string type (`PartKind`), not a closed enum — a vendor's own step type that doesn't fit any existing `Kind` gets a new constant, never a schema change forcing every existing consumer to handle a new case defensively.

```go
type ToolCall struct {
    ID        string          // correlates to a later ToolResult — shape varies wildly per vendor, never parsed, only compared for equality
    Name      string          // vendor's own tool name, e.g. "Edit", "editToolCall", "replace_file_content"
    Args      json.RawMessage // vendor's own arg encoding, untouched — agy double-encodes this as a JSON string; adapters decode that before storing here so Args is always a real JSON value
    Confirmed bool            // from VendorPack, once annotated — never assumed true for an absent/unannotated call
    Tags      []string        // from VendorPack, once annotated — open-ended, not a closed enum
    Hosted    bool            // true for tools executed server-side on the vendor's own infra (e.g. Claude's WebSearch) — cannot be delegated to a headless session, no local process to spin up
}

type ToolResult struct {
    ToolCallID string
    Text       string // best-effort plain-text rendering; structured results also populate EditViews or stay in Raw
    IsError    bool
    Ref        string // points at an externally-stored full result instead of inlining — cursor-agent's convention for large web results, generalized here
}
```

**Parsing never requires a loaded `VendorPack`** — `Confirmed`/`Tags` start zero-valued on every freshly-parsed `ToolCall` and only get populated by `VendorPack.AnnotateToolCall`, a separate, explicit, optional step. This is what keeps every adapter unit-testable in isolation from `vendorpacks/*.yaml`.

### `ExtractParts` / `LastUserInstruction` — the incoming-side entry point (`ngl.go`)

```go
func ExtractParts(content json.RawMessage) []Part
func LastUserInstruction(vendor string, messagesJSON []byte) (string, error)
```

`ExtractParts` classifies an Anthropic-shaped `content` value (bare string, or a content-block array) — a bare string is always `PartUserText`; in a block array, `type="text"` → `PartUserText`, `type="tool_result"` → `PartToolResult`, anything else (images, `tool_use`, …) → `PartOther` with empty `Text`.

`LastUserInstruction` is the real entry point: finds the *last* `role="user"` message (not the first — a defensive choice repeated at every "which one do I trust" decision point in this package), keeps only its `PartUserText` parts (never `tool_result` or other block types), concatenates them, and strips this vendor's known scaffolding via `StripScaffold`.

```go
func StripScaffold(vendor, text string) string
```

`scaffoldStrippers` is a `map[string]*regexp.Regexp`, one entry per vendor with a *confirmed* auto-injected wrapper convention — today exactly one entry (`"claude"` → `(?s)<system-reminder>.*?</system-reminder>`). An unrecognized-but-named vendor is a deliberate no-op — **never guess a stripping rule for a vendor nobody has confirmed the convention for.** An *empty* vendor name means something different — the origin CLI couldn't be identified at all — and applies **every** known vendor's pattern defensively rather than none, since "no stripping" is exactly the condition that caused the original live bug this package exists to fix. (Regression test: `TestDelegateHandler_ScaffoldedFlagDoesNotTriggerDelegation`.)

## 3. `OriginAdapter` — the full method contract (`origin.go`)

```go
type OriginAdapter interface {
    Vendor() string
    Matches(r *http.Request) bool
    ReadRequestBody(r *http.Request) ([]byte, error)
    ExtractUserInstruction(body []byte) (text, model string, stream, ok bool, err error)
    WriteReply(w http.ResponseWriter, model string, stream bool, header string, replyText <-chan string) error
}
```

Each method's contract, with the incident that shaped it:

- **`Matches(r)`** — structural (host/path shape) recognition, checked *before any body read*. Must be side-effect-free and must not consume `r.Body`. Built 2026-07-27 after `internal/mitm`'s delegate handler hardcoded `r.URL.Path != "/v1/messages"` as its entry gate — true only when Claude Code is the front CLI. cursor-agent's and agy's own real traffic is never that shape (Connect-RPC and a Gemini-style REST call respectively), so typing a delegate flag directly into either CLI silently did nothing; the hardcoded gate rejected the request before any flag-parsing ever ran.

- **`ReadRequestBody(r)`** — reads this vendor's own body in whatever way is safe for its wire protocol. Most vendors are simple request/response (`io.ReadAll`), but doing that *unconditionally* caused a real, live-confirmed bug (2026-07-29): cursor-agent's `agent.v1.AgentService/Run` is a genuine **bidi-streaming** RPC whose real client keeps its request stream's send side open ~30 seconds (periodic keepalive envelopes) even for one headless turn — `io.ReadAll` blocked the whole handler for that entire window, and the client, having received nothing by the time it finished its own send side, fired `RST_STREAM(CANCEL)` at essentially that moment, dooming every subsequent write regardless of shape or timing.

- **`ExtractUserInstruction(body)`** — parses the vendor's own body, returns the newest human instruction (scaffolding already stripped), model name, and whether it's a streaming request. **`ok=false` with a nil error is a distinct, deliberate outcome from `err != nil`**: it means the body parsed structurally fine but this adapter has no *confirmed, verified* way to separate genuine human text from scaffolding for what it found. Callers **must** treat `ok=false` exactly like a parse failure — fall through to real origin passthrough — never fall back to a naive raw-body substring scan. That exact shortcut, tried once for Claude before NGL existed, is the original bug this whole package exists to prevent; guessing at an unverified schema carries the identical risk for a different vendor.

- **`WriteReply(w, model, stream, header, replyText)`** — renders a synthetic reply in the vendor's own wire shape, in place of forwarding to the real origin. `header`, if non-empty, must be written (or otherwise get real bytes flowing) *as early as possible*, before blocking on `replyText` — built 2026-07-29 after cursor-agent's own HTTP/2 client abandoned a delegate reply stream (`http2: stream closed`) that received zero bytes for the whole duration of a slow delegate call. `header` doubles as telling the human what was delegated to whom, since a synthesized reply otherwise gives no indication a different CLI produced it. `replyText` delivers exactly one value then closes — implementations must not assume it's already closed when `WriteReply` is called; a slow delegate call fills it asynchronously from a separate goroutine.

### `HostWithoutPort` (`origin.go`)

```go
func HostWithoutPort(r *http.Request) string
```

Strips a trailing `:port` from `r.Host` before comparing against a bare hostname suffix. Real, live-confirmed bug (2026-07-28): cursor-agent's and agy's `Matches` both compared `r.Host` directly against a bare suffix — correct for the CONNECT-based/gateway path (Go's `http.Client` omits the default `:443`), wrong for transparent interception, where the client's own HTTP/2 `:authority` pseudo-header is **not** guaranteed to omit it (confirmed live: `r.Host` was literally `"agentn.global.api5.cursor.sh:443"`). `Matches()` silently never fired even though the request genuinely reached Glider — traffic fell through to origin passthrough with no error, no log line pointing at why.

### `ResolveOriginAdapter`

```go
func ResolveOriginAdapter(r *http.Request) OriginAdapter // first registered adapter whose Matches(r) is true, or nil
```

`nil` means "not this codebase's own traffic to a known vendor backend" — callers must treat it exactly like `vendors.ResolveOriginVendorName`'s `""` result: a safe, valid "not our concern," never an error.

## 4. `DelegateRenderer` — the full method contract (`render.go`)

```go
type DelegateRenderer interface {
    Vendor() string
    Render(raw []byte) (clean string, ok bool)
}
```

Deliberately a **separate** interface from `OriginAdapter`, not a reused/overloaded one — `OriginAdapter` recognizes and replies to a vendor's own *live network traffic*; `DelegateRenderer` formats a vendor's *captured subprocess stdout* from a headless delegate run. Different data source, different question, no shared contract.

Built 2026-07-28 after a real reported problem: Claude's and cursor-agent's headless `-p --output-format stream-json` output is raw NDJSON — every internal event line, not just the final answer — and relaying that whole blob as a delegate reply is unreadable for day-to-day use, even though it's exactly what a debugging session wants to see. `ok=false` (no registered renderer, or this run's raw bytes don't parse the way this renderer expects) means callers must fall back to raw text **with a visible note**, never silently swap in a guessed clean answer.

`vendors.ResolveDelegate` renders through `renderDelegateReply` (`internal/vendors/render.go`) by default (`clean` mode) — a single Vendors-page dashboard toggle (`ResponseDetail`/`SetResponseDetail`), not a per-message flag. Raw mode always shows the unformatted transcript. Clean mode: no renderer registered → raw text, no note (a normal, unremarkable state, not a degradation). Renderer registered but declines (`ok=false`) → raw text **with** an explicit note appended — only this case means formatting genuinely degraded for *this* run.

## 5. Per-vendor implementation matrix

### claude

| Method/behavior | Detail |
|---|---|
| Real wire shape | Anthropic Messages API, `POST /v1/messages`. Public spec — ground truth, not reverse-engineered. |
| `Matches` | Path-only (`r.Method == POST && r.URL.Path == "/v1/messages"`) — no host check, deliberate: the whole point of `ANTHROPIC_BASE_URL` repointing is that the host varies; the path is the one constant. |
| `ReadRequestBody` | Plain `io.ReadAll(r.Body)` — no bidi concern. |
| `ExtractUserInstruction` | Delegates entirely to `LastUserInstruction("claude", req.Messages)` — this adapter is just the `OriginAdapter`-shaped wrapper; `internal/api/anthropic_messages.go` (a Claude-only gateway route) calls `LastUserInstruction` directly, not through this adapter. |
| `WriteReply` | Non-streaming: one atomic JSON write (`writeClaudeJSON`) — `header + <-replyText` concatenated, since there's no way to get `header` on the wire early for a single atomic body. Streaming: `writeClaudeSSE` — sends `header` as its own `text_delta` event immediately, then blocks on `replyText`, sends it as a second `text_delta`. No periodic keep-alive ticker — no timeout confirmed for Claude's own client the way one has for cursor-agent's. |
| Outgoing parse (`ParseClaudeTurn`) | `type="text"` blocks → `PartUserText`; `type="tool_use"` → `PartToolCall`. `Hosted` is set from the `tool_use` id's prefix: `srvtoolu_` (server-side, e.g. WebSearch) vs. `toolu_` (client-side) — Claude's own signal, not an NGL invention. |
| Scaffold convention | `<system-reminder>...</system-reminder>`, confirmed live, present even in `--bare` mode, often multiple non-overlapping occurrences per message. |
| Tool catalog (confirmed-live, `adapter_claude.go`) | Edit/Write (`ClaudeEditViews` — empty `structuredPatch` + null `originalFile` = whole-file create/overwrite), Bash, Glob, WebFetch (fetch-and-*summarize*, not raw HTML), WebSearch (hosted; result is a **heterogeneous** array — one structured-hits object then one markdown-summary string, not homogeneous), NotebookEdit (native `Before`/`After`, unlike Write's null-`originalFile` convention), Read (polymorphic: plain text vs. `.ipynb` → `{type:"notebook", file:{...}}`), Task (Claude's own subagent-delegation primitive — the concrete precedent `native_glider_orchestration.md`'s Delegate design is built on; wire name is actually `"Agent"`, not `"Task"`). |
| Result/denial signal | `ClaudeResultEvent.PermissionDenials` — confirmed live, a clean structured `[{"tool_name":...,"tool_use_id":...,"tool_input":...}]` array whenever any tool call in the run was denied. No prose-parsing required (contrast agy, which has nothing like this). |

### cursor-agent

| Method/behavior | Detail |
|---|---|
| Real wire shape | Connect-RPC over genuine HTTP/2, `POST /agent.v1.AgentService/Run`. Confirmed live via an isolated capture proxy (`tools/wirecapture`) with real h2 support — an earlier plain-HTTP capture pass missed this entirely because the completion host negotiates HTTP/2 unconditionally. Field path to the human prompt hand-decoded and cross-checked field-for-field against the public `agent_v1.proto`: `AgentClientMessage.run_request → AgentRunRequest.action → ConversationAction.user_message_action → UserMessageAction.user_message → UserMessage.text`. |
| `Matches` | `POST`, `HostWithoutPort(r)` suffix `.cursor.sh`, path exactly `/agent.v1.AgentService/Run`. |
| `ReadRequestBody` | `cursorrpc.ReadFirstEnvelope(r.Body)` — reads only the **first** Connect envelope, not the whole body. This is *not lossy* for this call shape: the human's prompt is always the first envelope; everything after is periodic keepalives on an RPC that's genuinely bidi-streaming at the protocol level even for one headless turn. |
| `ExtractUserInstruction` | Hand-rolled protobuf wire-format walker (`findLDField`, `readVarintLocal`) — no generated Go types exist for `agent.v1`. Walks the field path above via raw length-delimited field extraction. |
| `WriteReply` | `cursorrpc.WriteDelegateReplyWithKeepAlive` — **not** the existing `WriteRunSSEResponse` used elsewhere in the codebase's Path B fulfillment. Real, live-confirmed fix (2026-07-29) for cursor-agent's own HTTP/2 client abandoning the stream when `replyText` resolved synchronously with zero bytes sent in the meantime. `model` is ignored — `AgentServerMessage`'s own frames carry no model field, matching the real origin. |
| Outgoing parse (`ParseCursorAgentTurn`) | Dispatches on top-level `"type"`: `user`/`assistant` → text `Part`s; `tool_call` → `ParseCursorToolCall` (ID fixed 2026-07-26 to come from `toolCallId`, confirmed identical to the outer `call_id` — an earlier version left `ID` silently always empty); `thinking` → `PartReasoning` (empty `Text` on the `"completed"` subtype, a closing marker with no text of its own); `system`/`init`, `result`, `interaction_query` → `Raw` preserved, `Parts` empty (session/protocol bookkeeping, not turn content). |
| Composite tool-call ID | `ParseCursorCompositeID` splits `"call-<uuid>-<n>\nfc_<uuid>_<n>"` (embedded newline) into a Cursor-wrapper half and an OpenAI-style `fc_…` half — implies an OpenAI-shaped model sits behind at least one routing tier. |
| Tool catalog (confirmed-live, `adapter_cursor.go`) | `editToolCall` (`CursorEditViews` — richer than Claude's: natively populates unified diff text **and** both full before/after snapshots at once), `deleteToolCall` (preserves deleted content in `prevContent` — undo-friendly), `globToolCall`, `grepToolCall` (results keyed by workspace **root**, each holding per-file match groups), `webFetchToolCall` (pre-converted to markdown, unlike Claude's model-summarized fetch), `webSearchToolCall` (references typically hold **one** entry with a large synthesized chunk, not one reference per hit), `updateTodosToolCall` (real DAG `Dependencies` field exists but wasn't observed populated live — model expressed a dependency as free text instead). |
| Rejection signal | `CursorToolRejection` — a completed `shellToolCall` carries `{"result":{"rejected":{"command":...,"reason":""}}}` in place of `result.success`; `reason` was empty in every observed rejection. |
| A genuinely new, only-partially-understood event | `CursorInteractionQuery` (`"interaction_query"`) — a permission-gate protocol distinct from the tool-call lifecycle, observed wrapping a web-search approval; whether a sibling "denied" shape exists, and whether this gate applies beyond web search, are both unconfirmed. |

### agy

| Method/behavior | Detail |
|---|---|
| Real wire shape | Gemini-Cloud-Code-Assist-internal REST, `POST .../v1internal:streamGenerateContent`, `Authorization: Bearer ya29...` (OAuth2, not an API key). Confirmed live via an isolated capture proxy — structurally unrelated to both Anthropic's Messages API and Cursor's Connect-RPC family, which is itself the evidence that per-vendor `OriginAdapter`s are load-bearing, not ceremony: none of the three vendors captured so far share a wire shape. |
| `Matches` | `POST`, `HostWithoutPort(r)` suffix `cloudcode-pa.googleapis.com`, path contains `:streamGenerateContent`. |
| `ReadRequestBody` | Plain `io.ReadAll` — no bidi concern. |
| `ExtractUserInstruction` | agy's scaffolding convention is **inverted** relative to Claude's: the entire `parts[].text` blob is agy's own construction, with genuine human text nested inside `<USER_REQUEST>...</USER_REQUEST>` and sibling tags (`<ADDITIONAL_METADATA>`, `<USER_SETTINGS_CHANGE>`, …) holding agy's own injected context alongside it. Extraction means pulling the tag's *inner* content out, not removing a wrapper from around real text (`agyUserRequestTag` regex). No match → `ok=false`, refuse rather than guess. |
| `WriteReply` | Sends `header` as its own `data:` SSE event immediately, then blocks on `replyText`, sends it as a second event. **Event boundary is `\r\n\r\n`, not `\n\n`** — matches the real captured response byte for byte; agy's own client parser was built against that framing. No periodic keep-alive ticker confirmed necessary. |
| Outgoing parse (`ParseAgyTurn`) | agy's natural unit really is **one tool call per envelope** (one row in its steps table) — a genuine per-vendor granularity difference from Claude's "many Parts from one content-block array" and cursor-agent's "one Part from one stream-json line," not papered over. Args arrive double-encoded (a JSON string containing JSON, `argumentsJson`) — decoded before storage so `ToolCall.Args` is always a real JSON value. `thinkingSignature` → `ReasoningToken`. |
| Tool catalog (confirmed-live, `adapter_agy.go`) | `replace_file_content` (`RangeReplace` — structurally closer to an LSP `TextEdit{range, newText}` plus an optimistic-concurrency snapshot than to a diff), `write_to_file` (confirmed as the tool for **both** creating new files and overwriting existing ones — no distinct `create_file` was ever actually invoked), `view_file`, `list_dir` (live wire name — **not** the wire-catalog's `list_directory`, which was never actually invoked), `run_command` (agy's universal fallback whenever a dedicated fs tool it doesn't actually have would otherwise be needed). |
| The vendor that proved a single canonical edit type is wrong | agy alone confirmed **two** structurally different edit shapes live (`replace_file_content`'s range-replace vs. `write_to_file`'s whole-file) — the direct evidence behind `EditViews`' multi-native-view design (§6 below) rather than one canonical struct every adapter must populate. |

## 6. `EditViews` — cross-vendor edit normalization (`editviews.go`)

```go
type EditViews struct {
    Path string
    Raw  map[string]any // untouched vendor args — nothing is ever lossy, even for views not understood yet

    RangeReplace *RangeReplace // agy's replace_file_content, natively
    Hunks        []DiffHunk    // Claude's Edit structuredPatch, natively
    UnifiedText  string        // cursor-agent's diffString, natively
    Before       string        // cursor-agent's beforeFullFileContent, natively
    After        string        // cursor-agent's afterFullFileContent, natively
    WholeFile    *WholeFile    // agy's write_to_file AND Claude's Write, natively
}
```

**Never one canonical struct every adapter must populate** — three vendors' confirmed wire shapes rule that out (agy alone confirmed two structurally different edit shapes live, so there is no single lossless "pick one" superset). Instead: a value carries whichever views its source natively provided, plus a converter registry (`Converters map[[2]string]Converter`) that computes other views **on demand**.

```go
func (v EditViews) Get(want string) (any, bool)
```

Checks the requested view directly first (`direct`), then walks `Converters` from whatever's natively available. Returns `ok=false` — **never a fabricated guess** — when the target view can't be derived from what's available. Registered converters today: `hunks → unified_text` (real, exact rendering), `before_after → unified_text` (an **honest partial result**: without real line-level diffing, renders the two full snapshots as one "replace everything" hunk rather than fabricate a plausible-looking line-by-line diff it didn't actually compute — real line-diffing is a real follow-up, not faked here), `whole_file → before_after` (treats `Content` as the post-image; `Before` stays empty unless separately fetched).

Per-vendor `EditViews` producers: `ClaudeEditViews`, `ClaudeNotebookEditViews`, `CursorEditViews`, `AgyEditViews` (all listed with their native views in §5's tool-catalog rows above).

## 7. `VendorPack` — data-driven tool catalog (`vendorpack.go`, `vendorpacks/*.yaml`)

```go
type VendorPack struct {
    Vendor             string
    ObservedCLIVersion string
    Tools              map[string]ToolSpec
    UnknownToolPolicy  string
    ConfirmedPolicy    string
}

type ToolSpec struct {
    Confirmed bool                // actually invoked live and decoded, not just wire-declared
    Tags      []string            // open-ended, multi-valued, best-effort — not a closed enum
    Args      map[string][]string // canonical arg name -> vendor's own field-name alias(es)
    DiffView  string              // which EditViews field this tool's raw args populate natively
}
```

`Confirmed` is a real, meaningful distinction, not a formality — agy's wire-declared tool catalog runs to ~40 entries while its actually-invoked live surface is ~7 tools (`vendorpacks/agy.yaml`). Existing packs on disk: `vendorpacks/claude.yaml`, `vendorpacks/cursor-agent.yaml`, `vendorpacks/agy.yaml`.

```go
func LoadVendorPack(path string) (*VendorPack, error)
func (p *VendorPack) CanonicalArg(tool, vendorFieldName string) (string, bool)
func (p *VendorPack) IsConfirmed(tool string) bool
func (p *VendorPack) AnnotateToolCall(tc *ToolCall)
```

`AnnotateToolCall` is the **only** place pack data reaches a `ToolCall` — parsing (every `adapter_<vendor>.go`) never calls this itself, which is exactly what keeps every adapter testable in isolation from `vendorpacks/*.yaml` (none of the adapter tests load a pack). A tool absent from the pack is left `Confirmed=false, Tags=nil` — genuinely *unknown*, not merely *unconfirmed*; callers implementing `UnknownToolPolicy` decide what that distinction means, `AnnotateToolCall` never guesses.

## 8. Real integration points — verified against source, not assumed

| Symbol | Called from | Purpose |
|---|---|---|
| `ngl.ResolveOriginAdapter` | `internal/mitm/delegate_handler.go` | The one and only live dispatch point recognizing a vendor's own traffic for the delegate/permission-relay feature. |
| `ngl.ResolveDelegateRenderer` | `internal/vendors/render.go` (`renderDelegateReply`) | Formats a completed headless run's output for the default "clean" reply mode. |
| `ngl.LastUserInstruction` | `internal/api/anthropic_messages.go`, `internal/vendors/origin.go` | The gateway's own Claude-only route calls this directly (not through `claudeOriginAdapter`) — `ResolveOriginVendorName`'s result (or `""` for an unidentified origin) is passed straight through. |
| `ngl.HostWithoutPort` | `adapter_cursor_origin.go`, `adapter_agy_origin.go` | Both non-Claude `Matches` implementations, for the reason in §3. |

**Honest gap, worth stating plainly**: `ParseClaudeTurn`, `ParseCursorAgentTurn`, and `ParseAgyTurn` — the entire `ParseXTurn` family — have **no call sites outside their own tests** as of this writing. They're built, individually unit-tested, and structurally sound, but not yet wired into any live runtime path. This matches `native_glider_orchestration.md`'s framing of NGL as "a first, minimal slice" of a larger cross-CLI orchestration vision — the outgoing/structuring direction exists ahead of a caller that needs it (a future native subagent/session-concurrency feature, per that doc's §3b, "not built"), not because it's dead code.

## 9. Adding a fifth vendor — the actual checklist

Extends the "fourth vendor" checklist already in `docs/site/ngl.html`, now cross-checked against real integration points above. A complete addition touches:

1. `configs/vendor_candidates.yaml` — probe args, print flag, `default`/`resume`/`interactive` templates (data, not Go).
2. `internal/vendors/adapter.go` — a `VendorAdapter` for denial detection, session-id/edit-view extraction (execution layer, not wire format — see `adapter_boundary.md`).
3. `internal/ngl/adapter_<vendor>.go` — a `ParseXTurn`, only if the vendor's own stdout needs structuring beyond plain text (agy's renderer shows this can be a near-identity no-op if the CLI's headless output is already plain prose).
4. `internal/ngl/adapter_<vendor>_origin.go` — an `OriginAdapter`, **only once its real live wire shape is confirmed** via an isolated capture (`tools/wirecapture`) — never registered on a guess. Register in `init()`.
5. `internal/ngl/adapter_<vendor>_render.go` — a `DelegateRenderer`, once its headless output shape is confirmed. Register in `init()`.
6. `vendorpacks/<vendor>.yaml` — tool catalog, `Confirmed`/`Tags`/`Args` per tool, built up incrementally as tools are actually observed live, not declared speculatively from documentation.

None of the above require touching `internal/mitm/delegate_handler.go`, `vendors.ResolveDelegate`, or any other shared dispatch code — the entire point of the interface split in §1.

## 10. Known gaps and open questions

- **`ParseXTurn` has no live caller** (§8) — the outgoing/structuring direction is ahead of its consumer.
- **`CursorInteractionQuery`'s "denied" shape is unconfirmed** — only the approved path for a web-search permission gate was ever observed live; whether this gate extends to other tools is open.
- **`cursorEditResult`'s `message` field** (`"The file <path> has been updated."`) is deliberately not modeled into `EditViews` — presentation text, not diff content, but worth knowing it's dropped, not missed.
- **Claude's Edit/Write/Bash use of the `tool_use_result` sibling-field convention** (`claudeUserToolResultEvent`) is confirmed for ToolSearch and WebSearch specifically; Edit/Write/Bash's use of the same convention is inferred from Claude Code's own internal consistency, not independently reconfirmed in the pass that found it — worth a fresh capture before leaning on it for something high-stakes.
- **`before_after → unified_text` conversion produces an honest partial result**, not a real line-level diff — a real diffing algorithm is a legitimate follow-up if any consumer needs actual hunks from a before/after-only source (cursor-agent's own edit result already provides real hunks natively via `diffString`, so this gap is really only cursor-agent-adjacent-but-missing-the-native-field or a hypothetical future vendor with only whole-file before/after).
- **Registration is a linear scan** (`ResolveOriginAdapter`, `ResolveDelegateRenderer`) — fine at three vendors, would want a map keyed by a cheap discriminator (host, or a registered prefix table) well before this list grows much further.
