//go:build windows

package fileacl_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glider-ai/glider/internal/fileacl"
)

// TestRestrictToCurrentUser_RealICALCS actually shells out to icacls
// against a real temp file — not mocked — since the whole point of this
// package is a real OS-level ACL change; a test that stubs out icacls
// wouldn't catch a real syntax/argument mistake in the command built here.
func TestRestrictToCurrentUser_RealICACLS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(path, []byte("shh"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := fileacl.RestrictToCurrentUser(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The current process (running as the same user that just got
	// exclusive access) must still be able to read the file — the exact
	// failure mode a wrong SID or a typo'd icacls flag would produce is
	// "restricted the file so nobody, including us, can read it anymore."
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file unreadable after restricting ACL: %v", err)
	}
	if string(got) != "shh" {
		t.Fatalf("got %q", got)
	}
}

func TestRestrictToCurrentUser_MissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.txt")
	if err := fileacl.RestrictToCurrentUser(path); err == nil {
		t.Fatal("expected an error for a nonexistent file")
	}
}
