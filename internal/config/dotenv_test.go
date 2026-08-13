package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvFiles(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	localPath := filepath.Join(dir, ".env.local")
	if err := os.WriteFile(envPath, []byte("GLIDER_TEST_A=from_env\n# comment\nGLIDER_TEST_B=b1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, []byte("GLIDER_TEST_B=b2\nGLIDER_TEST_C=c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GLIDER_TEST_A", "already")
	_ = os.Unsetenv("GLIDER_TEST_B")
	_ = os.Unsetenv("GLIDER_TEST_C")

	loaded, err := LoadDotEnvFiles(envPath, localPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded=%v", loaded)
	}
	if os.Getenv("GLIDER_TEST_A") != "already" {
		t.Fatalf("should not override preexisting shell: %q", os.Getenv("GLIDER_TEST_A"))
	}
	if os.Getenv("GLIDER_TEST_B") != "b2" {
		t.Fatalf("B=%q want b2 (.env.local overrides .env)", os.Getenv("GLIDER_TEST_B"))
	}
	if os.Getenv("GLIDER_TEST_C") != "c" {
		t.Fatalf("C=%q", os.Getenv("GLIDER_TEST_C"))
	}
}
