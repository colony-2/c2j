//go:build !unix && !windows

package process

import (
	"os/exec"
	"time"
)

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
