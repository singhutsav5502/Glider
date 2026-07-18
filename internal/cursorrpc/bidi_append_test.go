package cursorrpc_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/cursorrpc"
)

func TestInspectBidiAppendEnvelope(t *testing.T) {
	// Captured-style envelope: field1=ASCII-hex(inner field2=path), field2={id}, field3=seq
	pathBytes := []byte(`D:\repos\Glider\planning\x.md`)
	inner := append([]byte{0x12, byte(len(pathBytes))}, pathBytes...)
	innerHex := hex.EncodeToString(inner)

	var body []byte
	body = append(body, 0x0a, byte(len(innerHex)))
	body = append(body, innerHex...)
	uuid := "1607c686-9bf6-406e-ab42-903df1563aac"
	nested := append([]byte{0x0a, byte(len(uuid))}, uuid...)
	body = append(body, 0x12, byte(len(nested)))
	body = append(body, nested...)
	body = append(body, 0x18, 0x14) // seq = 20

	got, err := cursorrpc.InspectBidiAppend(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != uuid {
		t.Fatalf("request_id=%q", got.RequestID)
	}
	if !got.HasSeq || got.Seq != 20 {
		t.Fatalf("seq=%v has=%v", got.Seq, got.HasSeq)
	}
	if !got.PayloadIsHex || got.PayloadKind != cursorrpc.PayloadKindASCIIHex {
		t.Fatalf("kind=%q hex=%v", got.PayloadKind, got.PayloadIsHex)
	}
	if got.InnerBytes != len(inner) {
		t.Fatalf("inner_bytes=%d want %d", got.InnerBytes, len(inner))
	}
	if got.OuterWire == "" || got.InnerWire == "" {
		t.Fatalf("wires outer=%q inner=%q", got.OuterWire, got.InnerWire)
	}
	if got.InnerTopField != 2 {
		t.Fatalf("inner_top=%d", got.InnerTopField)
	}
	if got.RoleGuess != cursorrpc.RoleGuessContentBlob {
		t.Fatalf("role=%q", got.RoleGuess)
	}
	if !strings.Contains(got.PrintableHint, "Glider") {
		t.Fatalf("hint=%q", got.PrintableHint)
	}
	if len(got.Strings) == 0 {
		t.Fatal("expected strings")
	}
}

func TestInspectBidiAppendAckEmpty(t *testing.T) {
	// Live pattern: outer field1 hex of {7:ld(0)} = 3a 00
	inner := []byte{0x3a, 0x00}
	innerHex := hex.EncodeToString(inner)
	var body []byte
	body = append(body, 0x0a, byte(len(innerHex)))
	body = append(body, innerHex...)
	uuid := "96522c98-1a02-431a-9d8f-0dcd9c24efa5"
	nested := append([]byte{0x0a, byte(len(uuid))}, uuid...)
	body = append(body, 0x12, byte(len(nested)))
	body = append(body, nested...)
	body = append(body, 0x18, 0x5c) // seq 92

	got, err := cursorrpc.InspectBidiAppend(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.InnerTopField != 7 {
		t.Fatalf("top=%d wire=%q", got.InnerTopField, got.InnerWire)
	}
	if got.RoleGuess != cursorrpc.RoleGuessAckEmpty {
		t.Fatalf("role=%q", got.RoleGuess)
	}
}

func TestInspectBidiAppendFixtureFiles(t *testing.T) {
	dir := filepath.Join("testdata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		if !strings.HasPrefix(e.Name(), "bidi_append_") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := cursorrpc.InspectBidiAppend(raw)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if got.OuterWire == "" {
			t.Fatalf("%s: empty outer wire", e.Name())
		}
		if got.RequestID == "" && !strings.Contains(e.Name(), "noseq") {
			// fixtures always include request id except explicit ones
		}
		// Must not leak synthetic JWT if present in fixture bytes
		for _, s := range got.Strings {
			if strings.HasPrefix(s, "eyJ") {
				t.Fatalf("%s leaked jwt in strings: %q", e.Name(), s)
			}
		}
		found++
	}
	if found < 2 {
		t.Fatalf("expected >=2 bidi_append_*.bin fixtures, found %d", found)
	}
}

func TestInspectRunSSE(t *testing.T) {
	uuid := "96522c98-1a02-431a-9d8f-0dcd9c24efa5"
	proto := append([]byte{0x0a, byte(len(uuid))}, uuid...)
	// Connect frame: flags=0, big-endian size
	frame := []byte{0x00, 0x00, 0x00, 0x00, byte(len(proto))}
	body := append(frame, proto...)

	got, err := cursorrpc.InspectRunSSE(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != uuid {
		t.Fatalf("id=%q", got.RequestID)
	}
	if got.FrameCount != 1 || got.FrameSize != len(proto) {
		t.Fatalf("frames=%+v", got)
	}
	if got.ProtoWire != "1:ld(36)" {
		t.Fatalf("wire=%q", got.ProtoWire)
	}
}

func TestInspectRunSSEFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "run_sse_request.bin"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := cursorrpc.InspectRunSSE(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID == "" || got.FrameCount != 1 {
		t.Fatalf("%+v", got)
	}
	// Anonymized fixture UUID must not look like a live session secret beyond UUID shape
	if !strings.HasPrefix(got.RequestID, "00000000-") {
		t.Fatalf("fixture should use anonymized uuid, got %q", got.RequestID)
	}
}

func TestIsBidiAppendPath(t *testing.T) {
	if !cursorrpc.IsBidiAppendPath("/aiserver.v1.BidiService/BidiAppend") {
		t.Fatal("expected true")
	}
	if cursorrpc.IsBidiAppendPath("/agent.v1.AgentService/RunSSE") {
		t.Fatal("RunSSE is not BidiAppend")
	}
}

func TestIsRunSSEPath(t *testing.T) {
	if !cursorrpc.IsRunSSEPath("/agent.v1.AgentService/RunSSE") {
		t.Fatal("expected true")
	}
	if cursorrpc.IsRunSSEPath("/aiserver.v1.BidiService/BidiAppend") {
		t.Fatal("BidiAppend is not RunSSE")
	}
}

func TestInspectBidiAppendContextEnvelopePromptHints(t *testing.T) {
	// Live Capture 4 shape under inner top field 1:
	// nested 1:ld(meta), 2:ld(conversation), 14:ld(section labels)…
	meta := []byte("pad-meta-blob-xxxxxxxx")
	userPrompt := []byte("hi!")
	// Field-2 blob mixes noise + short user text (as seen in context packs).
	var conv []byte
	conv = append(conv, 0x00, 0x01, 0x02)
	conv = append(conv, userPrompt...)
	conv = append(conv, 0x00, 0x03)
	conv = append(conv, []byte(`D:\___repos\Glider\planning\x.md`)...)

	labelSys := []byte("system_prompt")
	labelTools := []byte("Tool definitions")

	var nested []byte
	nested = append(nested, 0x0a, byte(len(meta)))
	nested = append(nested, meta...)
	nested = append(nested, 0x12, byte(len(conv)))
	nested = append(nested, conv...)
	nested = append(nested, 0x20, 0x00) // 4:ld(0) via empty — use 4:varint=0 instead: tag 0x20
	// field 5:ld(36) uuid-ish pad
	uuid := "00000000-0000-4000-8000-000000000001"
	nested = append(nested, 0x2a, byte(len(uuid)))
	nested = append(nested, uuid...)
	nested = append(nested, 0x72, byte(len(labelSys)))
	nested = append(nested, labelSys...)
	nested = append(nested, 0x72, byte(len(labelTools)))
	nested = append(nested, labelTools...)

	// Make envelope large enough for context_envelope role (>=256 inner bytes).
	for len(nested) < 300 {
		pad := []byte("xxxxxxxx")
		nested = append(nested, 0x0a, byte(len(pad)))
		nested = append(nested, pad...)
	}

	inner := appendProtoLD(1, nested)
	innerHex := hex.EncodeToString(inner)
	body := appendProtoLD(1, []byte(innerHex))
	req := "b4e9eb79-a8b5-4184-a50a-526f2dbf4215"
	nid := append([]byte{0x0a, byte(len(req))}, req...)
	body = append(body, 0x12, byte(len(nid)))
	body = append(body, nid...)

	got, err := cursorrpc.InspectBidiAppend(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.InnerTopField != 1 {
		t.Fatalf("top=%d", got.InnerTopField)
	}
	if got.RoleGuess != cursorrpc.RoleGuessContextEnvelope {
		t.Fatalf("role=%q", got.RoleGuess)
	}
	if got.NestedKindGuess != cursorrpc.NestedKindContextSections {
		t.Fatalf("nested_kind=%q wire=%q", got.NestedKindGuess, got.InnerNestedWire)
	}
	joined := strings.Join(got.Strings, "|")
	if !strings.Contains(joined, "system_prompt") {
		t.Fatalf("expected section label, got %v", got.Strings)
	}
	if !strings.Contains(joined, "hi!") {
		t.Fatalf("expected user prompt hint hi!, got %v", got.Strings)
	}
	for _, s := range got.Strings {
		if strings.HasPrefix(s, "eyJ") {
			t.Fatalf("leaked jwt: %q", s)
		}
	}
}

func appendProtoLD(field int, payload []byte) []byte {
	// field<<3 | 2
	tag := byte(field<<3 | 2)
	if len(payload) < 128 {
		out := make([]byte, 0, 2+len(payload))
		out = append(out, tag, byte(len(payload)))
		return append(out, payload...)
	}
	// multi-byte length varint
	out := []byte{tag}
	n := len(payload)
	for n >= 0x80 {
		out = append(out, byte(n)|0x80)
		n >>= 7
	}
	out = append(out, byte(n))
	return append(out, payload...)
}

func TestInspectBidiAppendToolTextNested(t *testing.T) {
	// Live pattern under inner top field 2: 1:varint,14:ld(stdout),39:varint
	stdout := []byte("ProcessName : glider\nStartTime   : 7/18/2026")
	var nested []byte
	nested = append(nested, 0x08, 0x01) // 1:varint=1
	nested = append(nested, 0x72, byte(len(stdout)))
	nested = append(nested, stdout...)
	nested = append(nested, 0xb8, 0x02, 0x00) // 39:varint=0

	inner := append([]byte{0x12, byte(len(nested))}, nested...)
	innerHex := hex.EncodeToString(inner)
	body := append([]byte{0x0a, byte(len(innerHex))}, []byte(innerHex)...)
	uuid := "e6ddb0ee-15f6-4bba-98fd-df490586ffca"
	nid := append([]byte{0x0a, byte(len(uuid))}, uuid...)
	body = append(body, 0x12, byte(len(nid)))
	body = append(body, nid...)
	body = append(body, 0x18, 0x20) // seq 32

	got, err := cursorrpc.InspectBidiAppend(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.InnerTopField != 2 {
		t.Fatalf("top=%d", got.InnerTopField)
	}
	if got.NestedKindGuess != cursorrpc.NestedKindToolText {
		t.Fatalf("nested_kind=%q wire=%q", got.NestedKindGuess, got.InnerNestedWire)
	}
	if got.RoleGuess != cursorrpc.RoleGuessContentBlob {
		t.Fatalf("role=%q", got.RoleGuess)
	}
	joined := strings.Join(got.Strings, "|")
	if !strings.Contains(joined, "ProcessName") {
		t.Fatalf("expected field-14 stdout strings, got %v", got.Strings)
	}
}

func TestRedactSecretStrings(t *testing.T) {
	// Keep total hex payload < 128 bytes so a single-byte length delimiter works.
	jwt := []byte("eyJhbGciOiJIUzI1NiJ9.xx")
	path := []byte(`D:\repos\Glider\README.md`)
	inner := []byte{0x12, byte(len(jwt))}
	inner = append(inner, jwt...)
	inner = append(inner, 0x1a, byte(len(path)))
	inner = append(inner, path...)

	innerHex := hex.EncodeToString(inner)
	if len(innerHex) > 127 {
		t.Fatalf("fixture too large for single-byte ld: %d", len(innerHex))
	}
	body := append([]byte{0x0a, byte(len(innerHex))}, []byte(innerHex)...)
	got, err := cursorrpc.InspectBidiAppend(body)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.Strings, "|")
	if strings.Contains(joined, "eyJ") {
		t.Fatalf("jwt not redacted: %v", got.Strings)
	}
	if !strings.Contains(joined, "Glider") && !strings.Contains(got.PrintableHint, "Glider") {
		t.Fatalf("expected path hint, strings=%v hint=%q", got.Strings, got.PrintableHint)
	}
}
