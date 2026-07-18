package dashboard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/dashboard"
)

func TestDiscoverModels_OptionalBackendIsWarning(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"codellama:7b"}]}`))
	}))
	defer ollama.Close()

	cfg := &config.Config{
		Backends: []config.BackendConfig{
			{Name: "ollama", Type: "local", URL: ollama.URL},
			{Name: "vllm", Type: "local", URL: "http://127.0.0.1:1"},
		},
	}
	models, _, errs, warns := dashboard.DiscoverModels(context.Background(), cfg, nil)
	if len(errs) != 0 {
		t.Fatalf("expected no hard errors when ollama is live, got %v", errs)
	}
	if len(warns) == 0 {
		t.Fatal("expected vllm unreachable as warning")
	}
	if !strings.Contains(warns[0], "vllm") {
		t.Fatalf("warning=%q", warns[0])
	}
	found := false
	for _, m := range models {
		if m.Name == "codellama:7b" && m.Available {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected discovered ollama model, got %+v", models)
	}
}

func TestDiscoverModels_AllDownIsError(t *testing.T) {
	cfg := &config.Config{
		Backends: []config.BackendConfig{
			{Name: "ollama", Type: "local", URL: "http://127.0.0.1:1"},
			{Name: "vllm", Type: "local", URL: "http://127.0.0.1:2"},
		},
	}
	_, _, errs, warns := dashboard.DiscoverModels(context.Background(), cfg, nil)
	if len(warns) != 0 {
		t.Fatalf("expected no warnings when nothing is live, got %v", warns)
	}
	if len(errs) < 2 {
		t.Fatalf("expected hard errors for both backends, got %v", errs)
	}
}
