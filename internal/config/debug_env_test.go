package config_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/config"
)

func TestApplyMITMDebugEnv(t *testing.T) {
	t.Setenv("GLIDER_MITM_DEBUG_RPC", "1")
	cfg := config.DefaultConfig()
	if cfg.MITM.DebugAgentRPC {
		t.Fatal("default should be off")
	}
	config.ApplyMITMDebugEnv(cfg)
	if !cfg.MITM.DebugAgentRPC {
		t.Fatal("env should enable debug")
	}

	cfg2 := config.DefaultConfig()
	t.Setenv("GLIDER_MITM_DEBUG_RPC", "0")
	cfg2.MITM.DebugAgentRPC = true
	config.ApplyMITMDebugEnv(cfg2)
	if !cfg2.MITM.DebugAgentRPC {
		t.Fatal("env must not turn flag off")
	}
}

func TestParseConfigDebugAgentRPC(t *testing.T) {
	yml := `
server:
  proxy_port: 8080
  dashboard_port: 8081
mitm:
  enabled: true
  port: 8082
  debug_agent_rpc: true
  debug_dump_dir: ./.glider-debug
routing:
  rules:
    - name: Default
      priority: 0
      trigger: { type: always }
      action: { target: cloud, backend: openai, model: gpt-4o }
`
	t.Setenv("GLIDER_MITM_DEBUG_RPC", "")
	cfg, err := config.ParseConfig([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MITM.DebugAgentRPC {
		t.Fatal("yaml flag not set")
	}
	if cfg.MITM.DebugDumpDir != "./.glider-debug" {
		t.Fatalf("dump dir=%q", cfg.MITM.DebugDumpDir)
	}
}

func TestApplyMITMAgentRPCFulfillEnv(t *testing.T) {
	t.Setenv("GLIDER_MITM_AGENT_RPC_FULFILL", "1")
	cfg := config.DefaultConfig()
	if cfg.MITM.AgentRPCFulfill {
		t.Fatal("default should be off")
	}
	config.ApplyMITMDebugEnv(cfg)
	if !cfg.MITM.AgentRPCFulfill {
		t.Fatal("env should enable agent_rpc_fulfill")
	}
}

func TestParseConfigAgentRPCFulfill(t *testing.T) {
	yml := `
server:
  proxy_port: 8080
  dashboard_port: 8081
mitm:
  enabled: true
  port: 8082
  agent_rpc_fulfill: true
  agent_rpc_canned_on_error: true
  agent_rpc_canned_text: "pong-test"
routing:
  rules:
    - name: Default
      priority: 0
      trigger: { type: always }
      action: { target: cloud, backend: openai, model: gpt-4o }
`
	t.Setenv("GLIDER_MITM_AGENT_RPC_FULFILL", "")
	t.Setenv("GLIDER_MITM_AGENT_RPC_CANNED", "")
	cfg, err := config.ParseConfig([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MITM.AgentRPCFulfill {
		t.Fatal("yaml flag not set")
	}
	if !cfg.MITM.AgentRPCCannedOnError {
		t.Fatal("canned_on_error not set")
	}
	if cfg.MITM.AgentRPCCannedText != "pong-test" {
		t.Fatalf("canned text=%q", cfg.MITM.AgentRPCCannedText)
	}
}

func TestApplyMITMAgentRPCCannedEnv(t *testing.T) {
	t.Setenv("GLIDER_MITM_AGENT_RPC_CANNED", "1")
	cfg := config.DefaultConfig()
	config.ApplyMITMDebugEnv(cfg)
	if !cfg.MITM.AgentRPCCannedOnError {
		t.Fatal("env should enable canned_on_error")
	}
}

func TestApplyMITMAgentRPCToolCodecEnv(t *testing.T) {
	t.Setenv("GLIDER_MITM_AGENT_RPC_TOOL_CODEC", "true")
	cfg := config.DefaultConfig()
	if cfg.MITM.AgentRPCToolCodec {
		t.Fatal("default off")
	}
	config.ApplyMITMDebugEnv(cfg)
	if !cfg.MITM.AgentRPCToolCodec {
		t.Fatal("env should enable agent_rpc_tool_codec")
	}
}
