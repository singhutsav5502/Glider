//go:build windows

package vendors

import (
	"os/exec"

	"github.com/glider-ai/glider/internal/procutil"
)

// launchInteractive opens a brand-new, visible console window running
// v.Path (plus extraArgs, if any) via cmd.exe's own built-in "start" — the
// same mechanism proven live earlier this session via PowerShell's
// Start-Process (functionally identical: both ask the shell to spawn a
// detached console rather than reusing this process's own). No external
// dependency, no elevation needed.
func launchInteractive(v Vendor, cwd string, extraArgs ...string) error {
	// The window-title argument ("Glider: "+v.Name) is required whenever
	// the target path might contain spaces, or "start" misinterprets it
	// as the title itself.
	args := append([]string{"/C", "start", "Glider: " + v.Name, v.Path}, extraArgs...)
	cmd := exec.Command("cmd", args...)
	// Hides only this cmd.exe launcher's own transient console — "start"
	// still opens a real, separate, visible window for v.Path itself; that
	// window's visibility comes from "start", not from this process's own.
	procutil.HideWindow(cmd)
	if cwd != "" {
		cmd.Dir = cwd
	}
	return cmd.Start()
}
