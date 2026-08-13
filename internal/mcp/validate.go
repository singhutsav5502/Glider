package mcp

import (
	"fmt"
	"strings"
)

// ValidateServerConfig rejects secrets embedded in YAML/config and fills defaults.
func ValidateServerConfig(cfg *ServerConfig) error {
	if cfg == nil {
		return fmt.Errorf("nil server config")
	}
	cfg.ID = strings.TrimSpace(cfg.ID)
	if cfg.ID == "" {
		return fmt.Errorf("mcp: server id required")
	}
	if strings.TrimSpace(cfg.Auth.Token) != "" {
		return fmt.Errorf("mcp: inline auth.token forbidden — use token_env (e.g. GITHUB_TOKEN)")
	}
	tr := cfg.Transport
	if tr == "" {
		if cfg.URL != "" {
			tr = TransportHTTP
		} else if cfg.Command != "" {
			tr = TransportStdio
		}
		cfg.Transport = tr
	}
	switch cfg.Transport {
	case TransportStdio:
		if strings.TrimSpace(cfg.Command) == "" {
			return fmt.Errorf("mcp stdio: command required")
		}
	case TransportHTTP, TransportSSE:
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("mcp http: url required")
		}
	default:
		if cfg.URL == "" && cfg.Command == "" {
			return fmt.Errorf("mcp: transport, url, or command required")
		}
	}
	return nil
}

// ResolveGitHubTokenEnv returns the first non-empty GitHub token env var name.
func ResolveGitHubTokenEnv() string {
	for _, k := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(lookupEnv(k)); v != "" {
			return k
		}
	}
	return "GITHUB_PERSONAL_ACCESS_TOKEN"
}
