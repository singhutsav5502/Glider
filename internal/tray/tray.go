// Package tray puts Glider in the system tray when it starts, with a
// right-click "Exit" option, matching how a background service that
// otherwise has no visible window should still be easy to find and stop.
// Windows-only for now (like internal/mitm's transparent redirector and
// internal/procinfo) — see tray_windows.go for the real implementation
// and tray_other.go for the non-Windows stub, split via build tags so a
// Linux/macOS build never even compiles getlantern/systray's platform
// code (which needs CGO + GTK dev headers on Linux) — this repo's own
// GOOS=linux cross-compile check would otherwise break.
package tray

import _ "embed"

// Icon is Glider's tray icon — a simple two-tone paper-plane silhouette
// (tools/geniconpaperplane generates it; re-run that tool rather than
// hand-editing the .ico). Multi-resolution ICO (16/24/32/48/64/256), so
// Windows picks whichever size actually renders best.
//
//go:embed icon.ico
var Icon []byte

// Run starts the tray (Windows) or, on platforms without one, runs
// onReady synchronously and blocks until Quit is called — see
// tray_other.go. onReady is where the caller should set up the menu
// (SetupMenu) and kick off its own background work; onExit runs once
// shutdown begins, after Run's blocking call returns control.
func Run(onReady, onExit func()) { platformRun(onReady, onExit) }

// SetupMenu installs the icon and two menu items: "Open Dashboard"
// (calling onOpenDashboard) and "Exit" (calling onExitClick) — a no-op on
// platforms with no tray (tray_other.go), since there's no menu to click
// there in the first place. getlantern/systray exposes no left-click
// handler for the icon itself (only per-menu-item click channels), so
// "Open Dashboard" is a menu item, not a single-click gesture.
func SetupMenu(onOpenDashboard, onExitClick func()) { platformSetupMenu(onOpenDashboard, onExitClick) }

// Quit tears the tray down — call once the caller's own graceful shutdown
// (servers stopped, resources closed) has actually finished, so the icon
// disappears only after it's safe to, not the instant "Exit" is clicked.
func Quit() { platformQuit() }
