// Package atomicfile provides a crash-safe replacement for os.WriteFile
// when overwriting a file that already has meaningful content — a plain
// os.WriteFile truncates the target before writing the new bytes, so a
// process killed mid-write (crash, forced taskkill, power loss) leaves a
// truncated, often invalid file behind.
//
// Exists because of a real finding (2026-07-28 security/reliability
// audit): internal/vendors/agy_grant.go writes agy's REAL, external
// settings.json directly via os.WriteFile — twice per permission-grant
// resume (once to add a scoped allow-rule, once to revert it back) — with
// no atomicity at all. A crash in that narrow window doesn't just lose
// Glider's own state; it corrupts a config file the user's own agy CLI
// depends on, entirely outside Glider's control to repair. internal/vendors's
// own SaveRegistry (vendors.json) had the identical gap for Glider's own
// state.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile writes data to path by first writing to a temp file in the
// SAME directory (so the final rename is same-filesystem, hence atomic on
// both Windows and Unix) and renaming it over path — never leaves path
// partially written. perm is applied to the temp file before the rename,
// so the final file has the same mode as a direct os.WriteFile(path,
// data, perm) call would have produced.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".atomicfile-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Any return path below that doesn't reach the final, successful
	// os.Rename must clean up the temp file — os.Remove on an
	// already-renamed-away path is a harmless no-op error, ignored.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	success = true
	return nil
}
