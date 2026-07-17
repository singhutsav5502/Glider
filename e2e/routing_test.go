package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/cloud"
	"github.com/glider-ai/glider/internal/backend/ollama"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/orchestrator"
	"github.com/glider-ai/glider/internal/router"
	"github.com/glider-ai/glider/internal/transform"
	"github.com/glider-ai/glider/internal/vram"
)

const routingYAML = `
server:
  proxy_port: 8080
thresholds:
  max_local_context_tokens: 8000
routing:
  rules:
    - name: Explicit Local
      priority: 100
      trigger:
        type: explicit
        commands: ["/local"]
      action:
        target: local
        backend: ollama
        model: codellama:7b
    - name: Context Overflow
      priority: 10
      trigger:
        type: context_size
        operator: ">"
        value: 8000
      action:
        target: cloud
        backend: openai
        model: gpt-4o
    - name: Default Local
      priority: 0
      trigger:
        type: always
      action:
        target: local
        backend: ollama
        model: codellama:7b
`

func mockOllama(t *testing.T, chatHits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/chat/completions":
			chatHits.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"1","model":"codellama:7b","choices":[{"delta":{"content":"local"}}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case r.URL.Path == "/api/generate":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mockCloud(t *testing.T, chatHits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat/completions") {
			chatHits.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"1","model":"gpt-4o","choices":[{"delta":{"content":"cloud"}}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setupRoutedProxy(t *testing.T, cfgYAML string) (string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var oHits, cHits atomic.Int32
	ollamaSrv := mockOllama(t, &oHits)
	cloudSrv := mockCloud(t, &cHits)

	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	if err := os.WriteFile(path, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	reg := backend.NewRegistry()
	ob := ollama.New(ollamaSrv.URL)
	_ = reg.Register(ob)
	_ = ob.Ping(context.Background())
	_ = reg.Register(cloud.NewOpenAI(cloudSrv.URL, "sk"))
	_ = reg.RegisterModel(backend.ModelInfo{Name: "codellama:7b", Backend: "ollama", State: backend.ModelStateWarm, VRAMEstimateMB: 100})

	tok, err := transform.NewTokenizer()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := router.NewEngineFromConfig(cfg.Routing, router.NewStarlarkExecutor())
	if err != nil {
		t.Fatal(err)
	}
	vramMgr := vram.NewManager(vram.ManagerConfig{
		TotalBytes: 16 << 30, FreeBytes: 16 << 30, HeadroomBytes: 512 << 20,
	})
	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry:     reg,
		VRAM:         orchestrator.AdaptVRAM{Inner: vramMgr},
		CloudBackend: "openai",
		CloudModel:   "gpt-4o",
		IsHealthy:    func(string) bool { return true },
	})

	completer := &orchestrator.PipelineCompleter{
		Router:    engine,
		Executor:  exec,
		Tokenizer: tok,
	}
	hs := httptest.NewServer(api.NewServer("", &api.Handlers{Completer: completer}).Handler())
	t.Cleanup(hs.Close)
	return hs.URL, &oHits, &cHits
}

// T2.6.1
func TestE2E_ExplicitLocalRoutesToOllama(t *testing.T) {
	url, oHits, cHits := setupRoutedProxy(t, routingYAML)
	resp, err := http.Post(url+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"/local refactor this"}],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if oHits.Load() != 1 || cHits.Load() != 0 {
		t.Fatalf("ollama=%d cloud=%d body=%s", oHits.Load(), cHits.Load(), raw)
	}
	if !strings.Contains(string(raw), "local") {
		t.Fatalf("body=%s", raw)
	}
}

// T2.6.2
func TestE2E_LargeContextRoutesToCloud(t *testing.T) {
	url, oHits, cHits := setupRoutedProxy(t, routingYAML)
	big := strings.Repeat("word ", 15000)
	payload, _ := json.Marshal(map[string]any{
		"model":  "gpt-4o",
		"stream": true,
		"messages": []map[string]string{
			{"role": "user", "content": big},
		},
	})
	resp, err := http.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if cHits.Load() != 1 || oHits.Load() != 0 {
		t.Fatalf("ollama=%d cloud=%d body=%s", oHits.Load(), cHits.Load(), raw)
	}
	if !strings.Contains(string(raw), "cloud") {
		t.Fatalf("body=%s", raw)
	}
}

type liveEngine struct {
	get func() *router.Engine
}

func (l *liveEngine) Route(ctx context.Context, req *backend.CompletionRequest) (*backend.RoutingDecision, error) {
	return l.get().Route(ctx, req)
}

// T2.6.3 — raising context threshold flips routing from cloud to local
func TestE2E_ConfigHotReloadChangesRouting(t *testing.T) {
	var oHits, cHits atomic.Int32
	ollamaSrv := mockOllama(t, &oHits)
	cloudSrv := mockCloud(t, &cHits)

	cfg, err := config.ParseConfig([]byte(routingYAML))
	if err != nil {
		t.Fatal(err)
	}
	reg := backend.NewRegistry()
	_ = reg.Register(ollama.New(ollamaSrv.URL))
	_ = reg.Register(cloud.NewOpenAI(cloudSrv.URL, "sk"))
	_ = reg.RegisterModel(backend.ModelInfo{Name: "codellama:7b", Backend: "ollama", State: backend.ModelStateWarm, VRAMEstimateMB: 100})
	tok, _ := transform.NewTokenizer()
	star := router.NewStarlarkExecutor()
	engine, _ := router.NewEngineFromConfig(cfg.Routing, star)

	vramMgr := vram.NewManager(vram.ManagerConfig{TotalBytes: 16 << 30, FreeBytes: 16 << 30})
	exec := orchestrator.NewSimpleExecutor(orchestrator.SimpleExecutorConfig{
		Registry: reg, VRAM: orchestrator.AdaptVRAM{Inner: vramMgr},
		CloudBackend: "openai", CloudModel: "gpt-4o",
		IsHealthy: func(string) bool { return true },
	})
	var engineHolder atomic.Pointer[router.Engine]
	engineHolder.Store(engine)
	completer := &orchestrator.PipelineCompleter{
		Router: &liveEngine{get: func() *router.Engine { return engineHolder.Load() }},
		Executor: exec, Tokenizer: tok,
	}
	hs := httptest.NewServer(api.NewServer("", &api.Handlers{Completer: completer}).Handler())
	defer hs.Close()

	// Large prompt → cloud under threshold 8000
	mid := strings.Repeat("word ", 15000)
	payload, _ := json.Marshal(map[string]any{
		"model": "gpt-4o", "stream": true,
		"messages": []map[string]string{{"role": "user", "content": mid}},
	})
	resp, err := http.Post(hs.URL+"/v1/chat/completions", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if cHits.Load() != 1 || oHits.Load() != 0 {
		t.Fatalf("before reload: ollama=%d cloud=%d", oHits.Load(), cHits.Load())
	}

	updated := strings.ReplaceAll(routingYAML, "value: 8000", "value: 50000")
	cfg2, err := config.ParseConfig([]byte(updated))
	if err != nil {
		t.Fatal(err)
	}
	next, err := router.NewEngineFromConfig(cfg2.Routing, star)
	if err != nil {
		t.Fatal(err)
	}
	engineHolder.Store(next)

	resp, err = http.Post(hs.URL+"/v1/chat/completions", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if oHits.Load() != 1 || cHits.Load() != 1 {
		t.Fatalf("after reload: ollama=%d cloud=%d (want ollama=1 cloud=1)", oHits.Load(), cHits.Load())
	}
}
