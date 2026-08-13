// Package webviewshell puts the dashboard of Glider in a native window.
// Therefore a person does not have to open a tab in a browser.
//
// This is a thin shell around the SAME dashboard that the browser already uses.
// That is internal/dashboard, and HTTP serves it as before. This is not a
// different user interface. It uses 100% of the frontend in static/app.js. This
// package only adds a native window, and the content of that window is that
// same address.
//
// It operates on Windows only now. Refer to webviewshell_windows.go and
// webviewshell_other.go. This agrees with internal/tray and with the
// transparent redirector in internal/mitm, and the cause is the same.
//
// The library github.com/webview/webview_go needs the header files of GTK and
// WebKit to build on Linux, and the cross-compile test for GOOS=linux in this
// repository does not have them.
//
// On Windows it needs no additional header files, because it contains the C API
// of WebView2. It uses the WebView2 Runtime of Edge, which is already present
// by default on Windows 10 21H2 and later, and on Windows 11.
package webviewshell

// Show opens the dashboard of Glider, at url, in a native window. If a window
// is already open, it sends that window to url, and it does not open a second
// window.
//
// It returns immediately. The window operates on its own thread of the
// operating system, because the native objects of the graphic interface and of
// WebView2 need that.
func Show(url string) error {
	return platformShow(url)
}
