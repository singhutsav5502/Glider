//go:build !windows

package procutil

import "os/exec"

// AssignToKillOnCloseJob is a no-op outside Windows — Job Objects are a
// Windows-specific primitive. Linux's equivalent gap (a crashed Glider
// leaving orphaned subprocesses, or a stale iptables REDIRECT rule) is
// already called out in redirector_linux.go's own doc comment as a real,
// separately-tracked risk; process-group + PR_SET_PDEATHSIG parity for
// spawned delegate subprocesses is future work, not implemented here.
func AssignToKillOnCloseJob(cmd *exec.Cmd) error { return nil }

// IsInDelegateSubprocessJob is always false outside Windows — no Job
// Object membership exists to check. This means Linux does not yet get the
// Path B "skip the local-fulfillment wait for a known delegate
// subprocess" optimization (internal/mitm/intercept.go's
// tryRunSSEFulfill) — a real, known gap, not a silent correctness issue:
// the fallback behavior (treat delegate-subprocess traffic like any other
// front-CLI traffic) still produces a correct result, just with the same
// unnecessary wait this Windows-specific fix removes there.
func IsInDelegateSubprocessJob(uint32) bool { return false }
