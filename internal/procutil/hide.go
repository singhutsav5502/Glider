// Package procutil holds small, OS-specific process-spawning helpers
// shared across the codebase — currently just one: suppressing the
// console window a child process would otherwise flash open.
package procutil

import "os/exec"

// HideWindow configures cmd so launching it doesn't flash a new console
// window — a no-op everywhere except Windows (see hide_windows.go).
//
// Exists because of a real, live-reported bug (2026-07-28): once
// cmd/glider/main.go started building glider.exe with -H=windowsgui (to
// stop glider.exe's OWN console from appearing), every child process it
// spawns without this — nvidia-smi on internal/vram's 5-second poll
// timer, most visibly — started getting its own fresh console window
// from Windows instead of silently sharing glider.exe's (now
// nonexistent) one. A GUI-subsystem parent process doesn't get to lean on
// "child processes just inherit my console" the way a console-subsystem
// one does; each child needs to opt out explicitly.
func HideWindow(cmd *exec.Cmd) {
	hideWindow(cmd)
}
