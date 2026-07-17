package e2e_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/cloud"
	"github.com/glider-ai/glider/internal/backend/ollama"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/vram"
)

// T5.1.2 — local failure falls back to cloud
func TestE2E_FallbackToCloud(t *testing.T) {
	cloudHits := 0
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ollamaSrv.Close()
	cloudSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudHits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"1","choices":[{"delta":{"content":"saved"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer cloudSrv.Close()

	reg := backend.NewRegistry()
	_ = reg.Register(ollama.New(ollamaSrv.URL))
	_ = reg.Register(cloud.NewOpenAI(cloudSrv.URL, "sk"))
	_ = reg.RegisterModel(backend.ModelInfo{Name: "codellama:7b", Backend: "ollama", State: backend.ModelStateWarm})

	cfg := config.RoutingConfig{Rules: []config.RuleConfig{{
		Name: "always", Priority: 0,
		Trigger: config.TriggerConfig{Type: "always"},
		Action:  config.ActionConfig{Target: "local", Backend: "ollama", Model: "codellama:7b"},
	}}}
	engine, _ := router.NewEngineFromConfig(cfg, router.NewStarlarkExecutor())
	vramMgr := vram.NewManager(vram.ManagerConfig{TotalBytes: 16 << 30, FreeBytes: 16 << 30})
	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry: reg, VRAM: orchestrator.AdaptVRAM{Inner: vramMgr},
		CloudBackend: "openai", CloudModel: "gpt-4o",
		IsHealthy: func(string) bool { return true },
	})
	hs := httptest.NewServer(api.NewServer("", &api.Handlers{Completer: &orchestrator.PipelineCompleter{
		Router: engine, Executor: exec,
	}}).Handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if cloudHits != 1 || !strings.Contains(string(raw), "saved") {
		t.Fatalf("cloudHits=%d body=%s", cloudHits, raw)
	}
}

// T5.1.3 — concurrent requests
func TestE2E_ConcurrentRequests(t *testing.T) {
	var hits atomic.Int32
	backendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			hits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"1","choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer backendSrv.Close()

	reg := backend.NewRegistry()
	_ = reg.Register(ollama.New(backendSrv.URL))
	_ = reg.RegisterModel(backend.ModelInfo{Name: "m", Backend: "ollama", State: backend.ModelStateWarm})
	cfg := config.RoutingConfig{Rules: []config.RuleConfig{{
		Name: "a", Priority: 0, Trigger: config.TriggerConfig{Type: "always"},
		Action: config.ActionConfig{Target: "local", Backend: "ollama", Model: "m"},
	}}}
	engine, _ := router.NewEngineFromConfig(cfg, router.NewStarlarkExecutor())
	vramMgr := vram.NewManager(vram.ManagerConfig{TotalBytes: 16 << 30, FreeBytes: 16 << 30})
	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry: reg, VRAM: orchestrator.AdaptVRAM{Inner: vramMgr},
		IsHealthy: func(string) bool { return true },
	})
	hs := httptest.NewServer(api.NewServer("", &api.Handlers{Completer: &orchestrator.PipelineCompleter{
		Router: engine, Executor: exec,
	}}).Handler())
	defer hs.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Post(hs.URL+"/v1/chat/completions", "application/json",
				strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`))
			if err != nil {
				errs <- err
				return
			}
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				errs <- errStatus(resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 5 {
		t.Fatalf("hits=%d", hits.Load())
	}
}

// T5.3.3 — corrupt config keeps serving with old config
func TestE2E_CorruptConfigPreservesOld(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/glider.yaml"
	initial := []byte("server:\n  proxy_port: 8080\nthresholds:\n  max_local_context_tokens: 8000\n")
	if err := os.WriteFile(path, initial, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := config.NewProvider(cfg, path)
	if err := p.StartWatcher(); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	if err := os.WriteFile(path, []byte{0x00, 0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if p.Get().Thresholds.MaxLocalContextTokens != 8000 {
		t.Fatalf("config changed unexpectedly: %d", p.Get().Thresholds.MaxLocalContextTokens)
	}
}
