package mcp

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/glider-ai/glider/internal/fileacl"
)

const githubCredFileName = "github_token"

// CredentialsDir returns ~/.glider/credentials (or GLIDER_HOME/credentials).
func CredentialsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if v := strings.TrimSpace(os.Getenv("GLIDER_HOME")); v != "" {
		return filepath.Join(v, "credentials"), nil
	}
	return filepath.Join(home, ".glider", "credentials"), nil
}

func githubCredPath() (string, error) {
	dir, err := CredentialsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, githubCredFileName), nil
}

// LoadGitHubTokenFile reads a previously saved PAT/OAuth token (never logged).
func LoadGitHubTokenFile() (string, error) {
	p, err := githubCredPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SaveGitHubToken persists token to disk (0600) and sets process env so
// existing AuthEnv resolution picks it up without restart.
func SaveGitHubToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("empty github token")
	}
	dir, err := CredentialsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p, err := githubCredPath()
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	// This operation can fail, and that is acceptable. It gives a true limit with
	// an ACL, and that limit is more than the mode bits 0o600 above. Refer to the
	// comment on internal/fileacl for the cause: Windows does not enforce that mode
	// alone.
	//
	// The code writes a failure to the log, and that failure is not fatal. The token
	// is already safe on the disk.
	if err := fileacl.RestrictToCurrentUser(p); err != nil {
		slog.Default().Warn("mcp: could not restrict GitHub token file ACL", "path", p, "err", err)
	}
	_ = os.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", token)
	return nil
}

// ClearGitHubToken deletes the file with the credential. It also removes the
// environment variables of this process that Glider can have set from the user
// interface.
//
// It does not remove an environment variable of the user that has no relation
// to this work. That condition occurs when the file was empty, or when no
// person saved a token. This operation gives the best result that it can.
func ClearGitHubToken() error {
	p, err := githubCredPath()
	if err != nil {
		return err
	}
	_ = os.Remove(p)
	for _, k := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		_ = os.Unsetenv(k)
	}
	return nil
}

// HydrateGitHubTokenFromStore loads ~/.glider/credentials/github_token into the
// process env when no GitHub token env is already set. Call at process start.
func HydrateGitHubTokenFromStore() (bool, error) {
	for _, k := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if strings.TrimSpace(lookupEnv(k)) != "" {
			return false, nil
		}
	}
	tok, err := LoadGitHubTokenFile()
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if tok == "" {
		return false, nil
	}
	_ = os.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", tok)
	return true, nil
}

// GitHubTokenSource describes where the token came from (never the secret).
func GitHubTokenSource() string {
	for _, k := range []string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if strings.TrimSpace(lookupEnv(k)) != "" {
			// Prefer reporting file if env was hydrated from file and matches.
			if k == "GITHUB_PERSONAL_ACCESS_TOKEN" {
				if t, err := LoadGitHubTokenFile(); err == nil && t != "" {
					return "credentials_file"
				}
			}
			return "env:" + k
		}
	}
	if t, err := LoadGitHubTokenFile(); err == nil && t != "" {
		return "credentials_file"
	}
	return ""
}
