//go:build !unix

package jobinspect

import "os/exec"

const changesProcessGroups = false

func configureChangesProcess(cmd *exec.Cmd) {}
func cleanupChangesProcess(cmd *exec.Cmd)   {}
