package mitm

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandPath expands leading ~ to the user home directory.
func ExpandPath(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

// DefaultCAPaths returns default CA cert/key paths under ~/.glider/mitm/.
func DefaultCAPaths() (certPath, keyPath string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "glider-mitm-ca.crt", "glider-mitm-ca.key"
	}
	dir := filepath.Join(home, ".glider", "mitm")
	return filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
}
