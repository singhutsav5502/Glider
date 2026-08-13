//go:build windows

package fileacl

import (
	"fmt"
	"os/exec"
	"os/user"

	"github.com/glider-ai/glider/internal/procutil"
)

// restrictToCurrentUser calls icacls in a shell. Each Windows installation
// since XP has icacls, therefore this adds no new dependency. This code does not
// use the APIs for a security descriptor of Windows directly. To call icacls is
// a standard method for this exact problem.
//
//	/inheritance:r removes each ACE that the file takes from the directory
//	    above it. Those ACEs usually give the access that is wider than the
//	    intent. They usually come from the ACL of the parent directory, and
//	    they frequently name a local group such as "Users" or "Authenticated
//	    Users".
//	/grant:r REPLACES the list of explicit permissions with exactly one
//	    entry. It does not add an entry to the list that exists.
//
// The code names the user by SID. On Windows, user.Current().Uid gives the text
// of the SID, and the os/user package of Go does not give a UID number there.
// The code adds "*" before the SID, which is the syntax of icacls for "this
// value is a SID, and it is not a name".
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
