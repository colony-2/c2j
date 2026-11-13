package config

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryPythonDefaults(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	path := filepath.Join(root, ".c2/config.yaml")
	raw := []byte("execution: {python: {requires-python: '>=3.11', dependencies: ['Pillow==12.0.0']}}\n")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	source, err := LoadWorkspacePackages(root)
	require.NoError(t, err)
	require.Equal(t, []string{"Pillow==12.0.0"}, source.Execution.Python.Dependencies)
	config, err := parseProjectConfig(path, raw)
	require.NoError(t, err)
	require.Equal(t, source.Execution, config.Execution())
	for _, body := range []string{"null", "[]", "{dependencies: [12]}", "{requires-python: 3.11}", "{backend: uv}"} {
		require.NoError(t, os.WriteFile(path, []byte("execution: {python: "+body+"}\n"), 0600))
		_, err := LoadWorkspacePackages(root)
		require.Error(t, err)
	}
}
