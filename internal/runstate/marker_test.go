package runstate

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHome redirects markerPath's os.UserHomeDir lookup by setting
// HOME/USERPROFILE for the duration of the test — the same env vars
// os.UserHomeDir itself reads, so no production code needs a test-only
// override hook just for this.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}

func TestWasUncleanShutdown_NoMarkerIsClean(t *testing.T) {
	withTempHome(t)
	if WasUncleanShutdown() {
		t.Fatal("expected no marker on a fresh temp home")
	}
}

func TestMarkStarted_ThenUncleanUntilStoppedCleanly(t *testing.T) {
	withTempHome(t)
	if err := MarkStarted(); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if !WasUncleanShutdown() {
		t.Fatal("expected marker present right after MarkStarted (simulates a crash before clean shutdown)")
	}
	if err := MarkStoppedCleanly(); err != nil {
		t.Fatalf("MarkStoppedCleanly: %v", err)
	}
	if WasUncleanShutdown() {
		t.Fatal("expected marker gone after MarkStoppedCleanly")
	}
}

func TestMarkStoppedCleanly_NoMarkerIsNotAnError(t *testing.T) {
	withTempHome(t)
	if err := MarkStoppedCleanly(); err != nil {
		t.Fatalf("MarkStoppedCleanly on a nonexistent marker should be a no-op, got: %v", err)
	}
}

func TestMarkStarted_CreatesGliderDir(t *testing.T) {
	home := withTempHome(t)
	if err := MarkStarted(); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".glider", "run.marker")); err != nil {
		t.Fatalf("expected marker file to exist: %v", err)
	}
}
