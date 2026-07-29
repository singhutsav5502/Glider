package cursorrpc

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Payload encoding of BidiAppend outer field 1 (after optional HTTP gunzip).
const (
	PayloadKindEmpty     = "empty"
	PayloadKindASCIIHex  = "ascii_hex"  // common: hex digits of an inner proto
	PayloadKindRawProto  = "raw_proto"  // raw protobuf bytes (no hex wrap)
	PayloadKindGzipMagic = "gzip_magic" // rare: gzip blob inside field 1
	PayloadKindUnknown   = "unknown"
)

// RoleGuess is a best-effort label for R&D only — not a schema claim.
const (
	RoleGuessAckEmpty        = "ack_or_empty"     // tiny/empty inner (fields 3/5/7 small)
	RoleGuessContentBlob     = "content_blob"     // larger field-2 inner (tool/FS/context)
	RoleGuessContextEnvelope = "context_envelope" // large inner top field 1 (prompt/context pack)
	RoleGuessControlSmall    = "control_small"    // small non-empty non-content
	RoleGuessUnknown         = "unknown"
)

// NestedKindGuess labels recurring shapes under inner top LD (R&D only).
const (
	NestedKindToolText        = "tool_text"        // nested 1:varint,14:ld(N),39:varint — shell/tool stdout
	NestedKindPathBlob        = "path_blob"        // nested 28:ld / path-heavy alternate
	NestedKindContextSections = "context_sections" // under top=1: 1:ld + 2:ld + many 14:ld labels
	NestedKindGeneric         = "nested_ld"        // other nested LD content
	NestedKindNone            = ""
)

// BidiAppendInspect is a best-effort envelope + inner view of BidiService/BidiAppend
// request bodies observed on modern Cursor (api2). Not a full schema decode —
// origin passthrough remains required until a verified response codec exists.
//
// Live dumps (Cursor 3.12 / 2026-07) show a recurring outer shape:
//
//	field 1 length-delimited — append payload (often ASCII-hex of an inner proto)
//	field 2 length-delimited — nested { field 1 = request UUID }
//	field 3 varint — sequence / append index
//
// After hex unwrap (or raw), the inner top-level field number appears to act as a
// message-type discriminator (hypotheses — see RoleGuess / findings doc):
//
//	1:ld(N) — context envelope (model + section labels + large nested field 2)
//	2:ld(N) — content (paths, tool results, context); N varies widely
//	3:ld(4) / 5:ld(4) — tiny control
//	7:ld(0) — empty ack-like marker
type BidiAppendInspect struct {
	RequestID       string   `json:"request_id,omitempty"`
	Seq             uint64   `json:"seq,omitempty"`
	HasSeq          bool     `json:"has_seq,omitempty"`
	PayloadBytes    int      `json:"payload_bytes,omitempty"`
	PayloadKind     string   `json:"payload_kind,omitempty"`
	PayloadIsHex    bool     `json:"payload_is_hex,omitempty"` // legacy alias of ascii_hex
	InnerBytes      int      `json:"inner_bytes,omitempty"`
	InnerTopField   int      `json:"inner_top_field,omitempty"`
	InnerWire       string   `json:"inner_wire,omitempty"`
	InnerNestedWire string   `json:"inner_nested_wire,omitempty"`
	NestedKindGuess string   `json:"nested_kind_guess,omitempty"` // tool_text | path_blob | …
	OuterWire       string   `json:"outer_wire,omitempty"`
	RoleGuess       string   `json:"role_guess,omitempty"`
	PrintableHint   string   `json:"printable_hint,omitempty"` // short safe hint, never full body
	Strings         []string `json:"strings,omitempty"`        // redacted printable extracts (capped)
}

// RunSSEInspect is a best-effort view of AgentService/RunSSE request bodies.
// Observed shape: Connect frame (flags=0) wrapping protobuf field 1 = run/request UUID.
// Response stream is not inspected here (request Observe only).
type RunSSEInspect struct {
	RequestID  string `json:"request_id,omitempty"`
	FrameCount int    `json:"frame_count,omitempty"`
	FrameSize  int    `json:"frame_size,omitempty"`
	FrameFlags uint8  `json:"frame_flags,omitempty"`
	ProtoWire  string `json:"proto_wire,omitempty"`
	RoleGuess  string `json:"role_guess,omitempty"`
}

const (
	maxInspectStrings   = 8
	maxInspectStringLen = 96
	maxNestedWireFields = 16
	maxGunzipField1     = 64 << 10
)

// InspectBidiAppend parses the outer BidiAppend envelope and peeks the field-1
// inner payload without claiming fulfillability.
// body may be raw protobuf (application/proto) — not Connect-framed.
// Caller should pass HTTP-gunzipped bytes when Content-Encoding was gzip.
func InspectBidiAppend(body []byte) (*BidiAppendInspect, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	out := &BidiAppendInspect{
		OuterWire: summarizeWire(body, 24),
		RoleGuess: RoleGuessUnknown,
	}

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
		case 0: // varint
			v, vn := readVarint(body[off:])
			if vn <= 0 {
				return out, nil
			}
			off += vn
			if fieldNum == 3 {
				out.Seq = v
				out.HasSeq = true
			}
		case 2: // length-delimited
			ln, vn := readVarint(body[off:])
			if vn <= 0 || ln > uint64(len(body)-off-vn) {
				return out, nil
			}
			off += vn
			chunk := body[off : off+int(ln)]
			off += int(ln)
			switch fieldNum {
			case 1:
				inspectField1Payload(out, chunk)
			case 2:
				if id := nestedStringField1(chunk); id != "" {
					out.RequestID = id
				}
			}
		case 1:
			if off+8 > len(body) {
				return out, nil
			}
			off += 8
		case 5:
			if off+4 > len(body) {
				return out, nil
			}
			off += 4
		default:
			return out, nil
		}
	}
	return out, nil
}

func inspectField1Payload(out *BidiAppendInspect, chunk []byte) {
	out.PayloadBytes = len(chunk)
	if len(chunk) == 0 {
		out.PayloadKind = PayloadKindEmpty
		out.RoleGuess = RoleGuessAckEmpty
		return
	}

	inner := chunk
	switch {
	case isASCIIHex(chunk):
		out.PayloadKind = PayloadKindASCIIHex
		out.PayloadIsHex = true
		decoded, err := hex.DecodeString(string(chunk))
		if err != nil {
			out.PayloadKind = PayloadKindUnknown
			return
		}
		inner = decoded
		out.InnerBytes = len(decoded)
	case len(chunk) >= 2 && chunk[0] == 0x1f && chunk[1] == 0x8b:
		out.PayloadKind = PayloadKindGzipMagic
		plain, err := gunzipCapped(chunk, maxGunzipField1)
		if err != nil || len(plain) == 0 {
			out.PrintableHint = printableHint(chunk, 96)
			return
		}
		inner = plain
		out.InnerBytes = len(plain)
	case looksLikeProto(chunk):
		out.PayloadKind = PayloadKindRawProto
		out.InnerBytes = len(chunk)
	default:
		out.PayloadKind = PayloadKindUnknown
		out.PrintableHint = printableHint(chunk, 96)
		return
	}

	out.InnerWire = summarizeWire(inner, 24)
	out.InnerTopField = firstFieldNum(inner)
	out.InnerNestedWire = summarizeNestedTopLD(inner, maxNestedWireFields)
	out.NestedKindGuess = guessNestedKind(out.InnerTopField, out.InnerNestedWire)
	// Prefer strings from nested content field 14 (tool stdout) when present —
	// raw whole-inner scan often prefixes paths with protobuf noise bytes.
	if preferred := extractNestedContentStrings(inner, maxInspectStrings, maxInspectStringLen); len(preferred) > 0 {
		out.Strings = preferred
	} else {
		out.Strings = extractPrintableStrings(inner, maxInspectStrings, maxInspectStringLen)
	}
	out.PrintableHint = pickHint(out.Strings, printableHint(inner, 96))
	out.RoleGuess = guessInnerRole(out.InnerTopField, out.InnerBytes, out.InnerWire)
}

// InspectRunSSE parses a Connect-framed AgentService/RunSSE request body.
func InspectRunSSE(body []byte) (*RunSSEInspect, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	out := &RunSSEInspect{RoleGuess: "run_id_request"}
	frames := scanConnectFramesLocal(body)
	payload := body
	if len(frames) > 0 {
		out.FrameCount = len(frames)
		out.FrameSize = frames[0].size
		out.FrameFlags = frames[0].flags
		if frames[0].size > 0 && len(body) >= 5+frames[0].size {
			payload = body[5 : 5+frames[0].size]
		}
	}
	out.ProtoWire = summarizeWire(payload, 16)
	if id := nestedStringField1(payload); id != "" {
		out.RequestID = id
	} else if id := firstStringField(payload, 1); id != "" {
		out.RequestID = id
	}
	return out, nil
}

// IsBidiAppendPath reports BidiService/BidiAppend (case-insensitive).
func IsBidiAppendPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "bidiappend")
}

// IsRunSSEPath reports agent.v1.AgentService/RunSSE (case-insensitive).
func IsRunSSEPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "runsse") || strings.Contains(lower, "agent.v1.agentservice/run")
}

// IsAgentServiceRunPath reports the bare agent.v1.AgentService/Run RPC
// specifically (case-insensitive) — distinct from IsRunSSEPath, which
// (deliberately, for response-shape classification) also matches this same
// path since Run and RunSSE return the identical AgentServerMessage stream
// type. This narrower check exists because Run and RunSSE differ on the
// *request* side in a way that matters for how the request body must be
// read: Run is genuine bidi-streaming (`rpc Run(stream AgentClientMessage)
// returns (stream AgentServerMessage)`) — see ReadFirstEnvelope's own doc
// comment for why that means the request body must not be read via a
// blocking io.ReadAll — while RunSSE takes a single unary BidiRequestId,
// with no equivalent concern.
func IsAgentServiceRunPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, "agent.v1.agentservice/run")
}

func guessInnerRole(topField, innerBytes int, innerWire string) string {
	switch {
	case innerBytes == 0:
		return RoleGuessAckEmpty
	case topField == 7 && strings.Contains(innerWire, "7:ld(0)"):
		return RoleGuessAckEmpty
	case topField == 3 && innerBytes <= 8:
		return RoleGuessAckEmpty
	case topField == 5 && innerBytes <= 8:
		return RoleGuessControlSmall
	case topField == 1 && innerBytes >= 256:
		return RoleGuessContextEnvelope
	case topField == 2 && innerBytes >= 16:
		return RoleGuessContentBlob
	case topField == 2:
		return RoleGuessControlSmall
	case innerBytes <= 8:
		return RoleGuessAckEmpty
	default:
		return RoleGuessUnknown
	}
}

func guessNestedKind(topField int, nestedWire string) string {
	if nestedWire == "" {
		return NestedKindNone
	}
	if topField == 1 {
		hasBlob := strings.Contains(nestedWire, "2:ld(")
		hasLabels := strings.Contains(nestedWire, "14:ld(")
		if hasBlob && hasLabels {
			return NestedKindContextSections
		}
		if hasBlob || strings.Contains(nestedWire, ":ld(") {
			return NestedKindGeneric
		}
		return NestedKindNone
	}
	if topField != 2 {
		return NestedKindNone
	}
	switch {
	case strings.Contains(nestedWire, "14:ld(") && strings.Contains(nestedWire, "1:varint"):
		return NestedKindToolText
	case strings.Contains(nestedWire, "28:ld("):
		return NestedKindPathBlob
	case strings.Contains(nestedWire, ":ld("):
		return NestedKindGeneric
	default:
		return NestedKindNone
	}
}

// extractNestedContentStrings walks the first top-level LD payload and prefers
// nested field 14 (tool text) or field 28 (path blob) printable runs.
// For inner top field 1 (context envelope), collects short field-14 section
// labels plus redacted user-visible hints from nested field 2.
func extractNestedContentStrings(inner []byte, maxCount, maxLen int) []string {
	chunk := firstTopLengthDelimited(inner)
	if len(chunk) == 0 {
		return nil
	}
	top := firstFieldNum(inner)
	var field14, field28, field2 []byte
	var sectionLabels []string
	off := 0
	for off < len(chunk) {
		key, n := readVarint(chunk[off:])
		if n <= 0 {
			break
		}
		off += n
		fieldNum := int(key >> 3)
		wireType := int(key & 7)
		switch wireType {
		case 0:
			_, vn := readVarint(chunk[off:])
			if vn <= 0 {
				return nil
			}
			off += vn
		case 1:
			if off+8 > len(chunk) {
				return nil
			}
			off += 8
		case 5:
			if off+4 > len(chunk) {
				return nil
			}
			off += 4
		case 2:
			ln, vn := readVarint(chunk[off:])
			if vn <= 0 || off+vn+int(ln) > len(chunk) {
				return nil
			}
			off += vn
			payload := chunk[off : off+int(ln)]
			off += int(ln)
			switch fieldNum {
			case 2:
				// Keep the largest field-2 blob (conversation / prompt pack).
				if len(payload) >= len(field2) {
					field2 = payload
				}
			case 14:
				field14 = payload
				if top == 1 {
					if label := shortSectionLabel(payload); label != "" {
						sectionLabels = append(sectionLabels, label)
					}
				}
			case 28:
				field28 = payload
			}
		default:
			return nil
		}
	}

	if top == 1 {
		return mergeUniqueStrings(sectionLabels, extractUserVisiblePromptHints(field2, maxCount, maxLen), maxCount)
	}

	prefer := field14
	if len(prefer) == 0 {
		prefer = field28
	}
	if len(prefer) == 0 {
		return nil
	}
	return extractPrintableStrings(prefer, maxCount, maxLen)
}

// shortSectionLabel returns a redacted printable label from a small LD (e.g. system_prompt).
func shortSectionLabel(b []byte) string {
	if len(b) < 4 || len(b) > 120 {
		return ""
	}
	if !isPrintableASCII(string(b)) {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 4 {
		return ""
	}
	if redacted, skip := redactSecretString(s); skip {
		return ""
	} else {
		return redacted
	}
}

// extractUserVisiblePromptHints pulls short conversational / prompt-like runs from a
// context field-2 blob. Prefers brief user text over long paths; always redacts secrets.
func extractUserVisiblePromptHints(b []byte, maxCount, maxLen int) []string {
	if len(b) == 0 || maxCount <= 0 {
		return nil
	}
	if maxLen <= 0 {
		maxLen = 96
	}
	// Allow short prompts ("hi!") — general extract requires len>=8.
	raw := extractPrintableStringsFlexible(b, maxCount*3, maxLen, 2)
	var preferred, rest []string
	for _, s := range raw {
		if looksLikeUserPromptHint(s) {
			preferred = append(preferred, s)
		} else if looksLikeSectionOrModelLabel(s) {
			preferred = append(preferred, s)
		} else {
			rest = append(rest, s)
		}
	}
	out := append(preferred, rest...)
	if len(out) > maxCount {
		out = out[:maxCount]
	}
	return out
}

func looksLikeUserPromptHint(s string) bool {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 2 || len(trimmed) > 160 {
		return false
	}
	if looksLikeUUID(trimmed) || looksLikeLongHexKey(trimmed) {
		return false
	}
	if strings.ContainsAny(trimmed, `/\`) && (strings.Contains(trimmed, ":\\") || strings.HasPrefix(trimmed, "/") || strings.Contains(trimmed, "Users\\")) {
		return false
	}
	letters := 0
	for _, r := range trimmed {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters >= 1
}

func looksLikeSectionOrModelLabel(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(lower, "prompt"), strings.Contains(lower, "tool"),
		strings.Contains(lower, "subagent"), strings.Contains(lower, "conversation"),
		strings.Contains(lower, "gemini"), strings.Contains(lower, "claude"),
		strings.Contains(lower, "gpt"):
		return true
	default:
		return false
	}
}

func mergeUniqueStrings(a, b []string, maxCount int) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	var out []string
	add := func(list []string) {
		for _, s := range list {
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
			if len(out) >= maxCount {
				return
			}
		}
	}
	add(a)
	add(b)
	return out
}

func firstTopLengthDelimited(b []byte) []byte {
	off := 0
	for off < len(b) {
		key, n := readVarint(b[off:])
		if n <= 0 {
			return nil
		}
		off += n
		wireType := int(key & 7)
		switch wireType {
		case 0:
			_, vn := readVarint(b[off:])
			if vn <= 0 {
				return nil
			}
			off += vn
		case 1:
			off += 8
		case 5:
			off += 4
		case 2:
			ln, vn := readVarint(b[off:])
			if vn <= 0 || off+vn+int(ln) > len(b) {
				return nil
			}
			off += vn
			return b[off : off+int(ln)]
		default:
			return nil
		}
	}
	return nil
}

func pickHint(stringsOut []string, fallback string) string {
	for _, s := range stringsOut {
		if len(s) >= 8 {
			return s
		}
	}
	return fallback
}

func firstFieldNum(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	key, n := readVarint(b)
	if n <= 0 {
		return 0
	}
	return int(key >> 3)
}

// summarizeNestedTopLD walks the first length-delimited field's payload for a nested tag summary.
func summarizeNestedTopLD(b []byte, maxFields int) string {
	off := 0
	for off < len(b) {
		key, n := readVarint(b[off:])
		if n <= 0 {
			return ""
		}
		off += n
		wireType := int(key & 7)
		switch wireType {
		case 0:
			_, vn := readVarint(b[off:])
			if vn <= 0 {
				return ""
			}
			off += vn
		case 1:
			off += 8
		case 5:
			off += 4
		case 2:
			ln, vn := readVarint(b[off:])
			if vn <= 0 || off+vn+int(ln) > len(b) {
				return ""
			}
			off += vn
			chunk := b[off : off+int(ln)]
			return summarizeWire(chunk, maxFields)
		default:
			return ""
		}
	}
	return ""
}

func nestedStringField1(b []byte) string {
	off := 0
	for off < len(b) {
		key, n := readVarint(b[off:])
		if n <= 0 {
			return ""
		}
		off += n
		fieldNum := int(key >> 3)
		wireType := int(key & 7)
		if wireType != 2 {
			// skip non-ld and continue when possible
			switch wireType {
			case 0:
				_, vn := readVarint(b[off:])
				if vn <= 0 {
					return ""
				}
				off += vn
			case 1:
				off += 8
			case 5:
				off += 4
			default:
				return ""
			}
			continue
		}
		ln, vn := readVarint(b[off:])
		if vn <= 0 || ln > uint64(len(b)-off-vn) {
			return ""
		}
		off += vn
		chunk := b[off : off+int(ln)]
		off += int(ln)
		if fieldNum == 1 {
			s := string(chunk)
			if looksLikeUUID(s) || isPrintableASCII(s) {
				return s
			}
			return ""
		}
	}
	return ""
}

func firstStringField(b []byte, wantField int) string {
	off := 0
	for off < len(b) {
		key, n := readVarint(b[off:])
		if n <= 0 {
			return ""
		}
		off += n
		fieldNum := int(key >> 3)
		wireType := int(key & 7)
		switch wireType {
		case 0:
			_, vn := readVarint(b[off:])
			if vn <= 0 {
				return ""
			}
			off += vn
		case 1:
			if off+8 > len(b) {
				return ""
			}
			off += 8
		case 5:
			if off+4 > len(b) {
				return ""
			}
			off += 4
		case 2:
			ln, vn := readVarint(b[off:])
			if vn <= 0 || off+vn+int(ln) > len(b) {
				return ""
			}
			off += vn
			chunk := b[off : off+int(ln)]
			off += int(ln)
			if fieldNum == wantField {
				s := string(chunk)
				if looksLikeUUID(s) || isPrintableASCII(s) {
					return s
				}
			}
		default:
			return ""
		}
	}
	return ""
}

func summarizeWire(body []byte, maxFields int) string {
	if len(body) == 0 {
		return ""
	}
	if maxFields <= 0 {
		maxFields = 24
	}
	var parts []string
	off := 0
	for off < len(body) && len(parts) < maxFields {
		key, n := readVarint(body[off:])
		if n <= 0 {
			break
		}
		off += n
		fieldNum := int(key >> 3)
		wireType := int(key & 7)
		if fieldNum <= 0 {
			break
		}
		switch wireType {
		case 0:
			_, vn := readVarint(body[off:])
			if vn <= 0 {
				return strings.Join(parts, ",")
			}
			off += vn
			parts = append(parts, fmt.Sprintf("%d:varint", fieldNum))
		case 1:
			if off+8 > len(body) {
				return strings.Join(parts, ",")
			}
			off += 8
			parts = append(parts, fmt.Sprintf("%d:i64", fieldNum))
		case 2:
			ln, vn := readVarint(body[off:])
			if vn <= 0 || off+vn+int(ln) > len(body) {
				return strings.Join(parts, ",")
			}
			off += vn + int(ln)
			parts = append(parts, fmt.Sprintf("%d:ld(%d)", fieldNum, ln))
		case 5:
			if off+4 > len(body) {
				return strings.Join(parts, ",")
			}
			off += 4
			parts = append(parts, fmt.Sprintf("%d:i32", fieldNum))
		default:
			return strings.Join(parts, ",")
		}
	}
	return strings.Join(parts, ",")
}

func readVarint(b []byte) (uint64, int) {
	var x uint64
	for i := 0; i < len(b) && i < 10; i++ {
		c := b[i]
		x |= uint64(c&0x7f) << (7 * i)
		if c < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}

func isASCIIHex(b []byte) bool {
	if len(b) < 4 || len(b)%2 != 0 {
		return false
	}
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func looksLikeProto(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	key, n := readVarint(b)
	if n <= 0 {
		return false
	}
	fieldNum := int(key >> 3)
	wireType := int(key & 7)
	if fieldNum <= 0 || fieldNum > 1000 {
		return false
	}
	switch wireType {
	case 0, 1, 2, 5:
		return true
	default:
		return false
	}
}

func gunzipCapped(b []byte, capN int) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	plain, err := io.ReadAll(io.LimitReader(gr, int64(capN)+1))
	if err != nil {
		return nil, err
	}
	if len(plain) > capN {
		plain = plain[:capN]
	}
	return plain, nil
}

func isPrintableASCII(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			switch {
			case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
			default:
				return false
			}
		}
	}
	return true
}

// printableHint extracts a short run of printable bytes for R&D (paths, labels).
// Never returns the full payload; maxLen caps the hint.
func printableHint(b []byte, maxLen int) string {
	strs := extractPrintableStrings(b, 1, maxLen)
	if len(strs) == 0 {
		return ""
	}
	return strs[0]
}

// extractPrintableStrings finds printable runs, redacting secrets / tokens.
func extractPrintableStrings(b []byte, maxCount, maxLen int) []string {
	return extractPrintableStringsFlexible(b, maxCount, maxLen, 8)
}

// extractPrintableStringsFlexible is like extractPrintableStrings but with a configurable
// minimum run length (use 2–3 to surface short user prompts like "hi!").
func extractPrintableStringsFlexible(b []byte, maxCount, maxLen, minLen int) []string {
	if maxCount <= 0 {
		maxCount = 8
	}
	if maxLen <= 0 {
		maxLen = 96
	}
	if minLen <= 0 {
		minLen = 8
	}
	var out []string
	var run strings.Builder
	flush := func() {
		s := run.String()
		run.Reset()
		if len(s) < minLen {
			return
		}
		if redacted, skip := redactSecretString(s); skip {
			return
		} else {
			s = redacted
		}
		if len(s) > maxLen {
			s = s[:maxLen]
		}
		out = append(out, s)
	}
	for _, c := range b {
		if c >= 32 && c <= 126 && (unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)) ||
			c == '\\' || c == '/' || c == '.' || c == '-' || c == '_' || c == ':' ||
			c == ' ' || c == '@' || c == '+' || c == '=' || c == ',' || c == '[' || c == ']' ||
			c == '!' || c == '?' || c == '\'' || c == '"' || c == '(' || c == ')') {
			run.WriteByte(c)
			if run.Len() >= maxLen*2 {
				flush()
				if len(out) >= maxCount {
					break
				}
			}
		} else {
			flush()
			if len(out) >= maxCount {
				break
			}
		}
	}
	flush()
	if len(out) > maxCount {
		out = out[:maxCount]
	}
	return out
}

// redactSecretString returns (sanitized, skipEntirely).
func redactSecretString(s string) (string, bool) {
	lower := strings.ToLower(s)
	trimmed := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(trimmed, "eyJ"): // JWT
		return "[redacted_jwt]", false
	case strings.Contains(lower, "bearer "):
		return "[redacted_bearer]", false
	case strings.Contains(lower, "authorization"):
		return "", true
	case looksLikeLongHexKey(trimmed):
		return fmt.Sprintf("[redacted_hex len=%d]", len(trimmed)), false
	case strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey") ||
		strings.Contains(lower, "secret"):
		return "[redacted_secret]", false
	default:
		return s, false
	}
}

func looksLikeLongHexKey(s string) bool {
	if len(s) < 32 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

type connectFrameLocal struct {
	flags uint8
	size  int
}

func scanConnectFramesLocal(body []byte) []connectFrameLocal {
	if len(body) < 5 {
		return nil
	}
	var out []connectFrameLocal
	off := 0
	for off+5 <= len(body) {
		flags := body[off]
		if flags&^0b00000011 != 0 {
			return out
		}
		n := int(body[off+1])<<24 | int(body[off+2])<<16 | int(body[off+3])<<8 | int(body[off+4])
		if n < 0 || off+5+n > len(body) {
			return out
		}
		out = append(out, connectFrameLocal{flags: flags, size: n})
		off += 5 + n
		if len(out) >= 32 {
			break
		}
	}
	return out
}
