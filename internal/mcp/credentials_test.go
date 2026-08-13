package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadClearGitHubToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GLIDER_HOME", dir)
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")

	if GitHubTokenPresent() {
		// Clear env from parent process leakage into Setenv empty — Unsetenv
		_ = os.Unsetenv("GITHUB_PERSONAL_ACCESS_TOKEN")
		_ = os.Unsetenv("GITHUB_TOKEN")
		_ = os.Unsetenv("GH_TOKEN")
	}
	if err := SaveGitHubToken("ghp_test_token_xyz"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "credentials", "github_token")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	if !GitHubTokenPresent() {
		t.Fatal("expected token present after save")
	}
	got, err := LoadGitHubTokenFile()
	if err != nil || got != "ghp_test_token_xyz" {
		t.Fatalf("load=%q err=%v", got, err)
	}
	if src := GitHubTokenSource(); src != "credentials_file" && src != "env:GITHUB_PERSONAL_ACCESS_TOKEN" {
		t.Fatalf("source=%q", src)
	}
	if err := ClearGitHubToken(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("file should be gone: %v", err)
	}
}

func TestHydrateGitHubTokenFromStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GLIDER_HOME", dir)
	_ = os.Unsetenv("GITHUB_PERSONAL_ACCESS_TOKEN")
	_ = os.Unsetenv("GITHUB_TOKEN")
	_ = os.Unsetenv("GH_TOKEN")
	cred := filepath.Join(dir, "credentials")
	if err := os.MkdirAll(cred, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cred, "github_token"), []byte("ghp_from_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := HydrateGitHubTokenFromStore()
	if err != nil || !ok {
		t.Fatalf("hydrate ok=%v err=%v", ok, err)
	}
	if os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN") != "ghp_from_file" {
		t.Fatalf("env=%q", os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN"))
	}
}

func TestListToolsOrCatalogMessage(t *testing.T) {
	m := NewManager()
	if err := m.Configure(DefaultGitHubConfig()); err != nil {
		t.Fatal(err)
	}
	res, err := m.ListToolsOrCatalogResult(t.Context(), "github")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "catalog" {
		t.Fatalf("source=%s", res.Source)
	}
	if res.Message == "" || res.HealthError == "" {
		t.Fatalf("expected message+health_error: %+v", res)
	}
	if len(res.Tools) == 0 {
		t.Fatal("expected catalog tools")
	}
}
