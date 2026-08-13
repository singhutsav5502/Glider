//go:build windows

package vendors

import (
	"os/exec"
	"syscall"
)

// createNewConsole is CREATE_NEW_CONSOLE — not exported by the syscall
// package itself, so defined here from its documented Win32 value.
const createNewConsole = 0x00000010

// launchInteractive opens a brand-new, visible console window running
// v.Path directly (plus extraArgs, if any), via the CREATE_NEW_CONSOLE
// process-creation flag.
//
// Deliberately NOT the earlier "cmd /C start <title> <path> <args>" form
// (fixed 2026-07-28, security audit): extraArgs can carry a raw human
// chat message (a delegate's {{prompt}} substitution — see
// resolveInteractive in resume.go), and routing untrusted text through
// cmd.exe's own command-line parser is a real, if narrow, injection
// surface — cmd.exe re-parses the whole reconstructed command line for
// &, |, and %VAR% even when the caller believes it passed "separate"
// arguments, and reliably proving no crafted prompt can break out of its
// quoting is not something worth staking correctness on. Calling
// CreateProcess directly, which is what exec.Command already does inside
// passes each argument through untouched — no shell re-parses it, ever.
//
// Tradeoff: cmd's "start" also let a custom window title be set
// ("Glider: "+v.Name); a directly-launched console has no equivalent
// without attaching to the child's console from another process (its own
// real, if more exotic, risk) — the window just shows whatever title the
// target CLI or Windows itself assigns. Purely cosmetic, not worth the
// injection surface it used to cost.
func launchInteractive(v Vendor, cwd string, extraArgs ...string) error {
	cmd := exec.Command(v.Path, extraArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole}
	if cwd != "" {
		cmd.Dir = cwd
	}
	return cmd.Start()
}
