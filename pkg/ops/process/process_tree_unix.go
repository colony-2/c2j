//go:build unix

package process

import (
	"os/exec"
	"syscall"
	"time"
)

const CheckedTreeCleanupSupported = true

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

// The group can outlive its original shell after a control exit.
func stopExitedProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
