package dashboard_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/dashboard"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/gorilla/websocket"
)

func setupDash(t *testing.T) (*httptest.Server, *config.Provider, *backend.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	cfg := config.DefaultConfig()
	cfg.Thresholds.MaxLocalContextTokens = 8000
	data, _ := json.Marshal(cfg) // wrong format; write YAML via store later
	_ = data
	raw := []byte("server:\n  proxy_port: 8080\nthresholds:\n  max_local_context_tokens: 8000\n")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := config.NewProvider(loaded, path)
	reg := backend.NewRegistry()
	_ = reg.RegisterModel(backend.ModelInfo{Name: "codellama:7b", Backend: "ollama", State: backend.ModelStateCold})
	_ = reg.RegisterModel(backend.ModelInfo{Name: "llama3:8b", Backend: "ollama", State: backend.ModelStateWarm})
	bus := metrics.NewBus()
	store := &dashboard.FileConfigStore{Provider: p, Path: path}
	models := &dashboard.RegistryModelController{Registry: reg}
	srv := dashboard.New(":0", bus, store, models)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, p, reg, path
}

// T4.1.1
func TestGetConfig(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cfg config.Config
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Thresholds.MaxLocalContextTokens != 8000 {
		t.Fatalf("tokens=%d", cfg.Thresholds.MaxLocalContextTokens)
	}
}

// T4.1.2
func TestPutConfig(t *testing.T) {
	ts, p, _, path := setupDash(t)
	body := []byte(`{"thresholds":{"max_local_context_tokens":12000}}`)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if p.Get().Thresholds.MaxLocalContextTokens != 12000 {
		t.Fatalf("memory=%d", p.Get().Thresholds.MaxLocalContextTokens)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("12000")) {
		t.Fatalf("file not updated: %s", raw)
	}
}

// T4.1.3
func TestPutConfigRejectsInvalid(t *testing.T) {
	ts, p, _, _ := setupDash(t)
	body := []byte(`{"thresholds":{"max_local_context_tokens":-1}}`)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if p.Get().Thresholds.MaxLocalContextTokens != 8000 {
		t.Fatalf("config changed")
	}
}

// T4.1.4
func TestGetModels(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var models []backend.ModelInfo
	_ = json.NewDecoder(resp.Body).Decode(&models)
	if len(models) != 2 {
		t.Fatalf("models=%+v", models)
	}
}

// T4.1.5 / T4.1.6
func TestLoadUnloadModel(t *testing.T) {
	ts, _, reg, _ := setupDash(t)
	resp, err := http.Post(ts.URL+"/api/models/codellama:7b/load", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	m, _ := reg.GetModel("codellama:7b")
	if m.State != backend.ModelStateWarm {
		t.Fatalf("state=%s", m.State)
	}
	resp, err = http.Post(ts.URL+"/api/models/codellama:7b/unload", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	m, _ = reg.GetModel("codellama:7b")
	if m.State != backend.ModelStateCold {
		t.Fatalf("state=%s", m.State)
	}
}

// T4.2.1 / T4.2.3
func TestWebSocketEvents(t *testing.T) {
	bus := metrics.NewBus()
	// rebuild server with known bus
	cfgPath := filepath.Join(t.TempDir(), "g.yaml")
	_ = os.WriteFile(cfgPath, []byte("server:\n  proxy_port: 8080\n"), 0o644)
	cfg, _ := config.LoadConfig(cfgPath)
	p := config.NewProvider(cfg, cfgPath)
	reg := backend.NewRegistry()
	srv := dashboard.New(":0", bus, &dashboard.FileConfigStore{Provider: p, Path: cfgPath}, &dashboard.RegistryModelController{Registry: reg})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	u := "ws" + hs.URL[len("http"):] + "/ws"
	c1, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	// Allow handlers to Subscribe before publishing.
	time.Sleep(50 * time.Millisecond)
	bus.Publish(metrics.Event{Type: metrics.EventRequest, Data: metrics.RequestEventData{ID: "r1", Route: "local", Model: "m", Tokens: 10, LatencyMs: 1.5}})

	for _, c := range []*websocket.Conn{c1, c2} {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var ev metrics.Event
		if err := c.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != metrics.EventRequest {
			t.Fatalf("type=%s", ev.Type)
		}
	}
}

// T4.2.2
func TestWebSocketVRAM(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "g.yaml")
	_ = os.WriteFile(cfgPath, []byte("server:\n  proxy_port: 8080\n"), 0o644)
	cfg, _ := config.LoadConfig(cfgPath)
	p := config.NewProvider(cfg, cfgPath)
	bus := metrics.NewBus()
	srv := dashboard.New(":0", bus, &dashboard.FileConfigStore{Provider: p, Path: cfgPath}, &dashboard.RegistryModelController{Registry: backend.NewRegistry()})
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	u := "ws" + hs.URL[len("http"):] + "/ws"
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(50 * time.Millisecond)
	bus.Publish(metrics.Event{Type: metrics.EventVRAMUpdate, Data: metrics.VRAMEventData{Total: 8, Used: 3, Free: 5}})
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var ev metrics.Event
	if err := c.ReadJSON(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != metrics.EventVRAMUpdate {
		t.Fatalf("type=%s", ev.Type)
	}
}

// T4.5.1 / T4.5.2
func TestStaticAssets(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte(`id="app"`)) {
		t.Fatalf("html status=%d body=%s", resp.StatusCode, body)
	}
	resp, err = http.Get(ts.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	ct := resp.Header.Get("Content-Type")
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains([]byte(ct), []byte("javascript")) {
		t.Fatalf("js ct=%s status=%d", ct, resp.StatusCode)
	}
	resp, err = http.Get(ts.URL + "/style.css")
	if err != nil {
		t.Fatal(err)
	}
	ct = resp.Header.Get("Content-Type")
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains([]byte(ct), []byte("text/css")) {
		t.Fatalf("css ct=%s status=%d", ct, resp.StatusCode)
	}
}
