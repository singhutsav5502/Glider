package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glider-ai/glider/internal/atomicfile"
)

func TestWriteFile_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	if err := atomicfile.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteFile_OverwritesExistingFileCompletely(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("this is the original, much longer content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(path, []byte("short"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The real bug this guards against: a shorter overwrite leaving
	// trailing bytes from the original file behind (which a naive
	// truncate-in-place done wrong could, though os.WriteFile itself
	// does not have this bug — this test exists for the rename-based
	// implementation specifically, to catch a regression toward a
	// non-atomic in-place approach).
	if string(got) != "short" {
		t.Fatalf("got %q, want exactly the new content with no leftover bytes", got)
	}
}

func TestWriteFile_NoTempFileLeftBehindOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := atomicfile.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly the target file in dir, got %v", entries)
	}
}
