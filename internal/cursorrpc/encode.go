package cursorrpc

import (
	"fmt"
	"net/http"

	aiserverv1 "github.com/everestmz/cursor-rpc/cursor/gen/aiserver/v1"
	"github.com/glider-ai/glider/internal/backend"
)

// WriteStreamChatResponse encodes local completion chunks as a Connect
// server-stream of aiserver.v1.StreamChatResponse messages.
//
// This is the maximum-viable local fulfill path for AiService StreamChat /
// StreamComposer families from cursor-rpc schemas.
func WriteStreamChatResponse(w http.ResponseWriter, chunks <-chan backend.CompletionChunk) error {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/connect+proto")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	for chunk := range chunks {
		if chunk.Content == "" && chunk.FinishReason == "" {
			continue
		}
		msg := &aiserverv1.StreamChatResponse{Text: chunk.Content}
		if err := WriteConnectProtoFrame(w, msg); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	return WriteConnectEndStream(w)
}

// FailClosedConnect writes an actionable Connect end-stream error so Cursor
// surfaces a clear failure instead of hanging (local chosen but cannot fulfill).
func FailClosedConnect(w http.ResponseWriter, message string) error {
	w.Header().Set("Content-Type", "application/connect+proto")
	w.WriteHeader(http.StatusOK)
	return WriteConnectEndStreamError(w, "unimplemented", message)
}

// LocalFulfillError is returned when the harness chose local but we cannot
// safely encode a Cursor-acceptable Agent response for this RPC.
func LocalFulfillError(path string) error {
	return fmt.Errorf(
		"glider: agent RPC %s routed local but response encoding is not supported yet; "+
			"use Override OpenAI Base URL + cus- model prefix (gateway Mode A), or allow origin passthrough",
		path,
	)
}
