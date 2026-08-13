// Package cursorrpc decodes and encodes the Cursor aiserver.v1 and agent.v1
// Connect/protobuf RPCs that Glider needs to read and answer.
//
// Both protocols are handled on the wire, field by field, with no generated
// types and no protoc in the build. Refer to aiserver_wire.go for the chat
// family and to runsse_codec.go for the agent family.
package cursorrpc

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Connect envelope flags (same bit layout as connectrpc.com/connect).
const (
	flagCompressed = 0b00000001
	flagEndStream  = 0b00000010
)

// ReadFirstEnvelope reads exactly one Connect envelope from r, which is a
// header of 5 bytes and then the data. It reads nothing more from r.
//
// A live test found the root cause on 2026-07-29, with an isolated trace of the
// HTTP/2 frames from tools/wirecapture.
//
// agent.v1.AgentService/Run is a true RPC that streams in two directions:
// `rpc Run(stream AgentClientMessage) returns (stream AgentServerMessage)`.
//
// The true client of cursor-agent keeps the send side of its own request stream
// open for approximately 30 seconds. It sends a small envelope of approximately
// 7 bytes each 5 seconds, to keep the connection alive. Then it sends
// END_STREAM. It does this also for one turn with no console, from `-p`.
//
// io.ReadAll(r.Body) blocks until END_STREAM arrives. Therefore the server waits
// for the full 30 seconds, with no message, before it can write one byte back.
//
// cursor-agent receives nothing in that period. Therefore it sends
// RST_STREAM(ErrCode=CANCEL) at approximately the same moment that it completes
// its own send side. Each write after that fails against a stream that is
// already reset.
//
// That is the true mechanism of the "http2: stream closed" errors. A person
// tried some earlier corrections with a smaller scope, on the shape of the
// encoder frames and on the sequence of the writes. Those corrections were
// true, but they were never the cause.
func ReadFirstEnvelope(r io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("read connect envelope header: %w", err)
	}
	n := binary.BigEndian.Uint32(header[1:5])
	out := make([]byte, 5+int(n))
	copy(out, header[:])
	if _, err := io.ReadFull(r, out[5:]); err != nil {
		return nil, fmt.Errorf("read connect envelope payload (%d bytes): %w", n, err)
	}
	return out, nil
}

// UnwrapProtoPayload returns the protobuf message bytes from a Connect/gRPC-style
// HTTP body. Unary Connect uses raw protobuf; some clients wrap a single envelope.
func UnwrapProtoPayload(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	if payload, ok, err := trySingleEnvelope(body); ok {
		return payload, err
	}
	return body, nil
}

func trySingleEnvelope(body []byte) ([]byte, bool, error) {
	if len(body) < 5 {
		return nil, false, nil
	}
	flags := body[0]
	// Only treat as envelope when reserved bits are clear and length fits exactly
	// one frame (unary-as-framed) or the first frame of a multi-frame body.
	if flags&^(flagCompressed|flagEndStream) != 0 {
		return nil, false, nil
	}
	n := binary.BigEndian.Uint32(body[1:5])
	if int(n)+5 > len(body) {
		return nil, false, nil
	}
	// Prefer envelope unwrap when the frame consumes the whole body, or when
	// Content-Type implies Connect framing (caller may still pass raw proto that
	// coincidentally looks framed — then UnmarshalProto will fail and fall back).
	if int(n)+5 != len(body) && int(n)+5 < len(body) {
		// Multi-frame: use first data frame only for request decode.
		if flags&flagEndStream != 0 {
			return nil, false, nil
		}
	}
	data := body[5 : 5+int(n)]
	if flags&flagCompressed != 0 {
		gr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, true, fmt.Errorf("gzip envelope: %w", err)
		}
		defer gr.Close()
		out, err := io.ReadAll(gr)
		return out, true, err
	}
	// Heuristic: if unframed proto.Unmarshal would also work on body, prefer raw
	// when envelope length does not match whole body and first bytes look like
	// typical protobuf field tags — handled by UnmarshalProto fallback.
	if int(n)+5 == len(body) {
		return data, true, nil
	}
	return data, true, nil
}

// WriteConnectFrame writes one Connect/gRPC data envelope around an already
// encoded protobuf message. Callers build that message with the field
// helpers in this package — protoBytesField and the Encode* functions — so
// nothing here needs a generated type.
func WriteConnectFrame(w io.Writer, raw []byte) error {
	return writeEnvelope(w, 0, raw)
}

// WriteConnectEndStream writes the Connect end-stream envelope (empty success JSON).
func WriteConnectEndStream(w io.Writer) error {
	return writeEnvelope(w, flagEndStream, []byte("{}"))
}

// WriteConnectEndStreamError writes a Connect end-stream error payload.
func WriteConnectEndStreamError(w io.Writer, code, message string) error {
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
	return writeEnvelope(w, flagEndStream, payload)
}

func writeEnvelope(w io.Writer, flags uint8, data []byte) error {
	var prefix [5]byte
	prefix[0] = flags
	binary.BigEndian.PutUint32(prefix[1:], uint32(len(data)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// LooksLikeConnectProto reports whether Content-Type is Connect/protobuf (not JSON).
func LooksLikeConnectProto(contentType string) bool {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "application/proto"),
		strings.Contains(ct, "application/connect+proto"),
		strings.Contains(ct, "application/grpc"),
		strings.Contains(ct, "application/grpc-web"):
		return true
	default:
		return false
	}
}
