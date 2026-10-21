//go:build !windows && !linux

package procutil

import (
	"os/exec"
	"syscall"
)

func Quiet(cmd *exec.Cmd) {}

func Background(cmd *exec.Cmd) {}

// AfterStart lowers the priority of a started background process.
func AfterStart(pid int) { syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10) }
