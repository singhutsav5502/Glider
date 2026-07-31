// Package runstate detects whether the previous Glider run shut down
// cleanly. A crash or an external forceful kill (taskkill /F, SIGKILL)
// runs no Go code at all — nothing in the dying process can log a
// warning, revert a transparent redirect, or tell anyone anything at the
// moment it happens. The only place that gap can ever be surfaced is
// retrospectively, on the NEXT startup. See MITM_NETWORK.md / the
// 2026-07-30 incident this closes: a killed instance left an orphaned
// delegate subprocess running and (on Linux, per redirector_linux.go's
// own doc comment) can leave a stale iptables REDIRECT rule active
// indefinitely — a live operator had no signal that either had happened
// until they went looking for it themselves.
package runstate

import (
	"os"
	"path/filepath"
	"time"
)

// markerPath returns ~/.glider/run.marker (or ./.glider/run.marker when
// the home directory can't be resolved — same fallback convention as
// internal/metrics/history.go and internal/contextgraph/graph.go).
func markerPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".glider", "run.marker")
	}
	return filepath.Join(home, ".glider", "run.marker")
}

// MarkStarted writes the marker file, timestamped, at process startup —
// call this before starting the proxy/MITM/dashboard. Its mere presence
// on a LATER startup (see WasUncleanShutdown) is the whole signal; content
// is just for a human peeking at the file directly.
func MarkStarted() error {
	path := markerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("glider started "+time.Now().Format(time.RFC3339)+"\n"), 0o644)
}

// MarkStoppedCleanly removes the marker — call this only after every
// Shutdown() call in the normal exit sequence has returned. A run that
// never reaches this point (crash, forceful kill) leaves the marker
// behind for WasUncleanShutdown to find on the next startup.
func MarkStoppedCleanly() error {
	err := os.Remove(markerPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// WasUncleanShutdown reports whether a marker from a previous run is still
// present — call this at startup, before MarkStarted (which would
// otherwise immediately overwrite the evidence). Not an error by itself
// (os.Stat failing for any reason, including "no such file", just means
// "no evidence of an unclean shutdown" — this is a best-effort diagnostic
// signal, not a correctness gate, so a filesystem hiccup here should never
// block startup).
func WasUncleanShutdown() bool {
	_, err := os.Stat(markerPath())
	return err == nil
}
