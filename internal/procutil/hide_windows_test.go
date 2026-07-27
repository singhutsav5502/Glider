//go:build windows

package procutil_test

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/glider-ai/glider/internal/procutil"
)

func TestHideWindow_SetsHideWindowAndCreateNoWindow(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "echo hi")
	procutil.HideWindow(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("expected SysProcAttr to be set")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("expected HideWindow=true")
	}
	const createNoWindow = 0x08000000
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("expected CREATE_NO_WINDOW set in CreationFlags, got %#x", cmd.SysProcAttr.CreationFlags)
	}
}

func TestHideWindow_PreservesExistingSysProcAttr(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "echo hi")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000001} // some pre-existing flag
	procutil.HideWindow(cmd)

	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("expected HideWindow=true")
	}
	if cmd.SysProcAttr.CreationFlags&0x00000001 == 0 {
		t.Fatal("expected pre-existing CreationFlags bit to survive")
	}
}
