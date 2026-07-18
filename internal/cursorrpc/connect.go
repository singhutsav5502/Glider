// Package cursorrpc decodes/encodes Cursor aiserver.v1 Connect/protobuf chat RPCs
// using schemas from github.com/everestmz/cursor-rpc (see THIRD_PARTY.md).
package cursorrpc

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/proto"
)

// Connect envelope flags (same bit layout as connectrpc.com/connect).
const (
	flagCompressed = 0b00000001
	flagEndStream  = 0b00000010
)

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
	// when envelope length doesn't match whole body and first bytes look like
	// typical protobuf field tags — handled by UnmarshalProto fallback.
	if int(n)+5 == len(body) {
		return data, true, nil
	}
	return data, true, nil
}

// UnmarshalProto unwraps Connect framing if present, then unmarshals into msg.
func UnmarshalProto(body []byte, msg proto.Message) error {
	payload, err := UnwrapProtoPayload(body)
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(payload, msg); err != nil {
		// Retry raw body if envelope heuristic was wrong.
		if !bytes.Equal(payload, body) {
			return proto.Unmarshal(body, msg)
		}
		return err
	}
	return nil
}

// WriteConnectProtoFrame writes one Connect/gRPC data envelope.
func WriteConnectProtoFrame(w io.Writer, msg proto.Message) error {
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
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
