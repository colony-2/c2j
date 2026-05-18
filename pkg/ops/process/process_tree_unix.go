//go:build unix

package process

import (
	"os/exec"
	"syscall"
	"time"
)

const processTerminationGrace = 2 * time.Second

func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcessTree(cmd *exec.Cmd) {
	signalProcessGroup(cmd, syscall.SIGTERM)
}

func killProcessTree(cmd *exec.Cmd) {
	signalProcessGroup(cmd, syscall.SIGKILL)
}

func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if err == syscall.ESRCH {
		return
	}
}
