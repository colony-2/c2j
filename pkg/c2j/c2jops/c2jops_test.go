package c2jops

import (
	"testing"

	coreops "github.com/colony-2/c2j/pkg/core/ops"
	"github.com/stretchr/testify/require"
)

func TestOpsIncludesExtensionExecution(t *testing.T) {
	names := opNames(Ops())
	require.Contains(t, names, "extension_execution")
	require.NotContains(t, names, "cells.list")
}

func TestRegisterInstallsExtensionExecution(t *testing.T) {
	original := coreops.List()
	t.Cleanup(func() {
		coreops.Replace(original...)
	})

	Register()

	names := opNames(coreops.List())
	require.Contains(t, names, "extension_execution")
	require.NotContains(t, names, "cells.list")
}

func opNames(ops []coreops.RegisterableOp) map[string]struct{} {
	names := make(map[string]struct{}, len(ops))
	for _, op := range ops {
		names[op.GetName()] = struct{}{}
	}
	return names
}
