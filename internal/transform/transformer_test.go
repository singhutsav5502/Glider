package transform_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/transform"
)

func TestTrimContextTruncateMiddle_T4_4_1(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	tr := transform.NewTransformer(config.TransformConfig{Enabled: true, TrimContext: true}, tok)

	word := "context "
	first := backend.Message{Role: "system", Content: "You are Cursor's coding assistant."}
	last := backend.Message{Role: "user", Content: "Summarize the conversation."}
	middle := make([]backend.Message, 0, 200)
	for i := 0; i < 200; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		middle = append(middle, backend.Message{
			Role:    role,
			Content: strings.Repeat(word, 120),
		})
	}

	msgs := append([]backend.Message{first}, middle...)
	msgs = append(msgs, last)
	req := &backend.CompletionRequest{Messages: msgs}

	if tok.EstimateRequestTokens(req) <= 8192 {
		t.Fatalf("test setup: request has %d tokens, need > 8192", tok.EstimateRequestTokens(req))
	}

	out := tr.TrimContext(req, 8192)

	if len(out.Messages) == 0 {
		t.Fatal("TrimContext returned no messages")
	}
	if out.Messages[0].Role != first.Role || out.Messages[0].Content != first.Content {
		t.Fatalf("first message changed: got %+v, want %+v", out.Messages[0], first)
	}
	lastOut := out.Messages[len(out.Messages)-1]
	if lastOut.Role != last.Role || lastOut.Content != last.Content {
		t.Fatalf("last message changed: got %+v, want %+v", lastOut, last)
	}
	if tok.EstimateRequestTokens(out) > 8192 {
		t.Fatalf("TrimContext tokens = %d, want <= 8192", tok.EstimateRequestTokens(out))
	}
	if len(out.Messages) >= len(req.Messages) {
		t.Fatalf("expected middle truncation, got %d messages (input %d)", len(out.Messages), len(req.Messages))
	}
}

func TestTrimContextNoOpWhenUnderLimit_T4_4_2(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	tr := transform.NewTransformer(config.TransformConfig{Enabled: true, TrimContext: true}, tok)

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: strings.Repeat("word ", 400)},
			{Role: "assistant", Content: strings.Repeat("reply ", 400)},
		},
	}

	out := tr.TrimContext(req, 8192)
	if len(out.Messages) != len(req.Messages) {
		t.Fatalf("message count = %d, want %d", len(out.Messages), len(req.Messages))
	}
	for i := range req.Messages {
		if out.Messages[i] != req.Messages[i] {
			t.Fatalf("message[%d] changed: got %+v, want %+v", i, out.Messages[i], req.Messages[i])
		}
	}
}

func TestAugmentPrependUserInstruction_T4_4_3(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	prepend := "Be concise. Respond in under 200 words."
	tr := transform.NewTransformer(config.TransformConfig{
		Enabled:        true,
		AugmentPrepend: prepend,
	}, tok)

	cursorSystem := "You are Cursor, an AI coding assistant."
	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "system", Content: cursorSystem},
			{Role: "user", Content: "Write a function."},
		},
	}

	out := tr.Augment(req)

	if len(out.Messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(out.Messages))
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != cursorSystem {
		t.Fatalf("Cursor system prompt changed: got %+v", out.Messages[0])
	}
	if out.Messages[1].Role != "system" || out.Messages[1].Content != prepend {
		t.Fatalf("augmentation message = %+v, want system %q", out.Messages[1], prepend)
	}
	if out.Messages[2].Content != req.Messages[1].Content {
		t.Fatalf("user message changed: got %+v", out.Messages[2])
	}
}

func TestTransformDisabledPassthrough_T4_4_4(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	tr := transform.NewTransformer(config.TransformConfig{}, tok)

	req := &backend.CompletionRequest{
		Model: "gpt-4",
		Messages: []backend.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "hello"},
		},
		Stream: true,
	}

	out := tr.Apply(req, 8192)
	if out.Model != req.Model || out.Stream != req.Stream {
		t.Fatalf("Apply changed top-level fields: got model=%q stream=%v", out.Model, out.Stream)
	}
	if len(out.Messages) != len(req.Messages) {
		t.Fatalf("message count = %d, want %d", len(out.Messages), len(req.Messages))
	}
	for i := range req.Messages {
		if out.Messages[i] != req.Messages[i] {
			t.Fatalf("message[%d] changed: got %+v, want %+v", i, out.Messages[i], req.Messages[i])
		}
	}
}

func TestSystemPromptPreserved_T4_4_5(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	prepend := "Be concise."
	tr := transform.NewTransformer(config.TransformConfig{
		Enabled:        true,
		TrimContext:    true,
		AugmentPrepend: prepend,
	}, tok)

	cursorSystem := "You are Cursor. Format responses for the IDE."
	word := "context "
	middle := make([]backend.Message, 0, 200)
	for i := 0; i < 200; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		middle = append(middle, backend.Message{
			Role:    role,
			Content: strings.Repeat(word, 120),
		})
	}
	msgs := []backend.Message{
		{Role: "system", Content: cursorSystem},
	}
	msgs = append(msgs, middle...)
	msgs = append(msgs, backend.Message{Role: "user", Content: "Continue."})

	req := &backend.CompletionRequest{Messages: msgs}
	out := tr.Apply(req, 8192)

	found := false
	for _, msg := range out.Messages {
		if msg.Role == "system" && msg.Content == cursorSystem {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Cursor system prompt missing from output: %+v", out.Messages)
	}
}
