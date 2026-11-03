package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/stretchr/testify/require"
)

func TestSelfUsesCommandOutputAndContext(t *testing.T) {
	t.Chdir(t.TempDir())
	// Advertise an incomplete context regardless of the developer's environment.
	t.Setenv(jobcontext.CurrentTenantIDEnv, "")
	t.Setenv(jobcontext.CurrentJobIDEnv, "")
	t.Setenv(jobcontext.ChildJobTokenEnv, "private-token")
	command := newSelfCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--json"})
	require.NoError(t, command.Execute())
	require.Contains(t, out.String(), `"schema_version":1`)
	require.Contains(t, out.String(), `"incomplete_execution_context"`)
	require.NotContains(t, out.String(), "private-token")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, newSelfCmd().ExecuteContext(ctx), context.Canceled)
	command = newSelfCmd()
	command.SetArgs([]string{"unexpected"})
	require.Error(t, command.Execute())
}
