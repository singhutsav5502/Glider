package vendors

// LaunchInteractive starts v's own native interactive/TUI mode in a new,
// visible OS console window, detached from Glider's own process — not a
// relay, not captured output; the human drives it directly from there.
// This is deliberately simpler than Path B
// (planning/permission_relay_design.md §3, still unbuilt): no pty
// plumbing, no correlation back into any Glider conversation — just
// handing the human a real terminal, already in the right directory.
//
// extraArgs, when non-empty, are appended to the bare binary invocation.
// The dashboard's "Launch Interactive" button passes none (a plain blank
// session, running the bare binary the way all three vendors run it when
// given no print flag at all). ResolveDelegate's "interactive" Mode
// branch passes a resolved CommandTemplate's substituted Args instead, to
// seed the session with an initial task — each vendor's own CLI already
// supports this natively: claude and cursor-agent both take a bare
// positional "[prompt]" that starts interactive mode already primed with
// that first message (confirmed via each CLI's own --help, 2026-07-28);
// agy has no positional form and instead uses an explicit
// --prompt-interactive flag for the same purpose (also confirmed live).
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
