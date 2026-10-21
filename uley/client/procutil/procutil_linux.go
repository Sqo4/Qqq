package procutil

import (
	"os/exec"
	"syscall"
)

func Quiet(cmd *exec.Cmd) {}

// Background makes the child die together with the client.
func Background(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// AfterStart lowers the priority of a started background process.
func AfterStart(pid int) { syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10) }
