//go:build !windows

package procutil

import "os/exec"

// hideWindow is a no-op outside Windows — no console-window concept to
// suppress on platforms where a spawned process doesn't get one by default.
func hideWindow(cmd *exec.Cmd) {}
