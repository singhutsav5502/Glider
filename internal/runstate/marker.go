// Package runstate finds if the previous run of Glider stopped cleanly.
//
// A failure, or a forceful stop from outside such as taskkill /F or SIGKILL,
// runs no Go code. Therefore no code in the process that stops can write a
// warning, remove a transparent redirect, or tell a person anything at that
// moment.
//
// The only position that can show that gap is the NEXT start, after the event.
//
// Refer to MITM_NETWORK.md and to the incident on 2026-07-30 that this closes.
// An instance that a person stopped left a delegate subprocess with no parent.
// On Linux it can also leave an old REDIRECT rule of iptables active with no
// limit. Refer to the comment in redirector_linux.go. A live operator had no
// signal of either event, until that person searched for it.
package runstate

import (
	"os"
	"path/filepath"
	"time"
)

// markerPath gives ~/.glider/run.marker. It gives ./.glider/run.marker when the
// code cannot find the home directory. That is the same convention as
// internal/metrics/history.go and internal/contextgraph/graph.go.
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

// WasUncleanShutdown says if a marker from a previous run is still present.
//
// Call it at the start, before MarkStarted. MarkStarted writes over the evidence
// immediately.
//
// A failure of os.Stat is not an error by itself, and this includes "no such
// file". It only means "there is no evidence of a stop that was not clean". This
// is a diagnostic that operates when it can.
func WasUncleanShutdown() bool {
	_, err := os.Stat(markerPath())
	return err == nil
}
