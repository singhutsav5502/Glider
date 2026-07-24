package mcp

import (
	"fmt"
	"os"
	"strings"
)

// ResolveAuth expands AuthEnv into Token from the process environment.
func ResolveAuth(a AuthConfig) (AuthConfig, error) {
	out := a
	if out.Kind == "" {
		if out.TokenEnv != "" || out.Token != "" {
			out.Kind = AuthBearer
		} else {
			out.Kind = AuthNone
		}
	}
	if out.Kind == AuthEnv || (out.Token == "" && out.TokenEnv != "") {
		env := out.TokenEnv
		if env == "" {
			return out, fmt.Errorf("auth: token_env required")
		}
		v := os.Getenv(env)
		if v == "" {
			// Try GitHub aliases when configured env is empty.
			if isGitHubTokenEnv(env) {
				for _, alt := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
					if alt == env {
						continue
					}
					if vv := os.Getenv(alt); vv != "" {
						v = vv
						break
					}
				}
			}
			if v == "" && isGitHubTokenEnv(env) {
				if t, err := LoadGitHubTokenFile(); err == nil {
					v = strings.TrimSpace(t)
				}
			}
			if v == "" {
				return out, fmt.Errorf("auth: env %s empty", env)
			}
		}
		out.Token = v
		if out.Kind == AuthEnv {
			out.Kind = AuthBearer
		}
	}
	if out.HeaderName == "" && out.Kind == AuthBearer {
		out.HeaderName = "Authorization"
	}
	return out, nil
}

func isGitHubTokenEnv(env string) bool {
	switch env {
	case "GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN":
		return true
	default:
		return false
	}
}

// AuthorizationHeader returns "Bearer <token>" or empty.
func AuthorizationHeader(a AuthConfig) string {
	resolved, err := ResolveAuth(a)
	if err != nil || resolved.Token == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(resolved.Token), "bearer ") {
		return resolved.Token
	}
	return "Bearer " + resolved.Token
}
