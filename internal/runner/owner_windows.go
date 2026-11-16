//go:build windows

package runner

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

type windowsOwner struct {
	mu  sync.Mutex
	job windows.Handle
}

func (o *windowsOwner) Kill() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(o.job, 1)
}
func (o *windowsOwner) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.job == 0 {
		return nil
	}
	err := windows.CloseHandle(o.job)
	o.job = 0
	return err
}
func startOwned(command *exec.Cmd, memoryMB int) (owner, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	ownership := &windowsOwner{job: job}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if memoryMB > 0 {
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		limits.JobMemoryLimit = uintptr(int64(memoryMB+max(512, memoryMB/2)) << 20)
	}
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		ownership.Close()
		return nil, err
	}
	// No user code runs before assignment: the initial process starts suspended.
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
	if err = command.Start(); err != nil {
		ownership.Close()
		return nil, err
	}
	fail := func(err error) (owner, error) {
		_ = command.Process.Kill()
		ownership.Close()
		_ = command.Wait()
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(command.Process.Pid))
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		return fail(err)
	}
	resume := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")
	if err = resume.Find(); err != nil {
		return fail(err)
	}
	status, _, _ := resume.Call(uintptr(process))
	if status != 0 {
		return fail(fmt.Errorf("could not resume owned process (status %#x)", status))
	}
	return ownership, nil
}
