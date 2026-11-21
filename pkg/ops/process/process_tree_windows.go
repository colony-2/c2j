//go:build windows

package process

import (
	"errors"
	"os/exec"
	"strconv"
	"time"
)

const CheckedTreeCleanupSupported = false

const processTerminationGrace = 2 * time.Second

func configureProcessTree(cmd *exec.Cmd) {
}

func terminateProcessTree(cmd *exec.Cmd) {
	killProcessTree(cmd)
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}

func stopExitedProcessTree(*exec.Cmd) error {
	return errors.New("checked extension wait process-tree cleanup is unsupported on this platform")
}
