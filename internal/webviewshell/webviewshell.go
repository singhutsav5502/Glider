// Package webviewshell puts Glider's dashboard in a native window instead
// of requiring the user to open a browser tab — a thin shell around the
// SAME dashboard the browser already talks to (internal/dashboard, served
// over HTTP as before), not a separate UI. Reuses 100% of the existing
// static/app.js frontend; the only thing this package adds is a native
// window whose content is that same URL.
//
// Windows-only for now (see webviewshell_windows.go / webviewshell_other.go),
// matching internal/tray and internal/mitm's transparent redirector — same
// reasoning: the underlying library (github.com/webview/webview_go) needs
// GTK+WebKit dev headers to build on Linux, which this repo's own
// GOOS=linux cross-compile check doesn't have. On Windows it needs no
// extra headers (bundles the WebView2 C API surface itself) and uses the
// Edge WebView2 Runtime already present on Windows 10 21H2+/11 by default.
package webviewshell

// Show opens Glider's dashboard (at url) in a native window, or — if one
// is already open — navigates the existing window to url instead of
// opening a second one. Returns immediately; the window itself runs on
// its own dedicated OS thread (native GUI/WebView2 objects are
// thread-affine — see webviewshell_windows.go), independent of
// internal/tray's own message loop, so opening/closing this window never
// blocks or interferes with the tray icon.
func Show(url string) error {
	return platformShow(url)
}
