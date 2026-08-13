// Package fileacl limits the ACCESS to a file with sensitive content. It does
// more than the permission bits that a person gives to os.WriteFile on Windows.
//
// Unix applies os.WriteFile(path, data, 0o600) exactly, because the file system
// enforces those permission bits. Windows is the primary platform of this
// project, and it has no direct equivalent. NTFS uses ACLs, and it does not use
// the mode bits of POSIX. The os package of Go does not change 0o600 into an
// equal ACL that limits access.
//
// A file that a person writes with 0o600 on Windows usually still takes the ACL
// of the directory above it. That ACL frequently permits a read by each local
// account. The value of the mode argument does not change this.
//
// The security audit on 2026-07-28 found this for two files with high
// consequences: the private key of the CA in internal/mitm, and the token for
// GitHub in internal/mcp/credentials.go. A person who takes that private key can
// do a MITM operation against each host that trusts the CA.
//
// Code already wrote both files with 0o600. The INTENT was correct. Only the
// platform that this project ships on did not enforce it.
package fileacl

// RestrictToCurrentUser limits the access to path, to the current user of the
// operating system only.
//
// On Windows it removes each ACE that the file takes from the directory above
// it, and it gives Full Control to the SID of this user only.
//
// On each other platform it does nothing. There the permission bits of the file
// system are the true and sufficient protection, and the os.WriteFile call that
// wrote path already applied them.
func RestrictToCurrentUser(path string) error {
	return restrictToCurrentUser(path)
}
