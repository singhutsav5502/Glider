package vendors

// LaunchInteractive starts the native interactive mode of v, which is its TUI,
// in a new console window that a person can see. That window is separate from
// the process of Glider.
//
// This is not a relay, and the code captures no output. The person controls that
// window directly.
//
// This is more simple than Path B, and that is on purpose. Refer to
// planning/permission_relay_design.md §3, which no person has built. There is no
// pty, and there is no correlation back into a conversation of Glider. This
// function only gives a true terminal to the person, already in the correct
// directory.
//
// If extraArgs is not empty, the code adds those args to the plain call of the
// binary.
//
// The button "Launch Interactive" on the dashboard gives no extra args. That
// gives a plain session with nothing in it, and it runs the binary in the same
// way that all three vendors run it with no print flag.
//
// The branch for the "interactive" Mode in ResolveDelegate gives the args of a
// CommandTemplate, after the code puts the values in them. That gives the
// session a first task.
//
// The CLI of each vendor already accepts this. claude and cursor-agent both take
// a plain positional "[prompt]", and that starts the interactive mode with that
// first message. A person confirmed this with the --help of each CLI on
// 2026-07-28. agy has no positional form, and it uses an explicit
// --prompt-interactive flag for the same result. A live test confirmed that
// also.
func LaunchInteractive(v Vendor, cwd string, extraArgs ...string) error {
	return launchInteractive(v, cwd, extraArgs...)
}

// LaunchInteractiveFunc is the seam ResolveDelegate's "interactive" Mode
// branch calls through, rather than calling LaunchInteractive directly —
// a package-level var so tests can substitute a non-spawning stub instead
// of actually opening a real detached OS console window (Windows'
// implementation uses cmd.Start(), fire-and-forget; there is no clean way
// to prevent or await a real window from an automated test).
var LaunchInteractiveFunc = LaunchInteractive
