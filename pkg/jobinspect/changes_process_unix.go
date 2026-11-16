//go:build unix

package jobinspect

import (
	"os/exec"
	"syscall"
	"time"
)

const changesProcessGroups = true

func cleanupChangesProcess(cmd *exec.Cmd) {
	// Also stop descendants if the direct child exited before a pipe/transport
	// failure. Wait has already reaped our direct child.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func configureChangesProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
}
