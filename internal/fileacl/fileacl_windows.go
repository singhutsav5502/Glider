//go:build windows

package fileacl

import (
	"fmt"
	"os/exec"
	"os/user"

	"github.com/glider-ai/glider/internal/procutil"
)

// restrictToCurrentUser shells out to icacls (present on every Windows
// install since XP, no new dependency) rather than reimplementing
// Windows' security-descriptor APIs directly — a well-established,
// standard technique for this exact problem.
//
//	/inheritance:r strips inherited ACEs — these are what usually grant
//	    broader-than-intended access in the first place (typically
//	    whatever the parent directory's own ACL allows, often a local
//	    "Users" or "Authenticated Users" group).
//	/grant:r REPLACES the explicit grant list with exactly one entry,
//	    rather than adding to whatever was already explicitly granted.
//
// Targets the user by SID (user.Current().Uid on Windows — Go's os/user
// package returns the SID string there, not a UID number), prefixed with
// "*" (icacls' own syntax for "this token is a SID, not a name") —
// avoids any ambiguity from domain-qualified usernames or characters
// icacls' own name-parsing would need escaping for.
//
// A local Administrator can still recover the file in an emergency via
// Windows' own "take ownership" mechanism, which works regardless of an
// existing ACL — this restriction narrows *routine* access (other normal
// user accounts on a shared machine), not an admin's ultimate authority
// over the machine they administer.
func restrictToCurrentUser(path string) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("fileacl: resolve current user: %w", err)
	}
	cmd := exec.Command("icacls", path, "/inheritance:r", "/grant:r", "*"+u.Uid+":F")
	procutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fileacl: icacls %s: %w (output: %s)", path, err, out)
	}
	return nil
}
