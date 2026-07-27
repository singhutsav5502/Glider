//go:build !windows

package webviewshell

import (
	"fmt"
	"os/exec"
	"runtime"
)

// platformShow has no native-window implementation outside Windows yet
// (same scope boundary as internal/tray and internal/mitm's transparent
// redirector) — falls back to opening the system default browser, which
// is exactly what a user without this package would have done manually
// anyway, so "no window support here" degrades to the pre-existing
// behavior rather than an error.
func platformShow(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("webviewshell: no way to open a browser on GOOS=%s", runtime.GOOS)
	}
	return cmd.Start()
}
