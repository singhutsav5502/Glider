package dashboard_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/dashboard"
	"github.com/glider-ai/glider/internal/mcp"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/mitm"
	"github.com/glider-ai/glider/internal/tools"
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
	if len(cfg.Backends) < 1 {
		t.Fatal("expected at least ollama backend")
	}
	hasOllama := false
	for _, b := range cfg.Backends {
		if b.Name == "ollama" {
			hasOllama = true
			if !strings.Contains(b.URL, "127.0.0.1") {
				t.Fatalf("ollama URL should prefer 127.0.0.1, got %q", b.URL)
			}
		}
	}
	if !hasOllama {
		t.Fatal("expected ollama backend in intro config")
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

func TestMITMDebugRecentAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	if err := os.WriteFile(path, []byte("server:\n  proxy_port: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := config.NewProvider(loaded, path)
	reg := backend.NewRegistry()
	bus := metrics.NewBus()
	collector := metrics.NewCollector(bus)
	collector.IncAction("mitm", "agent_rpc_opaque")
	store := &dashboard.FileConfigStore{Provider: p, Path: path}
	models := &dashboard.RegistryModelController{Registry: reg}
	s := dashboard.New(":0", bus, store, models)
	s.Metrics = collector

	dumpDir := t.TempDir()
	dbg := &mitm.AgentRPCDebugger{Enabled: true, DumpDir: dumpDir, RingSize: 8}
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend", nil)
	req.Header.Set("Content-Type", "application/connect+proto")
	dbg.Observe(req, []byte{0, 0, 0, 0, 1, 'x'}, "agent_rpc_opaque")
	s.MITMDebug = dbg

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/mitm/debug/recent?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Enabled      bool                  `json:"enabled"`
		Recent       []mitm.RPCObservation `json:"recent"`
		PathCounts   map[string]int        `json:"path_counts"`
		Metrics      map[string]int        `json:"metrics"`
		Distribution *struct {
			LocalPct float64 `json:"local_pct"`
			CloudPct float64 `json:"cloud_pct"`
		} `json:"distribution"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Enabled || len(body.Recent) != 1 {
		t.Fatalf("body=%+v", body)
	}
	if body.Metrics["action:agent_rpc_opaque"] != 1 {
		t.Fatalf("metrics=%v", body.Metrics)
	}
	if body.Distribution == nil {
		t.Fatal("expected distribution on debug/recent when Metrics set")
	}

	// GET /api/metrics
	collector.Record(metrics.RequestRecord{Action: "local", Route: "local", Tokens: 5})
	collector.Record(metrics.RequestRecord{Action: "origin_passthrough", Route: "cloud", Tokens: 5})
	mresp, err := http.Get(ts.URL + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer mresp.Body.Close()
	var snap metrics.Snapshot
	if err := json.NewDecoder(mresp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.Distribution.LocalCount != 1 || snap.Distribution.CloudCount != 1 {
		t.Fatalf("snapshot=%+v", snap.Distribution)
	}

	s2 := dashboard.New(":0", bus, store, models)
	ts2 := httptest.NewServer(s2.Handler())
	t.Cleanup(ts2.Close)
	resp2, err := http.Get(ts2.URL + "/api/mitm/debug/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var body2 struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&body2)
	if body2.Enabled {
		t.Fatal("expected enabled=false without debugger")
	}
}

func TestContextAPIRecentAndTurn(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	// Without graph: empty recent
	resp, err := http.Get(ts.URL + "/api/context/recent")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var empty struct {
		Turns []any `json:"turns"`
		Stats struct {
			Turns int `json:"turns"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Turns) != 0 {
		t.Fatalf("want empty turns, got %d", len(empty.Turns))
	}

	g := contextgraph.New("")
	g.Append(contextgraph.Event{
		Kind:      contextgraph.EventTurnOpened,
		TurnID:    "turn-api-1",
		RequestID: "turn-api-1",
		Attrs:     map[string]string{"route": "cloud", "source": "explicit_cloud"},
	})
	g.Append(contextgraph.Event{
		Kind:      contextgraph.EventSummaryRequested,
		TurnID:    "turn-api-1",
		RequestID: "sum-api",
		Actor:     "cloud",
	})
	g.BindRequest("turn-api-1", "sum-api")

	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	_ = os.WriteFile(path, []byte("server:\n  proxy_port: 8080\n"), 0o644)
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := config.NewProvider(loaded, path)
	reg := backend.NewRegistry()
	bus := metrics.NewBus()
	store := &dashboard.FileConfigStore{Provider: p, Path: path}
	models := &dashboard.RegistryModelController{Registry: reg}
	srv := dashboard.New(":0", bus, store, models)
	srv.ContextGraph = g
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	resp2, err := http.Get(hs.URL + "/api/context/recent?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var recent struct {
		Turns []struct {
			ID    string `json:"id"`
			Route string `json:"route"`
			Stats *struct {
				EventCount int `json:"event_count"`
			} `json:"stats"`
		} `json:"turns"`
		Stats struct {
			Turns      int            `json:"turns"`
			CloudTurns int            `json:"cloud_turns"`
			ByKind     map[string]int `json:"by_kind"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&recent); err != nil {
		t.Fatal(err)
	}
	if recent.Stats.Turns < 1 || recent.Stats.CloudTurns < 1 {
		t.Fatalf("stats=%+v", recent.Stats)
	}
	if len(recent.Turns) < 1 || recent.Turns[0].Route != "cloud" {
		t.Fatalf("turns=%+v", recent.Turns)
	}

	resp3, err := http.Get(hs.URL + "/api/context/turns/sum-api")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Fatalf("status=%d", resp3.StatusCode)
	}
	var turn struct {
		ID     string `json:"id"`
		Route  string `json:"route"`
		Events []any  `json:"events"`
		Stats  *struct {
			EventCount int  `json:"event_count"`
			CloudLive  bool `json:"cloud_live"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	if turn.ID != "turn-api-1" || turn.Stats == nil || turn.Stats.EventCount < 2 {
		t.Fatalf("turn=%+v", turn)
	}
}

func TestMCPAPI(t *testing.T) {
	ts, _, _, _ := setupDash(t)
	// Without MCP manager: empty servers + github snapshot.
	resp, err := http.Get(ts.URL + "/api/mcp/servers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var empty struct {
		Servers []any `json:"servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	if empty.Servers == nil {
		t.Fatal("servers null")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	_ = os.WriteFile(path, []byte("server:\n  proxy_port: 8080\n"), 0o644)
	loaded, _ := config.LoadConfig(path)
	p := config.NewProvider(loaded, path)
	bus := metrics.NewBus()
	store := &dashboard.FileConfigStore{Provider: p, Path: path}
	models := &dashboard.RegistryModelController{Registry: backend.NewRegistry()}
	srv := dashboard.New(":0", bus, store, models)
	mgr := mcp.NewManager()
	if err := mgr.Configure(mcp.DefaultGitHubConfig(), mcp.DefaultGitHubStdioConfig()); err != nil {
		t.Fatal(err)
	}
	srv.MCP = mgr
	ts2 := httptest.NewServer(srv.Handler())
	t.Cleanup(ts2.Close)

	resp2, err := http.Get(ts2.URL + "/api/mcp/servers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var body struct {
		Servers []mcp.ServerStatus `json:"servers"`
		GitHub  mcp.GitHubStatus   `json:"github"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Servers) != 2 {
		t.Fatalf("servers=%d", len(body.Servers))
	}
	if body.GitHub.RemoteURL == "" {
		t.Fatal("github remote empty")
	}

	resp3, err := http.Get(ts2.URL + "/api/mcp/servers/github/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	var toolsBody struct {
		Source string     `json:"source"`
		Tools  []mcp.Tool `json:"tools"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&toolsBody); err != nil {
		t.Fatal(err)
	}
	if toolsBody.Source != "catalog" || len(toolsBody.Tools) == 0 {
		t.Fatalf("%+v", toolsBody)
	}

	resp4, err := http.Get(ts2.URL + "/api/mcp/github")
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("github status=%d", resp4.StatusCode)
	}

	// Disconnect unknown → 404/400 via reconnect on missing
	resp5, err := http.Post(ts2.URL+"/api/mcp/servers/nope/reconnect", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusNotFound {
		t.Fatalf("reconnect missing want 404 got %d", resp5.StatusCode)
	}
}

func TestWorkspaceAPI(t *testing.T) {
	dir := t.TempDir()
	lay := tools.LayoutForRun(dir, "api-run")
	if err := lay.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lay.OutAbs, "report.md"), []byte("# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lay.WorkAbs, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lay.WorkAbs, "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := metrics.NewBus()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	_ = os.WriteFile(cfgPath, []byte("server:\n  proxy_port: 8080\n"), 0o644)
	store := &dashboard.FileConfigStore{Provider: config.NewProvider(config.DefaultConfig(), cfgPath), Path: cfgPath}
	models := &dashboard.RegistryModelController{Registry: backend.NewRegistry()}
	srv := dashboard.New(":0", bus, store, models)
	srv.Workspace = dir
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/workspace?run=api-run")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var listing map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	if listing["run"] != "api-run" {
		t.Fatalf("%+v", listing)
	}

	filePath := filepath.ToSlash(filepath.Join("runs", "api-run", "out", "report.md"))
	resp2, err := http.Get(ts.URL + "/api/workspace?file=" + filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("file status=%d", resp2.StatusCode)
	}
	var file map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(file["content"]), "# ok") {
		t.Fatalf("%+v", file)
	}

	a := filepath.ToSlash(filepath.Join("runs", "api-run", "work", "a.txt"))
	b := filepath.ToSlash(filepath.Join("runs", "api-run", "work", "b.txt"))
	resp3, err := http.Get(ts.URL + "/api/workspace?diff=1&a=" + a + "&b=" + b)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp3.Body)
		t.Fatalf("diff status=%d body=%s", resp3.StatusCode, body)
	}
}

func TestAgentLogsAfterSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	if err := os.WriteFile(path, []byte("server:\n  proxy_port: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p := config.NewProvider(loaded, path)
	bus := metrics.NewBus()
	store := &dashboard.FileConfigStore{Provider: p, Path: path}
	models := &dashboard.RegistryModelController{Registry: backend.NewRegistry()}
	srv := dashboard.New(":0", bus, store, models)
	logs := agentlog.NewStore(32)
	srv.AgentLogs = logs
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	logs.Info(agentlog.ScopeHoop, "h1", "a", "one", nil)
	logs.Info(agentlog.ScopeHoop, "h1", "b", "two", nil)
	logs.Info(agentlog.ScopeHoop, "h1", "c", "three", nil)
	all := logs.Recent(agentlog.ScopeHoop, "h1", 50)
	if len(all) != 3 {
		t.Fatalf("seed len=%d", len(all))
	}

	resp, err := http.Get(ts.URL + "/api/agent-logs?scope=hoop&id=h1&limit=50")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var full struct {
		Entries []agentlog.Entry `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		t.Fatal(err)
	}
	if len(full.Entries) != 3 {
		t.Fatalf("full entries=%d", len(full.Entries))
	}

	after := all[0].Seq
	for _, q := range []string{
		fmt.Sprintf("/api/agent-logs?scope=hoop&id=h1&after_seq=%d&limit=50", after),
		fmt.Sprintf("/api/agent-logs?scope=hoop&id=h1&afterSeq=%d&limit=50", after),
	} {
		r, err := http.Get(ts.URL + q)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Entries []agentlog.Entry `json:"entries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			r.Body.Close()
			t.Fatal(err)
		}
		r.Body.Close()
		if len(body.Entries) != 2 {
			t.Fatalf("%s: want 2 got %d", q, len(body.Entries))
		}
		if body.Entries[0].Seq <= after || body.Entries[1].Message != "three" {
			t.Fatalf("%s: %+v", q, body.Entries)
		}
	}

	tip := all[2].Seq
	rEmpty, err := http.Get(fmt.Sprintf("%s/api/agent-logs?scope=hoop&id=h1&after_seq=%d", ts.URL, tip))
	if err != nil {
		t.Fatal(err)
	}
	defer rEmpty.Body.Close()
	var empty struct {
		Entries []agentlog.Entry `json:"entries"`
	}
	if err := json.NewDecoder(rEmpty.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Entries) != 0 {
		t.Fatalf("tip cursor want empty, got %+v", empty.Entries)
	}

	bad, err := http.Get(ts.URL + "/api/agent-logs?scope=hoop&id=h1&after_seq=nope")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad after_seq status=%d", bad.StatusCode)
	}
}
