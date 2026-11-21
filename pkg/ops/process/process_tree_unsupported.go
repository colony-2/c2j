//go:build !unix && !windows

package process

import (
	"errors"
	"os/exec"
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
	_ = cmd.Process.Kill()
}

func stopExitedProcessTree(*exec.Cmd) error {
	return errors.New("checked extension wait process-tree cleanup is unsupported on this platform")
}
