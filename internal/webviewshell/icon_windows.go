//go:build windows

package webviewshell

import (
	"os"
	"sync"
	"syscall"
	"unsafe"

	"github.com/glider-ai/glider/internal/tray"
)

// webview_go has no method to set the icon of a window. Window() returns only
// the raw HWND, which is an unsafe.Pointer. The contract in webview.h confirms
// that this value is the native handle.
//
// setWindowIcon closes that gap with the standard sequence of Win32:
//
//  1. LoadImageW reads the .ico bytes that tray.Icon already contains, and
//     those bytes already hold more than one resolution. Windows then selects
//     and scales the correct frame. This code does not read the ICO container
//     itself.
//  2. WM_SETICON applies the icon to this one window.
//
// LoadImageW needs a true path to a file, and it has no form that takes raw
// bytes. Therefore this code writes the bytes to a temporary file one time for
// each process, and each window uses that file.
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

// setWindowIcon gives the best result that it can. A window that opens with the
// default icon of WebView2, and not with the icon of Glider, is a difference in
// appearance only.
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
