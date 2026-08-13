package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/config"
)

const fullYAML = `
server:
  proxy_port: 8080
  dashboard_port: 8081
  log_level: "info"
thresholds:
  max_local_context_tokens: 8000
  idle_unload_timeout: "5m"
  request_timeout: "10m"
vram:
  strategy: "hybrid"
  headroom_mb: 512
  max_loaded_models: 3
  gpu_assignments:
    "codellama:7b": 0
models:
  - name: "codellama:7b"
    backend: "ollama"
    vram_estimate_mb: 4200
    max_context: 16384
    capabilities: ["code", "refactor"]
    keep_warm: true
routing:
  rules:
    - name: "Explicit Local"
      priority: 100
      trigger:
        type: "explicit"
        commands: ["/local", "/fast"]
      action:
        target: "local"
        model: "codellama:7b"
cloud:
  providers:
    - name: "openai"
      api_key_env: "OPENAI_API_KEY"
      base_url: "https://api.openai.com/v1"
  rate_limit:
    requests_per_minute: 30
    tokens_per_minute: 100000
  budget_cap_usd: 50.00
backends:
  - name: "ollama"
    type: "local"
    url: "http://localhost:11434"
dashboard:
  enabled: true
`

// T2.1.1
func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	if err := os.WriteFile(path, []byte(fullYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ProxyPort != 8080 {
		t.Fatalf("proxy_port=%d", cfg.Server.ProxyPort)
	}
	if cfg.Thresholds.MaxLocalContextTokens != 8000 {
		t.Fatalf("tokens=%d", cfg.Thresholds.MaxLocalContextTokens)
	}
	if cfg.VRAM.Strategy != "hybrid" || cfg.VRAM.HeadroomMB != 512 {
		t.Fatalf("vram=%+v", cfg.VRAM)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Name != "codellama:7b" {
		t.Fatalf("models=%+v", cfg.Models)
	}
	if len(cfg.Routing.Rules) != 1 || cfg.Routing.Rules[0].Priority != 100 {
		t.Fatalf("rules=%+v", cfg.Routing.Rules)
	}
}

// T2.1.2
func TestRejectInvalidYAML(t *testing.T) {
	_, err := config.ParseConfig([]byte("server:\n  proxy_port: [\nbad"))
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// T2.1.3
func TestRejectMissingProxyPort(t *testing.T) {
	// Explicit zero after parse — use YAML that sets proxy_port null via empty and bypass defaults
	cfg := &config.Config{}
	cfg.Server.ProxyPort = 0
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected validation error")
	}
}

// T2.1.4
func TestApplyDefaults(t *testing.T) {
	cfg, err := config.ParseConfig([]byte(`
server:
  proxy_port: 9090
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Thresholds.IdleUnloadTimeout != "5m" {
		t.Fatalf("idle=%s", cfg.Thresholds.IdleUnloadTimeout)
	}
	if cfg.VRAM.Strategy != "dynamic" {
		t.Fatalf("strategy=%s", cfg.VRAM.Strategy)
	}
	if cfg.Server.ProxyPort != 9090 {
		t.Fatalf("port=%d", cfg.Server.ProxyPort)
	}
}

// T2.2.1 / T2.2.2 / T2.2.3
func TestConfigHotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "glider.yaml")
	initial := []byte(`
server:
  proxy_port: 8080
thresholds:
  max_local_context_tokens: 8000
`)
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

	called := make(chan *config.Config, 1)
	p.Watch(func(c *config.Config) { called <- c })

	updated := []byte(`
server:
  proxy_port: 8080
thresholds:
  max_local_context_tokens: 12000
`)
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case c := <-called:
		if c.Thresholds.MaxLocalContextTokens != 12000 {
			t.Fatalf("callback cfg=%d", c.Thresholds.MaxLocalContextTokens)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callback not fired within 2s")
	}
	if p.Get().Thresholds.MaxLocalContextTokens != 12000 {
		t.Fatalf("get=%d", p.Get().Thresholds.MaxLocalContextTokens)
	}

	// T2.2.2 invalid rejected
	if err := os.WriteFile(path, []byte(":::bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if p.Get().Thresholds.MaxLocalContextTokens != 12000 {
		t.Fatalf("old config not preserved: %d", p.Get().Thresholds.MaxLocalContextTokens)
	}
}
