//go:build !windows

package tray

// quitCh is closed by platformQuit to unblock platformRun — the
// non-Windows stand-in for systray.Run's own blocking contract, since
// there's no real tray event loop to block on here.
var quitCh = make(chan struct{})

func platformRun(onReady, onExit func()) {
	onReady()
	<-quitCh
	onExit()
}

// platformSetupMenu is a no-op — no tray, no menu to click. OS signals
// remain the only shutdown trigger on this platform.
func platformSetupMenu(onOpenDashboard, onExitClick func()) {}

func platformQuit() {
	close(quitCh)
}
