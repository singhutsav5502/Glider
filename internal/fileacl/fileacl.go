// Package fileacl restricts a sensitive file's ACCESS beyond what a
// standard os.WriteFile permission-bits argument achieves on Windows.
//
// Go's os.WriteFile(path, data, 0o600) is honored literally on Unix (real
// enforcement via the filesystem's own permission bits), but on Windows —
// this project's primary target — there is no direct equivalent: NTFS
// uses ACLs, not POSIX mode bits, and Go's os package does not translate
// 0o600 into an equivalent restrictive ACL. A file written with 0o600 on
// Windows typically still inherits whatever broader access its parent
// directory's ACL already grants (often readable by any local account),
// regardless of the mode argument's intent.
//
// Found in the 2026-07-28 security audit for two genuinely
// high-consequence files: internal/mitm's CA private key (a compromise
// lets an attacker MITM every host the CA is trusted for) and the GitHub
// token (internal/mcp/credentials.go). Both were already written with
// 0o600 — correct INTENT, just not enforced on the platform this ships
// on.
package fileacl

// RestrictToCurrentUser narrows path's access to the current OS user only
// (Windows: strips inherited ACEs and grants Full Control to just this
// user's SID; a no-op elsewhere, where the filesystem's own permission
// bits — already applied by the os.WriteFile call that wrote path — are
// the real, sufficient enforcement). Best-effort: a failure here doesn't
// un-write the file or change what was already persisted, so callers
// should log a failure, not treat it as fatal to whatever operation just
// wrote the file.
func RestrictToCurrentUser(path string) error {
	return restrictToCurrentUser(path)
}
