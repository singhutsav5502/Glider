//go:build windows

package webviewshell

import (
	"runtime"
	"sync"

	"github.com/webview/webview_go"
)

var (
	mu      sync.Mutex
	current webview.WebView // nil when no window is open
	navCh   chan string     // set while a window is open; buffered 1, drained by its own goroutine
)

// platformShow creates a new window on its own dedicated, locked OS thread
// (webview_go's own init() already does runtime.LockOSThread() for
// whichever goroutine first touches it — see that package's doc comment;
// running Show from a fresh goroutine each time keeps that locked thread
// isolated from internal/tray's own message-loop thread, so the two never
// contend for the same thread's message queue). A second Show call while
// a window is already open re-navigates that window instead of opening a
// duplicate — webview_go exposes no "bring to front" call, so the
// existing window doesn't visually raise itself on a repeat click; it
// does, at least, always end up showing url.
func platformShow(url string) error {
	mu.Lock()
	if current != nil {
		ch := navCh
		mu.Unlock()
		ch <- url
		return nil
	}
	ch := make(chan string, 1)
	navCh = ch
	mu.Unlock()

	go runWindow(url, ch)
	return nil
}

func runWindow(url string, ch chan string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	w := webview.New(false)
	defer func() {
		mu.Lock()
		current = nil
		navCh = nil
		mu.Unlock()
		close(ch) // lets the drain goroutine below exit instead of leaking
		w.Destroy()
	}()

	mu.Lock()
	current = w
	mu.Unlock()

	w.SetTitle("Glider")
	w.SetSize(1100, 760, webview.HintNone)
	setWindowIcon(uintptr(w.Window()))

	// Drain queued navigations from other goroutines' Show calls —
	// Dispatch is webview_go's own documented way to safely call into a
	// running instance from a different goroutine than the one running
	// Run()'s message loop. A Dispatch racing the exact moment the window
	// closes is a known, accepted gap (low-probability, not
	// safety-critical for a dashboard shortcut) rather than something
	// worth extra synchronization for.
	go func() {
		for u := range ch {
			target := u
			w.Dispatch(func() { w.Navigate(target) })
		}
	}()

	w.Navigate(url)
	w.Run() // blocks until the window is closed
}
