//go:build windows

// Windows implementation of the process ownership seam: every owned child is
// started suspended and attached to its own kill-on-close job object, then
// resumed. Whole-tree termination is TerminateJobObject. Windows is one of the
// two supported platforms; there is no best-effort fallback here.
package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsOwner struct{}

func newPlatformOwner(OwnerConfig) Owner { return &windowsOwner{} }

// Assign creates the tree's job object with KILL_ON_JOB_CLOSE and starts the
// child suspended so it cannot spawn descendants before job assignment.
func (w *windowsOwner) Assign(cmd *exec.Cmd) (*Tree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("process: create job object: %w", err)
	}
	limitInfo := windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
		LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectBasicLimitInformation,
		uintptr(unsafe.Pointer(&limitInfo)), uint32(unsafe.Sizeof(limitInfo))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("process: configure kill-on-close job: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	return &Tree{exited: make(chan struct{}), platform: &windowsTree{job: job}}, nil
}

func (w *windowsOwner) Terminate(tree *Tree) error { return Terminate(tree) }

type windowsTree struct {
	job windows.Handle
}

// attach assigns the suspended child to its job and resumes its threads. The
// suspend window guarantees no descendant can escape ownership.
func (w *windowsTree) attach(proc *os.Process) error {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(proc.Pid))
	if err != nil {
		return fmt.Errorf("process: open child for job assignment: %w", err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(w.job, handle); err != nil {
		return fmt.Errorf("process: assign child to job: %w", err)
	}
	if err := resumeProcessThreads(uint32(proc.Pid)); err != nil {
		return err
	}
	return nil
}

func (w *windowsTree) terminate(int) error {
	if err := windows.TerminateJobObject(w.job, 1); err != nil {
		return fmt.Errorf("process: terminate job: %w", err)
	}
	return nil
}

func (w *windowsTree) alive(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false
	}
	// WAIT_OBJECT_0 means the process already exited; WAIT_TIMEOUT means it
	// is still running.
	return event == uint32(windows.WAIT_TIMEOUT)
}

// release closes the job handle. By the time the caller releases a tree its
// exit was confirmed, so kill-on-close has nothing left to kill; closing
// earlier would be exactly how an owned tree dies.
func (w *windowsTree) release() {
	_ = windows.CloseHandle(w.job)
	w.job = windows.Handle(0)
}

// resumeProcessThreads resumes every thread of the suspended child so its
// main thread starts running inside its assigned job.
func resumeProcessThreads(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("process: snapshot threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return fmt.Errorf("process: read first thread: %w", err)
	}
	resumed := false
	for {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return fmt.Errorf("process: open child thread %d: %w", entry.ThreadID, err)
			}
			_, _ = windows.ResumeThread(thread)
			_ = windows.CloseHandle(thread)
			resumed = true
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			break
		}
	}
	if !resumed {
		return fmt.Errorf("process: no resumable thread found for pid %d", pid)
	}
	return nil
}
