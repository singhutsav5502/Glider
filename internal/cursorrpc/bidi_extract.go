package cursorrpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/glider-ai/glider/internal/backend"
)

// BidiExtract is a harness-ready view of a context_envelope BidiAppend (Phase 1).
// Paired with RunSSE text codec (WriteRunSSETextResponse) for Path B fulfill.
type BidiExtract struct {
	Request   *backend.CompletionRequest
	Inspect   *BidiAppendInspect
	UserText  string
	ModelHint string
	Source    string // tiptap_text | printable_hint | section_fallback
}

// TipTap / ProseMirror text nodes as seen in Capture 5 nested field 2.
var tipTapTextRE = regexp.MustCompile(`"type"\s*:\s*"text"\s*,\s*"text"\s*:\s*"((?:\\.|[^"\\])*)"`)

// ExtractBidiCompletionRequest maps a BidiAppend body (HTTP-gunzipped if needed)
// into a CompletionRequest when the append is a context_envelope with recoverable
// user text. Returns (nil, nil) when the body is not an extractable turn
// (acks, tool blobs, empty).
//
// Local context contract (Path B):
//   - Messages: exactly one user message = TipTap LatestUserTurnText (not the
//     full context_envelope / history / system sections).
//   - Tools: never set (child tool RunSSE stays origin until tool codec).
//   - Model: string hint from envelope or "cursor-agent".
//   - Complexity / max_mode / model_tier: not present on the wire in MITM dumps
//     (2026-07-18); when Cursor exposes them, call router.TryAttachCursorComplexity.
// CompleteLocal receives this slim request via AgentFulfillHub — sticky Cloud
// deny-local still runs before fulfill in intercept.go.
func ExtractBidiCompletionRequest(body []byte) (*BidiExtract, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	insp, err := InspectBidiAppend(body)
	if err != nil {
		return nil, err
	}
	if insp == nil || insp.RoleGuess != RoleGuessContextEnvelope {
		return nil, nil
	}

	inner, err := unwrapBidiInner(body)
	if err != nil || len(inner) == 0 {
		return nil, nil
	}
	field2 := contextEnvelopeField2(inner)
	userText, source := extractUserTextFromPromptPack(field2, insp)
	if strings.TrimSpace(userText) == "" {
		return nil, nil
	}
	model := guessModelHint(insp)
	if model == "" {
		model = "cursor-agent"
	}

	req := &backend.CompletionRequest{
		Model: model,
		Messages: []backend.Message{
			{Role: "user", Content: userText},
		},
		Stream: true,
		Metadata: backend.RequestMetadata{
			RequestID:     insp.RequestID,
			OriginalModel: model,
			Adapter:       "cursor_bidi_extract",
			Priority:      backend.PriorityHigh,
		},
	}
	return &BidiExtract{
		Request:   req,
		Inspect:   insp,
		UserText:  userText,
		ModelHint: model,
		Source:    source,
	}, nil
}

func unwrapBidiInner(body []byte) ([]byte, error) {
	insp, err := InspectBidiAppend(body)
	if err != nil {
		return nil, err
	}
	// Re-walk outer field 1 to get raw inner bytes (Inspect already classified).
	off := 0
	for off < len(body) {
		key, n := readVarint(body[off:])
		if n <= 0 {
			break
		}
		off += n
		fieldNum := int(key >> 3)
		wireType := int(key & 7)
		switch wireType {
		case 0:
			_, vn := readVarint(body[off:])
			if vn <= 0 {
				return nil, nil
			}
			off += vn
		case 1:
			off += 8
		case 5:
			off += 4
		case 2:
			ln, vn := readVarint(body[off:])
			if vn <= 0 || off+vn+int(ln) > len(body) {
				return nil, nil
			}
			off += vn
			chunk := body[off : off+int(ln)]
			off += int(ln)
			if fieldNum != 1 {
				continue
			}
			switch {
			case isASCIIHex(chunk):
				return hex.DecodeString(string(chunk))
			case len(chunk) >= 2 && chunk[0] == 0x1f && chunk[1] == 0x8b:
				return gunzipCapped(chunk, maxGunzipField1)
			case looksLikeProto(chunk):
				return chunk, nil
			default:
				return nil, nil
			}
		default:
			return nil, nil
		}
	}
	_ = insp
	return nil, nil
}

// contextEnvelopeField2 returns the largest nested field-2 LD under inner top field 1.
func contextEnvelopeField2(inner []byte) []byte {
	if firstFieldNum(inner) != 1 {
		return nil
	}
	top := firstTopLengthDelimited(inner)
	if len(top) == 0 {
		return nil
	}
	var best []byte
	off := 0
	for off < len(top) {
		key, n := readVarint(top[off:])
		if n <= 0 {
			break
		}
		off += n
		fieldNum := int(key >> 3)
		wireType := int(key & 7)
		switch wireType {
		case 0:
			_, vn := readVarint(top[off:])
			if vn <= 0 {
				return best
			}
			off += vn
		case 1:
			if off+8 > len(top) {
				return best
			}
			off += 8
		case 5:
			if off+4 > len(top) {
				return best
			}
			off += 4
		case 2:
			ln, vn := readVarint(top[off:])
			if vn <= 0 || off+vn+int(ln) > len(top) {
				return best
			}
			off += vn
			payload := top[off : off+int(ln)]
			off += int(ln)
			if fieldNum == 2 && len(payload) >= len(best) {
				best = payload
			}
		default:
			return best
		}
	}
	return best
}

func extractUserTextFromPromptPack(field2 []byte, insp *BidiAppendInspect) (text, source string) {
	if len(field2) > 0 {
		if joined := tipTapTexts(field2); joined != "" {
			return joined, "tiptap_text"
		}
		// Prefer short conversational printable runs inside field 2.
		raw := extractPrintableStringsFlexible(field2, 12, 240, 2)
		for _, s := range raw {
			if looksLikeUserPromptHint(s) && !looksLikeSectionOrModelLabel(s) {
				return strings.TrimSpace(s), "printable_hint"
			}
		}
	}
	if insp != nil {
		for _, s := range insp.Strings {
			if looksLikeUserPromptHint(s) && !looksLikeSectionOrModelLabel(s) &&
				!strings.ContainsAny(s, `/\`) {
				return strings.TrimSpace(s), "section_fallback"
			}
		}
		if h := strings.TrimSpace(insp.PrintableHint); h != "" && looksLikeUserPromptHint(h) {
			return h, "printable_hint"
		}
	}
	return "", ""
}

func tipTapTexts(b []byte) string {
	matches := tipTapTextRE.FindAllSubmatch(b, 64)
	if len(matches) == 0 {
		// Try to locate a JSON doc object and walk it.
		if s := tipTapFromJSONDoc(b); s != "" {
			return s
		}
		return ""
	}
	var parts []string
	seen := map[string]struct{}{}
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		raw := string(m[1])
		unesc := unescapeJSONString(raw)
		unesc = strings.TrimSpace(unesc)
		if unesc == "" || len(unesc) > 4000 {
			continue
		}
		if _, ok := seen[unesc]; ok {
			continue
		}
		seen[unesc] = struct{}{}
		parts = append(parts, unesc)
	}
	return LatestUserTurnText(parts)
}

// LatestUserTurnText selects the current user prompt from TipTap text nodes.
// Joining the full history (prior Agent/Composer replies) contaminates Path B
// CompleteLocal and can false-trigger slash overrides buried in older turns.
func LatestUserTurnText(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	// Prefer the latest segment that carries an explicit routing slash command,
	// including a short preceding chrome crumb ("Composer") when present.
	for i := len(parts) - 1; i >= 0; i-- {
		if !containsRoutingSlash(parts[i]) {
			continue
		}
		start := i
		if start > 0 {
			prev := strings.TrimSpace(parts[start-1])
			if prev != "" && len(prev) <= 48 && !looksLikeAssistantNoise(prev) {
				start--
			}
		}
		return strings.TrimSpace(strings.Join(parts[start:], "\n"))
	}
	// No slash command: use the last non-noise node only (not the full join).
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if p == "" || looksLikeAssistantNoise(p) {
			continue
		}
		return p
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

var routingSlashRE = regexp.MustCompile(`(?i)(?:^|[\s\n])/(?:cloud|heavy|local|fast)(?:\s|$|[.,:;!?)\]]|"')`)

func containsRoutingSlash(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if routingSlashRE.MatchString(" " + s) {
		return true
	}
	// Prefix form: "/cloud …"
	lower := strings.ToLower(s)
	for _, cmd := range []string{"/cloud", "/heavy", "/local", "/fast"} {
		if strings.HasPrefix(lower, cmd) {
			if len(s) == len(cmd) {
				return true
			}
			r := rune(s[len(cmd)])
			return unicode.IsSpace(r) || strings.ContainsRune(".,:;!?)]}\"'`", r)
		}
	}
	return false
}

func looksLikeAssistantNoise(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return true
	}
	switch {
	case strings.HasPrefix(lower, "hi — i'm auto"),
		strings.HasPrefix(lower, "hi - i'm auto"),
		strings.HasPrefix(lower, "hi, i'm auto"),
		strings.Contains(lower, "i'm auto, an agent router"),
		strings.Contains(lower, "i’m auto, an agent router"),
		strings.Contains(lower, "path b local fulfillment"),
		strings.Contains(lower, "hello! it means"),
		strings.Contains(lower, "glad to say hello from the subagent"):
		return true
	default:
		return false
	}
}

func tipTapFromJSONDoc(b []byte) string {
	// Prefer the last TipTap doc in the buffer (newest turn), not the first.
	var lastParts []string
	search := b
	offset := 0
	for {
		rel := bytes.Index(search, []byte(`"type":"doc"`))
		spaced := false
		if rel < 0 {
			rel = bytes.Index(search, []byte(`"type": "doc"`))
			spaced = true
		}
		if rel < 0 {
			break
		}
		abs := offset + rel
		start := abs
		for start > 0 && b[start] != '{' {
			start--
		}
		if b[start] != '{' {
			step := rel + 1
			if spaced {
				step = rel + 1
			}
			offset += step
			if offset >= len(b) {
				break
			}
			search = b[offset:]
			continue
		}
		end := findJSONObjectEnd(b[start:])
		if end <= 0 {
			offset = abs + 1
			if offset >= len(b) {
				break
			}
			search = b[offset:]
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(b[start:start+end], &doc); err == nil {
			var parts []string
			walkTipTap(doc, &parts)
			if len(parts) > 0 {
				lastParts = parts
			}
		}
		offset = start + end
		if offset >= len(b) {
			break
		}
		search = b[offset:]
	}
	if len(lastParts) == 0 {
		return ""
	}
	return LatestUserTurnText(lastParts)
}

func findJSONObjectEnd(b []byte) int {
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return 0
}

func walkTipTap(v any, parts *[]string) {
	switch t := v.(type) {
	case map[string]any:
		typ, _ := t["type"].(string)
		if typ == "text" {
			if s, ok := t["text"].(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					*parts = append(*parts, s)
				}
			}
		}
		for _, child := range t {
			walkTipTap(child, parts)
		}
	case []any:
		for _, child := range t {
			walkTipTap(child, parts)
		}
	}
}

func unescapeJSONString(s string) string {
	// Wrap as JSON string for strconv-quality unescape.
	var out string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &out); err != nil {
		return s
	}
	return out
}

func guessModelHint(insp *BidiAppendInspect) string {
	if insp == nil {
		return ""
	}
	candidates := append([]string{}, insp.Strings...)
	if insp.PrintableHint != "" {
		candidates = append(candidates, insp.PrintableHint)
	}
	for _, s := range candidates {
		if m := modelLikeToken(s); m != "" {
			return m
		}
	}
	return ""
}

func modelLikeToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 80 {
		return ""
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "gemini-"),
		strings.HasPrefix(lower, "claude-"),
		strings.HasPrefix(lower, "gpt-"),
		strings.HasPrefix(lower, "o1"),
		strings.HasPrefix(lower, "o3"),
		strings.HasPrefix(lower, "o4"),
		strings.Contains(lower, "composer"):
		// Single token — reject paths / sentences.
		for _, r := range s {
			if unicode.IsSpace(r) || r == '/' || r == '\\' {
				return ""
			}
		}
		return s
	default:
		return ""
	}
}

// InspectRunSSEResponse summarizes the first bytes of a RunSSE response stream
// (Connect frames + redacted printable hint). Deep classify fills Frames.
type RunSSEResponseInspect struct {
	StatusCode    int               `json:"status_code,omitempty"`
	BodyBytes     int               `json:"body_bytes,omitempty"`
	PeekBytes     int               `json:"peek_bytes,omitempty"`
	FrameCount    int               `json:"frame_count,omitempty"`
	FrameSizes    []int             `json:"frame_sizes,omitempty"`
	FrameFlags    []uint8           `json:"frame_flags,omitempty"`
	ProtoWire     string            `json:"proto_wire,omitempty"`
	PrintableHint string            `json:"printable_hint,omitempty"`
	Strings       []string          `json:"strings,omitempty"`
	ContentType   string            `json:"content_type,omitempty"`
	Frames        []RunSSEFrameInfo `json:"frames,omitempty"`
}

const maxRunSSEResponsePeek = 64 << 10 // 64 KiB

// MaxRunSSEResponsePeek is the export alias for MITM response tee sizing.
const MaxRunSSEResponsePeek = maxRunSSEResponsePeek

// InspectRunSSEResponseBody peeks a RunSSE response body prefix (may be truncated)
// and deeply classifies frames (gunzip + AgentServerMessage oneofs).
func InspectRunSSEResponseBody(peek []byte, statusCode int, contentType string) *RunSSEResponseInspect {
	return InspectRunSSEResponseDeep(peek, statusCode, contentType)
}

// inspectRunSSEResponseShallow is the pre-deep scan used as a base by Deep.
func inspectRunSSEResponseShallow(peek []byte, statusCode int, contentType string) *RunSSEResponseInspect {
	out := &RunSSEResponseInspect{
		StatusCode:  statusCode,
		BodyBytes:   len(peek),
		PeekBytes:   len(peek),
		ContentType: contentType,
	}
	if len(peek) == 0 {
		return out
	}
	frames := scanConnectFramesLocal(peek)
	if len(frames) > 0 {
		out.FrameCount = len(frames)
		out.FrameSizes = make([]int, len(frames))
		out.FrameFlags = make([]uint8, len(frames))
		for i, f := range frames {
			out.FrameSizes[i] = f.size
			out.FrameFlags[i] = f.flags
		}
		if frames[0].size > 0 && len(peek) >= 5+frames[0].size {
			payload := peek[5 : 5+frames[0].size]
			out.ProtoWire = summarizeWire(payload, 24)
			out.Strings = extractPrintableStrings(payload, 6, 96)
			out.PrintableHint = pickHint(out.Strings, printableHint(payload, 96))
		}
	} else {
		out.ProtoWire = summarizeWire(peek, 24)
		out.Strings = extractPrintableStrings(peek, 6, 96)
		out.PrintableHint = pickHint(out.Strings, printableHint(peek, 96))
	}
	return out
}
