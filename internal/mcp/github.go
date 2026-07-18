package mcp

import "encoding/json"

// GitHubRemoteURL is the hosted GitHub MCP endpoint (Copilot MCP).
// See https://github.com/github/github-mcp-server and docs/host-integration.md.
const GitHubRemoteURL = "https://api.githubcopilot.com/mcp/"

// GitHubDockerImage is the local stdio server image.
const GitHubDockerImage = "ghcr.io/github/github-mcp-server"

// DefaultGitHubConfig returns a recommended ServerConfig for GitHub MCP.
// Auth uses GITHUB_PERSONAL_ACCESS_TOKEN (never commit the token).
func DefaultGitHubConfig() ServerConfig {
	return ServerConfig{
		ID:        "github",
		Name:      "GitHub MCP",
		Transport: TransportHTTP,
		URL:       GitHubRemoteURL,
		Auth: AuthConfig{
			Kind:     AuthEnv,
			TokenEnv: "GITHUB_PERSONAL_ACCESS_TOKEN",
		},
		Toolsets: []string{"context", "repos", "issues", "pull_requests", "code_security"},
		Enabled:  true,
	}
}

// DefaultGitHubStdioConfig runs the official server via Docker stdio.
func DefaultGitHubStdioConfig() ServerConfig {
	return ServerConfig{
		ID:        "github-stdio",
		Name:      "GitHub MCP (docker stdio)",
		Transport: TransportStdio,
		Command:   "docker",
		Args:      []string{"run", "-i", "--rm", "-e", "GITHUB_PERSONAL_ACCESS_TOKEN", GitHubDockerImage},
		Auth: AuthConfig{
			Kind:     AuthEnv,
			TokenEnv: "GITHUB_PERSONAL_ACCESS_TOKEN",
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
