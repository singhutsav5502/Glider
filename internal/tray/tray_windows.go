//go:build windows

package tray

import "github.com/getlantern/systray"

func platformRun(onReady, onExit func()) {
	systray.Run(onReady, onExit)
}

func platformSetupMenu(onExitClick func()) {
	systray.SetIcon(Icon)
	systray.SetTitle("Glider")
	systray.SetTooltip("Glider — background AI CLI router")
	mExit := systray.AddMenuItem("Exit", "Stop Glider")
	go func() {
		<-mExit.ClickedCh
		onExitClick()
	}()
}

func platformQuit() {
	systray.Quit()
}
