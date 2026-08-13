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

// killOnCloseJob is one Job Object of Windows. The code makes it when it first
// needs it, and each part of the process shares it for the life of the process.
// It has JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
//
// Windows then stops each process that is still in that job, at the moment when
// the last handle of the job closes. That occurs always when the process object
// of glider.exe ends, for ANY cause: a usual exit, a failure, or a forceful stop
// from outside such as `taskkill /F`.
//
// A forceful stop is exactly the condition where a path for a clean shutdown can
// never operate, because no Go code operates when a process stops in that way.
//
// This job object is the only mechanism that closes that gap. Cancellation
// through a context, with exec.CommandContext, helps only when the process of
// Glider is still alive and can do the cancellation. For a forceful stop, that
// is not true.
//
// This corrects a true incident, and a live test confirmed it on 2026-07-30. A
// delegate call started a subprocess of cursor-agent. A person then stopped the
// parent glider.exe with force, in the middle of a request. That subprocess
// continued and wrote its edit to the disk. No code was left to report that
// change, or to connect it with the request that started it.
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

// AssignToKillOnCloseJob puts the process of cmd, which already started, in the
// shared kill-on-close job. Call it after cmd.Start(), while cmd.Process has a
// value.
//
// Use it only for a delegate subprocess with no console, from RunWithOptions.
// Never use it for LaunchInteractive. An interactive session goes to the user as
// a window that operates alone. Refer to the comment on resolveInteractive:
// "Glider does not capture or relay anything from this session." To stop a true
// and live session of a user, only because Glider restarted, would destroy work.
// It would not make the system more safe.
//
// The function returns an error. A caller can write it to a log and then ignore
// it. A failed assignment keeps the behaviour from before 2026-07-31, where a
// subprocess can stay after a forceful stop. It does not stop the delegate call.
// A job assignment that fails is a gap in the protection, and it is not a gap in
// the correctness.
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
