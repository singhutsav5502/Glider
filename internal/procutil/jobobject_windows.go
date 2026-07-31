//go:build windows

package procutil

import (
	"errors"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errProcessNotStarted = errors.New("procutil: cmd.Process is nil — AssignToKillOnCloseJob must be called after cmd.Start()")

// killOnCloseJob is a single Windows Job Object, created lazily and shared
// for the life of this process, with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
// set — Windows automatically terminates every process still assigned to
// it the instant this job's last handle closes, which happens
// unconditionally when glider.exe's own process object is torn down, for
// ANY reason: normal exit, a crash, or an external forceful kill
// (`taskkill /F`, the exact case a graceful-shutdown code path can never
// run for, since no Go code executes when a process is forcibly
// terminated). This is the only mechanism that actually closes that gap —
// context cancellation (exec.CommandContext) only helps when Glider's own
// process is still alive to run the cancellation, which by definition
// isn't true for a forceful kill.
//
// Real, live-confirmed incident this fixes (2026-07-30): a delegate call's
// spawned cursor-agent subprocess kept running and wrote its edit to disk
// after the parent glider.exe had already been force-killed mid-request —
// an orphaned side effect nobody was left to report or correlate back to
// the request that triggered it.
var (
	killOnCloseJobOnce   sync.Once
	killOnCloseJobHandle windows.Handle
	killOnCloseJobErr    error
)

func getKillOnCloseJob() (windows.Handle, error) {
	killOnCloseJobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			killOnCloseJobErr = err
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		_, err = windows.SetInformationJobObject(
			h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		)
		if err != nil {
			_ = windows.CloseHandle(h)
			killOnCloseJobErr = err
			return
		}
		killOnCloseJobHandle = h
	})
	return killOnCloseJobHandle, killOnCloseJobErr
}

// AssignToKillOnCloseJob assigns cmd's already-started process to the
// shared kill-on-close job — must be called after cmd.Start(), while
// cmd.Process is populated. Deliberately for HEADLESS delegate subprocess
// execs only (RunWithOptions), never for LaunchInteractive: an interactive
// session is explicitly handed off to the user as a standalone window
// ("Glider does not capture or relay anything from this session" —
// resolveInteractive's own doc comment), and killing a real, live user
// session just because Glider happened to restart would be destructive,
// not a safety improvement. Returns an error the caller may choose to log
// and otherwise ignore — a failed enrollment leaves the pre-2026-07-31
// behavior (orphan on forceful kill) rather than blocking the delegate
// call outright, since an already-denied/failed job assignment is a
// hardening gap, not a correctness one.
func AssignToKillOnCloseJob(cmd *exec.Cmd) error {
	job, err := getKillOnCloseJob()
	if err != nil {
		return err
	}
	if cmd.Process == nil {
		return errProcessNotStarted
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.AssignProcessToJobObject(job, handle)
}

var (
	kernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procIsProcessInJob = kernel32.NewProc("IsProcessInJob")
)

// IsInDelegateSubprocessJob reports whether pid is a member of the shared
// kill-on-close job — true for a delegate subprocess (AssignToKillOnCloseJob
// was called for it) AND for any of ITS OWN descendants, since Windows
// automatically adds a job member's child processes to the same job
// (unless that child explicitly opts out via CREATE_BREAKAWAY_FROM_JOB,
// which none of Glider's spawned vendor CLIs do). This is why this check,
// not a flat "does pid equal the PID cmd.Start() returned" comparison, is
// the correct one: a vendor CLI invoked via a wrapper script (confirmed
// live for cursor-agent — cmd.Process.Pid and the real node.exe process
// making the actual network calls are different PIDs, parent and child)
// would otherwise never match. Used by internal/mitm's Path B fulfillment
// path to recognize delegate-subprocess traffic regardless of how many
// wrapper layers exist between Glider's own cmd.Start() and the real
// network-owning process. false (never an error) on any lookup failure —
// the caller's fallback (treat as ordinary front-CLI traffic) is always
// safe, just potentially slower, not incorrect.
func IsInDelegateSubprocessJob(pid uint32) bool {
	job, err := getKillOnCloseJob()
	if err != nil {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var result uint32 // BOOL out-param; IsProcessInJob writes 0/1 here
	ret, _, _ := procIsProcessInJob.Call(uintptr(handle), uintptr(job), uintptr(unsafe.Pointer(&result)))
	if ret == 0 { // the call itself failed
		return false
	}
	return result != 0
}
