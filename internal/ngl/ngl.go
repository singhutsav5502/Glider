// Package ngl is the first and smallest part of "Native Glider Language".
// NGL is the standard content of a turn, and it has no relation to one
// vendor. planning/ngl_and_adapters.md describes it.
//
// A true defect made this package necessary. It is not an exercise in
// documentation. On 2026-07-26, a detector of the delegate flag searched
// the raw JSON of the wire format for a substring at each position in "the
// last message with the user role". Claude Code writes its own
// <system-reminder> content automatically. That content had the text of the
// flag in it, because a person discussed this function while a person built
// it. Therefore the detector took control of true turns of the
// conversation, and it gave no message.
//
// The correction is not a better regular expression on the raw JSON. The
// correction is to stop the use of "role: user" to mean "text that the
// person wrote".
//
// The wire format with the shape of Anthropic puts a minimum of three
// different items in one envelope with role="user":
//
//   1. True input from a person.
//   2. tool_result content blocks. Code sends these back to the API as
//      "user" turns, and no person writes them.
//   3. Content that the front CLI adds automatically. It is inside a usual
//      block with type="text", and a filter on the type of the block cannot
//      see it.
//
// NGL divides these for each vendor. Thus a caller sees only the true
// intent of the person.
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

// Part is one piece of content with its class. Code fills ToolCall,
// ToolResultData and ReasoningToken only when Kind shows that they are
// present: PartToolCall, PartToolResult and PartReasoning in that sequence.
// This agrees with the drawing in ngl_and_adapters.md §10. That drawing uses
// the name "ToolResult". The field here has the name ToolResultData, thus it
// does not collide with the name of the ToolResult type.
type Part struct {
	Kind PartKind
	Text string

	ToolCall       *ToolCall
	ToolResultData *ToolResult
	ReasoningToken string // Claude's thinking.signature / agy's thinkingSignature — opaque, passthrough only, never parsed
}

// wireMessage is the shape that the Messages API of Anthropic uses on the
// wire. One envelope with role="user" holds each of three items: true
// input, tool results, and content that the vendor adds inside a block with
// type="text".
type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// wireContentBlock is one entry in a content array. The block types of
// Anthropic are the true signal on the wire for the difference between a
// tool_result and text. These types are text, tool_use, tool_result, image
// and others. The first code did this part correctly.
//
// The first code did not know one fact: a block with type="text" does not
// contain *only* the text of a person. The front CLI of a vendor can put its
// own content in the same block. Refer to scaffoldStrippers below.
type wireContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// scaffoldStrippers holds the regular expressions for each vendor. They
// agree with the content that the vendor adds automatically. Code removes
// that content before it extracts a true instruction from a person.
//
// This is data on purpose, and it is not one fixed rule in code. The
// correction must not apply to Claude only. Today the map has one entry,
// because a live test confirmed the convention of only one vendor. To add
// the convention of a second front is one more entry in the map, and it is
// not a new version of ExtractUserText.
//
// Each vendor has a LIST, and not one pattern. One front can have more than
// one convention that operates independently, and claude does. With one
// regular expression for each vendor, the code applied only the first one
// and let the others through.
var scaffoldStrippers = map[string][]*regexp.Regexp{
	"claude": {
		// Auto-injected context wrapper, observed live and repeatedly,
		// present even in --bare mode, often multiple non-overlapping
		// occurrences per message. (?s) makes '.' match newlines; the block
		// bodies are multi-line. Non-greedy so each block closes at its own
		// nearest tag rather than spanning past intervening real content.
		regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`),
		// <transcript> holds a full record of an earlier conversation.
		// <session> holds data about a session. A live test on 2026-07-31
		// confirmed both, and it found a true leak. A <transcript> block
		// held a full summary of a session. Code wrote that summary in a
		// permanent continuity file, word for word, as if a person had
		// written it. This is exactly the error that this package prevents,
		// but on a new marker.
		//
		// Each pattern gives two forms as alternatives. The first form is
		// closed. The second form opens and continues to the end of the
		// text. A block that is short or that still arrives is a true
		// condition. A pattern that does not agree with such a block lets
		// the full block through with no change, and that is the worst
		// possible result here.
		regexp.MustCompile(`(?s)<transcript>.*?(?:</transcript>|$)`),
		regexp.MustCompile(`(?s)<session>.*?(?:</session>|$)`),
	},
}

// StripScaffold removes the content that a vendor adds automatically from
// text.
//
// A vendor with a name that this code does not know does nothing. That name
// is true, but no person has confirmed its convention. Never make a rule
// for a vendor with no confirmed convention.
//
// An EMPTY vendor name has a different meaning: the code could not identify
// the origin CLI. Refer to vendors.ResolveOriginVendorName, which a person
// added on 2026-07-26 to replace a fixed "claude" text at both call sites
// that face HTTP.
//
// The condition "remove nothing" already caused one true defect on
// 2026-07-26. A part of the flag was inside a block that the code did not
// remove, and the code read it as a true instruction from a person, because
// ParseDelegateCommand searches the full text.
//
// To remove this content is always safe. The code removes only the text
// that agrees with one narrow pattern for each vendor, and a person does
// not write such text. Therefore the safe result for an origin with no
// identity is to apply the pattern of EVERY vendor, and not the pattern of
// no vendor.
//
// TestDelegateHandler_ScaffoldedFlagDoesNotTriggerDelegation found this. It
// failed on the same day as the change. The synthetic request in the
// httptest fixture has no true origin process, thus it uses this "" path.
func StripScaffold(vendor, text string) string {
	if vendor == "" {
		for _, res := range scaffoldStrippers {
			for _, re := range res {
				text = re.ReplaceAllString(text, "")
			}
		}
		return strings.TrimSpace(text)
	}
	res, ok := scaffoldStrippers[vendor]
	if !ok {
		return text
	}
	for _, re := range res {
		text = re.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
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

// LastUserInstruction is the primary entry point. It takes the name of a
// vendor and a raw `messages` JSON array with the shape of Anthropic. Then
// it does four steps:
//
//  1. It finds the last message with role="user".
//  2. It keeps only the PartUserText parts of that message. It keeps no
//     tool_result part and no other type of block.
//  3. It joins those parts.
//  4. It removes the content that the vendor adds automatically.
//
// The result is the true equivalent of "the newest instruction of the
// person", and it knows the vendor. The code before the correction gave
// "the text in the last envelope with the user role", which is a different
// thing.
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

// PriorUserInstructions returns a maximum of max true instructions from a
// person. They come from BEFORE the newest instruction, and the oldest one
// is first. This is the limited record of the conversation that a delegated
// CLI needs, thus it does not start cold. Refer to vendors.ContextPack.
//
// The function removes the newest instruction on purpose. That instruction
// is already the task of the delegate, and other code gives it separately.
// To give it again as background looks like a second request.
//
// This function uses the same protection as LastUserInstruction: the same
// shape on the wire, the same ExtractParts filter to true PartUserText, and
// the same StripScaffold for each vendor. The danger is the same. The
// content that a front CLI adds automatically must never go to a delegate
// as if a person wrote it. That error is the first incident that NGL
// prevents, and a record of many messages gives it more opportunity than
// one message.
//
// The function does not give an entry that is empty after it removes the
// added content. Therefore a session with only added content in its earlier
// turns gives nothing, and not a list of empty lines.
func PriorUserInstructions(vendor string, messagesJSON []byte, max int) ([]string, error) {
	if max <= 0 {
		return nil, nil
	}
	var messages []wireMessage
	if err := json.Unmarshal(messagesJSON, &messages); err != nil {
		return nil, err
	}

	var collected []string
	seenLatest := false
	for i := len(messages) - 1; i >= 0 && len(collected) < max; i-- {
		m := messages[i]
		if m.Role != "user" {
			continue
		}
		if !seenLatest {
			seenLatest = true // this is the delegate's own task — skip it
			continue
		}
		var out strings.Builder
		for _, p := range ExtractParts(m.Content) {
			if p.Kind == PartUserText {
				out.WriteString(p.Text)
			}
		}
		if text := strings.TrimSpace(StripScaffold(vendor, out.String())); text != "" {
			collected = append(collected, text)
		}
	}

	// Collected newest-first while walking backwards; callers want oldest
	// first so the list reads chronologically.
	for l, r := 0, len(collected)-1; l < r; l, r = l+1, r-1 {
		collected[l], collected[r] = collected[r], collected[l]
	}
	return collected, nil
}
