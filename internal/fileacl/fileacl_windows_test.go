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
// would not catch a real syntax/argument mistake in the command built here.
func TestRestrictToCurrentUser_RealICACLS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(path, []byte("shh"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := fileacl.RestrictToCurrentUser(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The current process runs as the same user that just got exclusive
	// access. Therefore it must still be able to read the file.
	//
	// An incorrect SID, or an icacls flag with a spelling error, gives one
	// exact failure: the code limits the file, and then no person can read
	// it, and this includes the owner.
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
