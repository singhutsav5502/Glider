//go:build windows

package webviewshell

import (
	"os"
	"sync"
	"syscall"
	"unsafe"

	"github.com/glider-ai/glider/internal/tray"
)

// webview_go exposes no way to set a window's icon directly — Window()
// only returns the raw HWND (unsafe.Pointer, confirmed by webview.h's own
// contract to be the native handle). setWindowIcon fills that gap with
// the standard Win32 sequence: load tray.Icon's already-embedded,
// already-multi-resolution .ico bytes via LoadImageW (letting Windows'
// own icon loader pick/scale the right embedded frame, rather than
// hand-parsing the ICO container ourselves), then WM_SETICON to apply it
// to this specific window. LoadImageW needs a real file path (no raw-bytes
// overload exists), so the embedded bytes are written to a temp file once
// per process and reused for every window.
var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procLoadImageW   = user32.NewProc("LoadImageW")
	procSendMessageW = user32.NewProc("SendMessageW")
)

const (
	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	wmSetIcon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
)

var (
	iconTempPath string
	iconTempOnce sync.Once
	iconTempErr  error
)

func iconFilePath() (string, error) {
	iconTempOnce.Do(func() {
		f, err := os.CreateTemp("", "glider-icon-*.ico")
		if err != nil {
			iconTempErr = err
			return
		}
		defer f.Close()
		if _, err := f.Write(tray.Icon); err != nil {
			iconTempErr = err
			return
		}
		iconTempPath = f.Name()
	})
	return iconTempPath, iconTempErr
}

// setWindowIcon is best-effort — a window that opens without Glider's own
// icon (falling back to WebView2's generic default) is a cosmetic gap,
// never worth failing the whole window over.
func setWindowIcon(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	path, err := iconFilePath()
	if err != nil {
		return
	}
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	hIconBig, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathPtr)), imageIcon, 32, 32, lrLoadFromFile)
	if hIconBig != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, hIconBig)
	}
	hIconSmall, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(pathPtr)), imageIcon, 16, 16, lrLoadFromFile)
	if hIconSmall != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, hIconSmall)
	}
}
