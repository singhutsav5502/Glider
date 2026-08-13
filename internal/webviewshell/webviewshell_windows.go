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

// platformShow makes a new window on its own thread of the operating system,
// and it locks that thread.
//
// The init() of webview_go already calls runtime.LockOSThread() for the first
// goroutine that uses it. Refer to the comment on that package. This code runs
// Show from a new goroutine each time. Therefore the locked thread stays
// separate from the thread with the message loop of internal/tray, and the two
// never compete for the message queue of one thread.
//
// A second call to Show, while a window is open, sends that window to the new
// address. It does not open a second window.
//
// webview_go has no call to put a window in front. Therefore the window that
// exists does not come to the front on a second click. But it always shows
// url.
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

	// Take the navigations that the Show calls of other goroutines put in the
	// queue.
	//
	// Dispatch is the documented method of webview_go to call safely into an
	// instance that operates, from a goroutine that is not the goroutine with the
	// message loop of Run().
	go func() {
		for u := range ch {
			target := u
			w.Dispatch(func() { w.Navigate(target) })
		}
	}()

	w.Navigate(url)
	w.Run() // blocks until the window is closed
}
