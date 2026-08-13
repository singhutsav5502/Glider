// Package summarizer adapts an inference backend to the Summarizer shape
// internal/vendors' continuity compaction expects.
//
// It lives here, not in internal/vendors, for the same reason VendorAdapter
// keeps execution behaviour out of the wire layer: vendors must not depend on
// internal/backend. vendors declares the interface it needs; cmd/glider wires
// a concrete one in at startup.
package summarizer

import (
	"context"
	"fmt"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
)

// Backend summarizes by calling one inference backend directly.
//
// Deliberately not routed through internal/router: this is Glider's own
// bookkeeping, not a user request, and putting it through the rule engine
// would let a user's routing rules silently redirect it — including onto a
// local model when the configured chain said cloud, which is the one thing
// the chain exists to control.
type Backend struct {
	Inference backend.InferenceBackend
	Model     string
	// MaxTokens bounds the reply. A summary that runs long defeats the point.
	MaxTokens int
}

// Summarize sends text as a single user message and joins the streamed reply.
func (b Backend) Summarize(ctx context.Context, text string, maxChars int) (string, error) {
	if b.Inference == nil {
		return "", fmt.Errorf("summarizer: no inference backend configured")
	}
	if strings.TrimSpace(b.Model) == "" {
		return "", fmt.Errorf("summarizer: no model configured for backend %s", b.Inference.Name())
	}

	maxTokens := b.MaxTokens
	if maxTokens <= 0 {
		// ~4 characters per token, with headroom so the model is bounded by
		// the instruction rather than cut off mid-sentence by the ceiling.
		maxTokens = maxChars/3 + 64
	}
	temp := 0.0 // a summary should not be creative

	req := &backend.CompletionRequest{
		Model:       b.Model,
		Messages:    []backend.Message{{Role: "user", Content: text}},
		Stream:      false,
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	}

	ch, err := b.Inference.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("summarizer: %s: %w", b.Inference.Name(), err)
	}

	// CompletionChunk carries no error field: a backend reports failure by
	// returning one from Complete, or by simply closing the channel. So an
	// empty result is the only failure signal available here, and it is the
	// one checked below.
	var out strings.Builder
	for chunk := range ch {
		out.WriteString(chunk.Content)
	}

	got := strings.TrimSpace(out.String())
	if got == "" {
		return "", fmt.Errorf("summarizer: %s produced no output", b.Inference.Name())
	}
	return got, nil
}
