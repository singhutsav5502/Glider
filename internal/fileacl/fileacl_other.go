//go:build !windows

package fileacl

// restrictToCurrentUser is a no-op outside Windows — the filesystem's own
// permission bits (already applied by whatever os.WriteFile call wrote
// the file) are real, sufficient enforcement on Unix-like systems.
func restrictToCurrentUser(path string) error { return nil }
