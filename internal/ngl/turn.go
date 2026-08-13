package ngl

import "encoding/json"

// Turn is the standard envelope for one exchange, and it has no relation to
// one vendor. Refer to the section "Proposed common envelope" in
// planning/agent_cli_interop.md, and to planning/ngl_and_adapters.md §1.
//
// The code always keeps Raw. The normalization adds data when it can, and it
// never replaces the native event of the vendor. Therefore the code can always
// send a Turn to the same vendor again, with no change.
type Turn struct {
	Vendor string          // "claude" | "cursor-agent" | "agy" | future
	Raw    json.RawMessage // the adapter's native event, untouched
	Parts  []Part
}

// ToolCall is a call to a tool in the standard form.
//
// Name and Args keep the names of the vendor. The table of aliases of a vendor
// pack, at VendorPack.Tools[x].Args, changes the arg field names of one vendor
// into the standard names, for a consumer that needs the same names across
// vendors. ToolCall itself never changes a name. Therefore the vendor name and
// Args stay true, and this is correct also for a tool that the code does not
// know or that no person confirmed.
//
// VendorPack.AnnotateToolCall gives the values for Confirmed and Tags. The
// parser does not give them. To parse an event on the wire never needs a vendor
// pack, and thus an adapter stays easy to test with no file from vendorpacks/.
// Therefore a ToolCall that the parser just made has the zero value in both
// fields, until a caller that has the pack decides to add the data.
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
	// Ref points at a full result that the code stores outside this structure, and
	// the code does not put that result here. This is the convention of
	// cursor-agent for a large result from the web.
	Ref string
}

// PartKind classifies one piece of a Turn's content. Deliberately an open
// string, not a closed enum — same reasoning as vendor-pack tags
// (agent_cli_interop.md §1): a vendor's own step type that does not fit any
// existing Kind just gets a new one, not a schema change.
const (
	PartToolCall  PartKind = "tool_call"
	PartReasoning PartKind = "reasoning"
)
