// Package tray puts Glider in the system tray at the start, with an "Exit"
// item in the menu of the right button. A service in the background has no
// window that a person can see. Therefore a person must still be able to find
// it and stop it easily.
//
// It operates on Windows only now. The transparent redirector in internal/mitm
// and the package internal/procinfo are the same. Refer to tray_windows.go for
// the true implementation, and to tray_other.go for the version that does
// nothing on other platforms.
//
// Build tags divide the two. Therefore a build for Linux or for macOS never
// compiles the platform code of getlantern/systray. That code needs CGO and the
// header files of GTK on Linux. Without this division, the cross-compile test
// for GOOS=linux in this repository would fail.
package tray

import _ "embed"

// Icon is Glider's tray icon — a simple two-tone paper-plane silhouette
// (tools/geniconpaperplane generates it; re-run that tool rather than
// hand-editing the .ico). Multi-resolution ICO (16/24/32/48/64/256), so
// Windows picks whichever size actually renders best.
//
//go:embed icon.ico
var Icon []byte

// Run starts the tray on Windows. On a platform with no tray, it runs onReady
// in sequence and then blocks, until code calls Quit. Refer to tray_other.go.
//
// In onReady, the caller must make the menu with SetupMenu, and must start its
// own work in the background. onExit operates one time when the shutdown
// starts, after the blocking call of Run returns.
func Run(onReady, onExit func()) { platformRun(onReady, onExit) }

// SetupMenu adds the icon and two items to the menu: "Open Dashboard", which
// calls onOpenDashboard, and "Exit", which calls onExitClick.
//
// It does nothing on a platform with no tray, in tray_other.go, because there
// is no menu for a person to use.
func SetupMenu(onOpenDashboard, onExitClick func()) { platformSetupMenu(onOpenDashboard, onExitClick) }

// Quit tears the tray down — call once the caller's own graceful shutdown
// (servers stopped, resources closed) has actually finished, so the icon
// disappears only after it is safe to, not the instant "Exit" is clicked.
func Quit() { platformQuit() }
