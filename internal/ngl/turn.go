package ngl

import "encoding/json"

// Turn is the canonical, vendor-agnostic envelope for one exchange, per
// planning/agent_cli_interop.md §"Proposed common envelope" and
// planning/ngl_and_adapters.md §1. Raw is always kept —
// normalization is best-effort and additive, never a replacement for the
// vendor's own native event, so a Turn can always be re-emitted to the
// same vendor untouched.
type Turn struct {
	Vendor string          // "claude" | "cursor-agent" | "agy" | future
	Raw    json.RawMessage // the adapter's native event, untouched
	Parts  []Part
}

// ToolCall is a normalized tool invocation. Name and Args are the vendor's
// own names — a vendor pack's alias table (VendorPack.Tools[x].Args) maps
// vendor-specific arg field names to canonical ones for consumers that want
// cross-vendor uniformity; ToolCall itself never renames anything, so
// vendor_name + Args stays honest even for an unrecognized/unconfirmed tool.
//
// Confirmed and Tags are populated by VendorPack.AnnotateToolCall, not by
// the parser itself — parsing a wire event never requires a vendor pack to
// be loaded (keeps adapters testable in isolation from vendorpacks/*.yaml),
// so a freshly-parsed ToolCall has both zero-valued until a caller that
// actually has the pack loaded chooses to annotate it.
type ToolCall struct {
	ID        string          // correlates to a later ToolResult — shape varies wildly per vendor (see agent_cli_interop.md "Correlation" rows), never parsed, only compared for equality
	Name      string          // vendor's own tool name, e.g. "Edit", "editToolCall", "replace_file_content"
	Args      json.RawMessage // vendor's own arg encoding, untouched — some vendors (agy) double-encode this as a JSON string; adapters decode that before storing here so Args is always a real JSON value, not a string containing JSON
	Confirmed bool            // from the vendor pack, once annotated — never assumed true for an absent/unannotated call
	Tags      []string        // from the vendor pack, once annotated — open-ended, not a closed enum (agent_cli_interop.md §1)
	Hosted    bool            // true for tools that execute server-side on the vendor's own infra (e.g. Claude's WebSearch) — cannot be delegated to a headless session the way a local tool can, since there's no local process to spin up
}

// ToolResult is a normalized tool result, correlated to a ToolCall by ID.
type ToolResult struct {
	ToolCallID string
	Text       string // best-effort plain-text rendering; structured results (diffs, todos) also populate EditViews or stay in Raw
	IsError    bool
	// Ref points at an externally-stored full result instead of inlining
	// it — cursor-agent's own convention for large web results
	// (~/.cursor/projects/<hash>/agent-tools/<uuid>.txt + a pointer
	// string, agent_cli_interop.md §2), generalized here rather than kept
	// vendor-specific, since forcing every large blob inline is the wrong
	// default regardless of which vendor produced it.
	Ref string
}

// PartKind classifies one piece of a Turn's content. Deliberately an open
// string, not a closed enum — same reasoning as vendor-pack tags
// (agent_cli_interop.md §1): a vendor's own step type that doesn't fit any
// existing Kind just gets a new one, not a schema change.
const (
	PartToolCall  PartKind = "tool_call"
	PartReasoning PartKind = "reasoning"
)
