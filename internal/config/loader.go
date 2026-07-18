package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfig(data)
}

func ParseConfig(data []byte) (*Config, error) {
	cfg := DefaultConfig()
	// Reset slices so YAML fully replaces defaults when present
	cfg.Models = nil
	cfg.Backends = nil
	cfg.Routing.Rules = nil
	cfg.Cloud.Providers = nil

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("yaml parse error: %w", err)
	}
	applyDefaults(cfg)
	ApplyMITMDebugEnv(cfg)
	if err := Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ApplyMITMDebugEnv turns on mitm.debug_agent_rpc when GLIDER_MITM_DEBUG_RPC is
// 1, true, yes, or on (case-insensitive). Does not turn the flag off.
func ApplyMITMDebugEnv(cfg *Config) {
	if cfg == nil {
		return
	}
	v := strings.TrimSpace(strings.ToLower(os.Getenv("GLIDER_MITM_DEBUG_RPC")))
	switch v {
	case "1", "true", "yes", "on":
		cfg.MITM.DebugAgentRPC = true
	}
	f := strings.TrimSpace(strings.ToLower(os.Getenv("GLIDER_MITM_AGENT_RPC_FULFILL")))
	switch f {
	case "1", "true", "yes", "on":
		cfg.MITM.AgentRPCFulfill = true
	}
	canned := strings.TrimSpace(strings.ToLower(os.Getenv("GLIDER_MITM_AGENT_RPC_CANNED")))
	switch canned {
	case "1", "true", "yes", "on":
		cfg.MITM.AgentRPCCannedOnError = true
	}
}

func applyDefaults(cfg *Config) {
	d := DefaultConfig()
	cfg.Routing.ApplyDefaultTarget()
	if cfg.Server.ProxyPort == 0 {
		cfg.Server.ProxyPort = d.Server.ProxyPort
	}
	if cfg.Server.DashboardPort == 0 {
		cfg.Server.DashboardPort = d.Server.DashboardPort
	}
	if cfg.Server.LogLevel == "" {
		cfg.Server.LogLevel = d.Server.LogLevel
	}
	if cfg.Thresholds.MaxLocalContextTokens == 0 {
		cfg.Thresholds.MaxLocalContextTokens = d.Thresholds.MaxLocalContextTokens
	}
	if cfg.Thresholds.IdleUnloadTimeout == "" {
		cfg.Thresholds.IdleUnloadTimeout = d.Thresholds.IdleUnloadTimeout
	}
	if cfg.Thresholds.RequestTimeout == "" {
		cfg.Thresholds.RequestTimeout = d.Thresholds.RequestTimeout
	}
	if cfg.VRAM.Strategy == "" {
		cfg.VRAM.Strategy = d.VRAM.Strategy
	}
	if cfg.VRAM.HeadroomMB == 0 {
		cfg.VRAM.HeadroomMB = d.VRAM.HeadroomMB
	}
	if cfg.VRAM.MaxLoadedModels == 0 {
		cfg.VRAM.MaxLoadedModels = d.VRAM.MaxLoadedModels
	}
	if cfg.VRAM.GPUAssignments == nil {
		cfg.VRAM.GPUAssignments = map[string]int{}
	}
	if cfg.ModelAliases == nil {
		cfg.ModelAliases = map[string]string{}
	}
	if cfg.MITM.Port == 0 {
		cfg.MITM.Port = 8082
	}
	if len(cfg.MITM.Hosts) == 0 {
		cfg.MITM.Hosts = []string{"api2.cursor.sh", "api3.cursor.sh", "api4.cursor.sh", "*.api5.cursor.sh"}
	}
}

// Validate is defined in validate.go (structural checks used by ParseConfig).
