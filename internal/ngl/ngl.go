// Package ngl is a first, minimal slice of "Native Glider Language" — the
// canonical, vendor-agnostic representation of a turn's content described in
// planning/ngl_and_adapters.md. It exists because of a real bug,
// not as a documentation exercise: on 2026-07-26, a delegate-flag detector
// that searched raw wire-format JSON for a substring anywhere in "the last
// user-role message" got tripped by Claude Code's own auto-injected
// <system-reminder> scaffolding (which had incidentally quoted the flag
// string while this very feature was being built and discussed), silently
// hijacking real conversation turns. The fix isn't a better regex on raw
// JSON — it's not treating "role: user" as "text the human typed" at all.
// Anthropic-shaped wire format conflates at least three different things
// under one role="user" envelope: genuine human input, tool_result content
// blocks (submitted back to the API as "user" turns, not human-authored),
// and vendor-specific auto-injected scaffolding that lives inside an
// ordinary type="text" block, invisible to block-type filtering alone. NGL's
// job is separating those, per vendor, so callers only ever see genuine
// human intent.
package ngl

import (
	"encoding/json"
	"regexp"
	"strings"
)

// PartKind classifies one piece of a message's content.
type PartKind string

const (
	PartUserText   PartKind = "user_text"
	PartToolResult PartKind = "tool_result"
	PartOther      PartKind = "other"
)

// Part is one classified piece of content. ToolCall, ToolResultData, and
// ReasoningToken are only populated when Kind indicates their presence
// (PartToolCall, PartToolResult, PartReasoning respectively) — mirrors
// ngl_and_adapters.md §10's sketch (there named "ToolResult"; the
// field here is ToolResultData to avoid colliding with the ToolResult type
// name itself).
type Part struct {
	Kind PartKind
	Text string

	ToolCall       *ToolCall
	ToolResultData *ToolResult
	ReasoningToken string // Claude's thinking.signature / agy's thinkingSignature — opaque, passthrough only, never parsed
}

// wireMessage is the Anthropic Messages API's on-the-wire shape — the same
// role="user" envelope for genuine input, tool results, and (embedded
// inside a type="text" block) vendor scaffolding.
type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// wireContentBlock is one entry of a content array. Anthropic's own block
// types (text, tool_use, tool_result, image, ...) are the real, wire-level
// signal for tool_result vs. text — that part of the original code was
// already correct. What was missing is that a type="text" block is not
// guaranteed to be *only* what a human typed; a vendor's own front can
// inject scaffolding into the same block (see scaffoldStrippers below).
type wireContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// scaffoldStrippers holds one regexp per vendor, matching that vendor's
// known auto-injected wrapper content so it can be removed before a genuine
// human instruction is extracted. This is deliberately data, not a single
// hardcoded rule — the whole point raised building this is that the fix
// must not be Claude-specific. Today there is exactly one entry because
// exactly one vendor's wrapping convention has been confirmed live; adding
// a second front's own convention later is one more map entry, not a
// rewrite of ExtractUserText.
var scaffoldStrippers = map[string]*regexp.Regexp{
	// Claude Code wraps auto-injected context in <system-reminder>...</system-reminder>,
	// observed live and repeatedly throughout this investigation, present
	// even in --bare mode, often multiple non-overlapping occurrences per
	// message. (?s) makes '.' match newlines; the block bodies are
	// multi-line. Non-greedy so each block closes at its own nearest tag
	// rather than spanning past intervening real content to the last tag
	// in the message.
	"claude": regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`),
}

// StripScaffold removes vendor's known auto-injected wrapper content from
// text. An unrecognized-but-named vendor (a real name with no known
// scaffolding convention yet) is a deliberate no-op — never guess a
// stripping rule for a vendor nobody has confirmed the convention for.
//
// An EMPTY vendor name means something different: the origin CLI could
// not be identified at all (see vendors.ResolveOriginVendorName, added
// 2026-07-26 to replace a previously-hardcoded "claude" literal at both
// HTTP-facing call sites) — and "no stripping" is exactly the condition
// that caused one real, live bug already (2026-07-26): a flag substring
// sitting inside an un-stripped scaffold block getting misread as a
// genuine human instruction, since ParseDelegateCommand searches the
// whole text. Stripping is a strictly safe operation — it only ever
// removes text matching one specific, narrow, auto-injected wrapper
// pattern per vendor, never something a human would plausibly type
// themselves — so the safe default for an unidentified origin is to apply
// EVERY known vendor's pattern defensively, not none. Caught by
// TestDelegateHandler_ScaffoldedFlagDoesNotTriggerDelegation failing the
// same day the generalization landed — the httptest fixture's synthetic
// request has no real resolvable origin process, so it exercises exactly
// this "" path.
func StripScaffold(vendor, text string) string {
	if vendor == "" {
		for _, re := range scaffoldStrippers {
			text = re.ReplaceAllString(text, "")
		}
		return strings.TrimSpace(text)
	}
	re, ok := scaffoldStrippers[vendor]
	if !ok {
		return text
	}
	return strings.TrimSpace(re.ReplaceAllString(text, ""))
}

// ExtractParts classifies an Anthropic-shaped content value (either a bare
// string or a content-block array) into Parts. A bare string is always
// PartUserText (that shape has no room for tool results). In a block array,
// type="text" becomes PartUserText, type="tool_result" becomes
// PartToolResult, anything else becomes PartOther with empty Text (images,
// tool_use, etc. — not text content at all).
func ExtractParts(content json.RawMessage) []Part {
	var asString string
	if json.Unmarshal(content, &asString) == nil {
		return []Part{{Kind: PartUserText, Text: asString}}
	}

	var blocks []wireContentBlock
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	parts := make([]Part, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, Part{Kind: PartUserText, Text: b.Text})
		case "tool_result":
			parts = append(parts, Part{Kind: PartToolResult, Text: b.Text})
		default:
			parts = append(parts, Part{Kind: PartOther})
		}
	}
	return parts
}

// LastUserInstruction is the main entry point: given a vendor name and a
// raw Anthropic-shaped `messages` JSON array, finds the last role="user"
// message, keeps only its PartUserText parts (never tool_result or other
// block types), concatenates them, and strips vendor's known scaffolding —
// the actual, vendor-aware equivalent of "the human's newest instruction",
// as opposed to "whatever text happens to sit in the last user-role
// envelope," which is what the pre-fix code effectively computed.
func LastUserInstruction(vendor string, messagesJSON []byte) (string, error) {
	var messages []wireMessage
	if err := json.Unmarshal(messagesJSON, &messages); err != nil {
		return "", err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "user" {
			continue
		}
		var out strings.Builder
		for _, p := range ExtractParts(m.Content) {
			if p.Kind == PartUserText {
				out.WriteString(p.Text)
			}
		}
		return StripScaffold(vendor, out.String()), nil
	}
	return "", nil
}
