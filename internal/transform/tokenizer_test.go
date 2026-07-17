package transform_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/transform"
)

func TestCountKnownInput_T2_3_1(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}

	got := tok.Count("Hello, world!")
	if got != 4 {
		t.Fatalf("Count(%q) = %d, want 4", "Hello, world!", got)
	}
}

func TestCountEmpty_T2_3_3(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}

	got := tok.Count("")
	if got != 0 {
		t.Fatalf("Count(%q) = %d, want 0", "", got)
	}
}

func TestEstimateRequestTokens_T2_3_2(t *testing.T) {
	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}

	word := "tokenization "
	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "system", Content: strings.Repeat(word, 150)},
			{Role: "user", Content: strings.Repeat(word, 175)},
			{Role: "assistant", Content: strings.Repeat(word, 175)},
		},
	}

	actual := 0
	for _, msg := range req.Messages {
		actual += tok.Count(msg.Content)
	}
	estimate := tok.EstimateRequestTokens(req)

	low := float64(actual) * 0.9
	high := float64(actual) * 1.1
	if float64(estimate) < low || float64(estimate) > high {
		t.Fatalf("EstimateRequestTokens = %d, content tokens = %d, want within ±10%%", estimate, actual)
	}
}
