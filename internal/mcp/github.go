package mcp

import "encoding/json"

// GitHubRemoteURL is the hosted GitHub MCP endpoint (Copilot MCP).
// See https://github.com/github/github-mcp-server
const GitHubRemoteURL = "https://api.githubcopilot.com/mcp/"

// GitHubDockerImage is the local stdio server image.
const GitHubDockerImage = "ghcr.io/github/github-mcp-server"

// GitHubInstallNotes documents how to run the official GitHub MCP server.
const GitHubInstallNotes = `
GitHub MCP (live):

HTTP (hosted):
  export GITHUB_PERSONAL_ACCESS_TOKEN=ghp_...   # or GITHUB_TOKEN / GH_TOKEN
  # Glider: mcp.DefaultGitHubConfig() → Manager.Connect
  # Sends Authorization + Mcp-Session-Id + MCP-Protocol-Version + X-MCP-Toolsets
  # On 401/session 400: clear session, refresh token from env/file, re-initialize once

Docker stdio (official image):
  docker pull ghcr.io/github/github-mcp-server
  export GITHUB_PERSONAL_ACCESS_TOKEN=ghp_...
  # Glider: mcp.DefaultGitHubStdioConfig() → Manager.Connect
  # Equivalent manual:
  docker run -i --rm -e GITHUB_PERSONAL_ACCESS_TOKEN ghcr.io/github/github-mcp-server

Never put the PAT in hoop YAML — use auth.token_env only.
Manual hosted verify: see planning/tools_mcp.md § ops verification checklist (NOT production-verified without live PAT).
`

// DefaultGitHubConfig returns a recommended ServerConfig for GitHub MCP HTTP.
// Token from GITHUB_PERSONAL_ACCESS_TOKEN, GITHUB_TOKEN, or GH_TOKEN (never commit).
func DefaultGitHubConfig() ServerConfig {
	env := ResolveGitHubTokenEnv()
	return ServerConfig{
		ID:        "github",
		Name:      "GitHub MCP",
		Transport: TransportHTTP,
		URL:       GitHubRemoteURL,
		Auth: AuthConfig{
			Kind:     AuthEnv,
			TokenEnv: env,
		},
		Toolsets: []string{"context", "repos", "issues", "pull_requests", "code_security"},
		Enabled:  true,
	}
}

// DefaultGitHubStdioConfig runs the official server via Docker stdio.
func DefaultGitHubStdioConfig() ServerConfig {
	env := ResolveGitHubTokenEnv()
	return ServerConfig{
		ID:        "github-stdio",
		Name:      "GitHub MCP (docker stdio)",
		Transport: TransportStdio,
		Command:   "docker",
		Args:      []string{"run", "-i", "--rm", "-e", env, GitHubDockerImage},
		Auth: AuthConfig{
			Kind:     AuthEnv,
			TokenEnv: env,
		},
		Toolsets: []string{"context", "repos", "code_security"},
		Enabled:  true,
	}
}

// GitHubToolCatalog returns a documented subset of GitHub MCP tools for stubs / UI.
func GitHubToolCatalog(toolsets []string) []Tool {
	schema := json.RawMessage(`{"type":"object","properties":{"owner":{"type":"string"},"repo":{"type":"string"}}}`)
	all := map[string][]Tool{
		"context": {
			{Name: "get_me", Description: "GitHub authenticated user context", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		},
		"repos": {
			{Name: "get_file_contents", Description: "Read a file from a repository", InputSchema: schema},
			{Name: "list_commits", Description: "List commits for a repository", InputSchema: schema},
			{Name: "search_code", Description: "Search code on GitHub", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
		},
		"issues": {
			{Name: "list_issues", Description: "List repository issues", InputSchema: schema},
		},
		"pull_requests": {
			{Name: "list_pull_requests", Description: "List pull requests", InputSchema: schema},
		},
		"code_security": {
			{Name: "list_code_scanning_alerts", Description: "List code scanning alerts", InputSchema: schema},
		},
	}
	if len(toolsets) == 0 {
		toolsets = []string{"context", "repos", "issues", "pull_requests", "code_security"}
	}
	var out []Tool
	seen := map[string]bool{}
	for _, ts := range toolsets {
		for _, t := range all[ts] {
			if seen[t.Name] {
				continue
			}
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	return out
}
