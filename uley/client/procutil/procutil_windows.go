package procutil

import (
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createNoWindow           = 0x08000000
	belowNormalPriorityClass = 0x00004000
)

// Quiet hides the console window of cmd.
func Quiet(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// Background is Quiet plus below-normal priority.
func Background(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow | belowNormalPriorityClass}
}

var (
	jobOnce sync.Once
	job     windows.Handle
)

// AfterStart puts the process into a job object that Windows kills
// together with the client, so the engine never outlives it.
func AfterStart(pid int) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			return
		}
		job = h
	})
	if job == 0 {
		return
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(p)
	windows.AssignProcessToJobObject(job, p)
}
