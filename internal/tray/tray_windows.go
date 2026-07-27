//go:build windows

package tray

import "github.com/getlantern/systray"

func platformRun(onReady, onExit func()) {
	systray.Run(onReady, onExit)
}

func platformSetupMenu(onOpenDashboard, onExitClick func()) {
	systray.SetIcon(Icon)
	systray.SetTitle("Glider")
	systray.SetTooltip("Glider — background AI CLI router")
	mOpen := systray.AddMenuItem("Open Dashboard", "Show Glider's dashboard")
	systray.AddSeparator()
	mExit := systray.AddMenuItem("Exit", "Stop Glider")
	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				onOpenDashboard()
			case <-mExit.ClickedCh:
				onExitClick()
				return
			}
		}
	}()
}

func platformQuit() {
	systray.Quit()
}
