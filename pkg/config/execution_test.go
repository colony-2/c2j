package config

import (
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryExecutionLiterals(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	raw := []byte("base: go\npackages: [uv:ruff==0.11.2]\nexecution:\n  base: linux*\n  capabilities: [docker, docker]\n  resources: {cpu: 2, memory: 8Gi, ephemeral-storage: 10Gi}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), raw, 0600))
	s, err := LoadWorkspacePackages(root)
	require.NoError(t, err)
	require.Equal(t, "linux*", *s.Execution.Base)
	require.Equal(t, "2", *s.Execution.Resources.CPU)
	require.Equal(t, "8Gi", *s.Execution.Resources.Memory)
	require.Equal(t, "10Gi", *s.Execution.Resources.EphemeralStorage)
	require.Equal(t, []string{"docker"}, s.Execution.Capabilities)
	require.NotEmpty(t, s.Digest)
	cfg, err := parseProjectConfig(filepath.Join(root, ".c2/config.yaml"), raw)
	require.NoError(t, err)
	require.Equal(t, s.Execution, cfg.Execution())
	require.Equal(t, s.Packages, cfg.Packages())
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0700))
	missing, err := LoadWorkspacePackages(child)
	require.NoError(t, err)
	require.True(t, missing.Missing)
}
func TestRepositoryExecutionInvalid(t *testing.T) {
	for _, value := range []string{
		"null", "[]", "{base: ''}", "{base: 42}", "{base: {command: echo x}}", "{base: '${{ x }}'}", "{base: 'linux amd64'}",
		"{capabilities: null}", "{capabilities: [42]}", "{capabilities: [Docker]}", "{capabilities: '${{ x }}'}",
		"{resources: {cpu: 0}}", "{resources: {memory: 1potato}}", "{resources: {memory: ''}}", "{resources: {memory: 123}}",
		"{resources: {gpu: 1}}", "{resources: []}", "{packages: []}", "{image: x}", "{platform: x}", "{command: echo x}",
	} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte("execution: "+value), 0600))
			_, err := LoadWorkspacePackages(root)
			require.Error(t, err)
			require.Contains(t, err.Error(), ".c2/config.yaml")
			require.Contains(t, err.Error(), "execution")
		})
	}
}
func TestRepositoryExecutionEmptyCapabilities(t *testing.T) {
	var e struct {
		Execution ExecutionDefaults `yaml:"execution"`
	}
	require.NoError(t, yaml.Unmarshal([]byte("execution: {capabilities: []}"), &e))
	require.Empty(t, e.Execution.Capabilities)
}
