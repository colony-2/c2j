package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
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

func TestSelfRepositoryExecutionDefaults(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte(`base: go
self: {repo: github.com/example/repository, ref: main}
packages: [uv:ruff==0.11.2]
execution:
 base: linux*
 capabilities: [docker, docker]
 resources: {cpu: 2000m, memory: 8192Mi, ephemeral-storage: 10Gi}
`), 0600))
	command := newSelfCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--json"})
	require.NoError(t, command.Execute())
	var got struct {
		Repo      string                 `json:"repo"`
		Packages  []string               `json:"packages"`
		Execution execution.Requirements `json:"repository_execution"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "github.com/example/repository", got.Repo)
	require.Equal(t, []string{"uv:ruff==0.11.2"}, got.Packages)
	require.Equal(t, "linux*", *got.Execution.Base)
	require.Equal(t, "2", *got.Execution.Resources.CPU)
	require.Equal(t, "8Gi", *got.Execution.Resources.Memory)
	require.Equal(t, "10Gi", *got.Execution.Resources.EphemeralStorage)
	require.Equal(t, []string{"docker"}, got.Execution.Capabilities)
}
