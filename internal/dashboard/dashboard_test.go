package dashboard_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func fullConfigJSON(tokens int) []byte {
	cfg := config.DefaultConfig()
	cfg.Thresholds.MaxLocalContextTokens = tokens
	cfg.ModelAliases = map[string]string{"gpt-4o": "codellama:7b"}
	data, _ := json.Marshal(cfg)
	return data
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
	if cfg.Server.ProxyPort != 8080 {
		t.Fatalf("proxy_port=%d", cfg.Server.ProxyPort)
	}
}

func TestGetConfigYAML(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Get(ts.URL + "/api/config?format=yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte("proxy_port:")) {
		t.Fatalf("not yaml: %s", body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Fatalf("ct=%s", ct)
	}
	cfg, err := config.ParseConfig(body)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Thresholds.MaxLocalContextTokens != 8000 {
		t.Fatalf("tokens=%d", cfg.Thresholds.MaxLocalContextTokens)
	}
}

// T4.1.2 — full config replace persists to disk + memory
func TestPutConfig(t *testing.T) {
	ts, p, _, path := setupDash(t)
	body := fullConfigJSON(12000)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, rawBody)
	}
	if p.Get().Thresholds.MaxLocalContextTokens != 12000 {
		t.Fatalf("memory=%d", p.Get().Thresholds.MaxLocalContextTokens)
	}
	if p.Get().ModelAliases["gpt-4o"] != "codellama:7b" {
		t.Fatalf("aliases=%v", p.Get().ModelAliases)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("12000")) {
		t.Fatalf("file not updated: %s", raw)
	}
}

func TestPutConfigYAML(t *testing.T) {
	ts, p, _, path := setupDash(t)
	yml := []byte(`
server:
  proxy_port: 8080
  dashboard_port: 8081
  log_level: info
thresholds:
  max_local_context_tokens: 15000
  idle_unload_timeout: 5m
  request_timeout: 120s
mitm:
  enabled: true
  port: 8082
  hosts:
    - api2.cursor.sh
    - api3.cursor.sh
    - api4.cursor.sh
    - "*.api5.cursor.sh"
  passthrough_default: true
model_aliases:
  gpt-4o: codellama:7b
dashboard:
  enabled: true
`)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", bytes.NewReader(yml))
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, rawBody)
	}
	if p.Get().Thresholds.MaxLocalContextTokens != 15000 {
		t.Fatalf("memory=%d", p.Get().Thresholds.MaxLocalContextTokens)
	}
	if !p.Get().MITM.Enabled {
		t.Fatal("mitm not enabled")
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("15000")) {
		t.Fatalf("file not updated: %s", raw)
	}
}

// T4.1.3
func TestPutConfigRejectsInvalid(t *testing.T) {
	ts, p, _, _ := setupDash(t)
	body := []byte(`{"server":{"proxy_port":8080},"thresholds":{"max_local_context_tokens":-1}}`)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	if !bytes.Contains(raw, []byte("max_local_context_tokens")) {
		t.Fatalf("error=%s", raw)
	}
	if p.Get().Thresholds.MaxLocalContextTokens != 8000 {
		t.Fatalf("config changed")
	}
}

func TestPutConfigRejectsInvalidYAML(t *testing.T) {
	ts, p, _, _ := setupDash(t)
	body := []byte("server:\n  proxy_port: 8080\nthresholds:\n  max_local_context_tokens: -5\n")
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if p.Get().Thresholds.MaxLocalContextTokens != 8000 {
		t.Fatal("config changed")
	}
}

func TestPutConfigNotifiesWatchers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	_ = os.WriteFile(path, []byte("server:\n  proxy_port: 8080\nthresholds:\n  max_local_context_tokens: 8000\n"), 0o644)
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := config.NewProvider(loaded, path)
	notified := make(chan int, 1)
	p.Watch(func(c *config.Config) {
		notified <- c.Thresholds.MaxLocalContextTokens
	})
	store := &dashboard.FileConfigStore{Provider: p, Path: path}
	srv := dashboard.New(":0", metrics.NewBus(), store, &dashboard.RegistryModelController{Registry: backend.NewRegistry()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/config", bytes.NewReader(fullConfigJSON(9001)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	select {
	case n := <-notified:
		if n != 9001 {
			t.Fatalf("notified=%d", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher not notified")
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
	var models []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&models)
	if len(models) != 2 {
		t.Fatalf("models=%+v", models)
	}
}

func TestGetVRAM(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Get(ts.URL + "/api/vram")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var snap map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	models, ok := snap["models"].([]any)
	if !ok || len(models) < 2 {
		t.Fatalf("models=%v", snap["models"])
	}
}

func TestValidateEndpoint(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	resp, err := http.Get(ts.URL + "/api/validate")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var res config.ValidationResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	_ = os.WriteFile(path, []byte("server:\n  proxy_port: 8080\n"), 0o644)
	cfg, _ := config.LoadConfig(path)
	p := config.NewProvider(cfg, path)
	hist, err := metrics.OpenHistoryStore(filepath.Join(dir, "hist"), "run-dash")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hist.Close() })
	_ = hist.Record(metrics.StoredRequest{ID: "1", Route: "local", Tokens: 1, LatencyMs: 2})
	bus := metrics.NewBus()
	srv := dashboard.New(":0", bus, &dashboard.FileConfigStore{Provider: p, Path: path}, &dashboard.RegistryModelController{Registry: backend.NewRegistry()})
	srv.History = hist
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	resp, err := http.Get(hs.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sessions []metrics.Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].RequestCount != 1 {
		t.Fatalf("sessions=%+v", sessions)
	}

	resp2, err := http.Get(hs.URL + "/api/sessions/run-dash/requests")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var reqs []metrics.StoredRequest
	if err := json.NewDecoder(resp2.Body).Decode(&reqs); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("reqs=%+v", reqs)
	}
}

func TestGPUAssignmentsPut(t *testing.T) {
	ts, p, _, _ := setupDash(t)
	body := []byte(`{"assignments":{"codellama:7b":0}}`)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/gpu-assignments", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	if p.Get().VRAM.GPUAssignments["codellama:7b"] != 0 {
		t.Fatalf("assignments=%v", p.Get().VRAM.GPUAssignments)
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
	for _, want := range []string{"MODE", "ACTION", "HOST / MODEL", "RULE", "cfg-yaml", "Edit YAML"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Fatalf("html missing %q", want)
		}
	}
	resp, err = http.Get(ts.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	ct := resp.Header.Get("Content-Type")
	jsBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains([]byte(ct), []byte("javascript")) {
		t.Fatalf("js ct=%s status=%d", ct, resp.StatusCode)
	}
	if !bytes.Contains(jsBody, []byte("original_model")) || !bytes.Contains(jsBody, []byte("data.mode")) {
		t.Fatal("app.js missing MITM observability field rendering")
	}
	if !bytes.Contains(jsBody, []byte("application/yaml")) {
		t.Fatal("app.js missing YAML save path")
	}
	resp, err = http.Get(ts.URL + "/style.css")
	if err != nil {
		t.Fatal(err)
	}
	ct = resp.Header.Get("Content-Type")
	cssBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains([]byte(ct), []byte("text/css")) {
		t.Fatalf("css ct=%s status=%d", ct, resp.StatusCode)
	}
	if !bytes.Contains(cssBody, []byte("90px 70px 110px 1.4fr 1fr 90px 70px")) {
		t.Fatal("css missing 7-column request log grid")
	}
}

func TestIntroConfigLoads(t *testing.T) {
	root := filepath.Join("..", "..", "configs", "glider.yaml")
	cfg, err := config.LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MITM.Enabled {
		t.Fatal("intro profile should enable MITM")
	}
	if cfg.Dashboard.Enabled != true {
		t.Fatal("dashboard should be enabled")
	}
	if len(cfg.ModelAliases) == 0 {
		t.Fatal("expected model_aliases")
	}
	if len(cfg.Cloud.Providers) < 2 {
		t.Fatal("expected openai+anthropic placeholders")
	}
	if len(cfg.Backends) < 2 {
		t.Fatal("expected ollama+vllm backends")
	}
	var hasScript, hasExplicit, hasDefault bool
	for _, r := range cfg.Routing.Rules {
		if r.Trigger.Type == "script" && strings.Contains(r.Trigger.File, "detect_refactor.star") {
			hasScript = true
		}
		if r.Trigger.Type == "explicit" {
			hasExplicit = true
		}
		if r.Trigger.Type == "always" && r.Action.Target == "cloud" {
			hasDefault = true
		}
	}
	if !hasScript || !hasExplicit || !hasDefault {
		t.Fatalf("routing incomplete script=%v explicit=%v default=%v", hasScript, hasExplicit, hasDefault)
	}
	wantHosts := []string{"api2.cursor.sh", "api3.cursor.sh", "api4.cursor.sh", "*.api5.cursor.sh"}
	for _, h := range wantHosts {
		found := false
		for _, got := range cfg.MITM.Hosts {
			if got == h {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing mitm host %q in %v", h, cfg.MITM.Hosts)
		}
	}
}
