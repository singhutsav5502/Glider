// Package atomicfile gives a replacement for os.WriteFile that is safe against
// a failure, when the code writes over a file that already has content of
// value.
//
// A plain os.WriteFile makes the target file empty before it writes the new
// bytes. Therefore a process that stops in the middle of the write leaves a
// file that is short and frequently incorrect. That stop can be a failure, a
// forced taskkill, or a loss of power.
//
// This package exists because of a true finding of the security and reliability
// audit on 2026-07-28.
//
// internal/vendors/agy_grant.go writes the TRUE external settings.json of agy
// with os.WriteFile. It does this two times for each resume that gives a
// permission: one time to add a rule with a small scope, and one time to remove
// that rule. It had no protection against a failure.
//
// A failure in that short period does not only lose the state of Glider. It
// damages a config file that the agy CLI of the user needs, and Glider cannot
// repair that file.
//
// SaveRegistry in internal/vendors, which writes vendors.json, had the same gap
// for the state of Glider itself.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile writes data to path in two steps. First it writes a temporary file
// in the SAME directory. Therefore the rename at the end is on the same file
// system, and thus it is atomic on Windows and on Unix. Then it renames that
// file over path. This method never leaves path with only a part of the
// content.
//
// The code applies perm to the temporary file before the rename. Therefore the
// final file has the same mode as a direct call to os.WriteFile with the same
// perm.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".atomicfile-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Any return path below that does not reach the final, successful
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
