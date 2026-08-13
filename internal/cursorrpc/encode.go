package cursorrpc

import (
	"fmt"
	"net/http"

	"github.com/glider-ai/glider/internal/backend"
)

// WriteStreamChatResponse encodes local completion chunks as a Connect
// server-stream of aiserver.v1.StreamChatResponse messages.
//
// This is the maximum-viable local fulfill path for the AiService StreamChat
// and StreamComposer families. Refer to aiserver_wire.go for the wire layout.
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
		if err := writeEnvelope(w, 0, EncodeStreamChatText(chunk.Content)); err != nil {
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

// The code returns LocalFulfillError when the harness selected a local model,
// but this package cannot safely make an Agent response that Cursor accepts
// for this RPC.
func LocalFulfillError(path string) error {
	return fmt.Errorf(
		"glider: agent RPC %s routed local but response encoding is not supported yet; "+
			"use Override OpenAI Base URL + cus- model prefix (gateway Mode A), or allow origin passthrough",
		path,
	)
}
