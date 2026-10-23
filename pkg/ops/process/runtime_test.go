package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExecuteProcessMasksDynamicallyResolvedGitCredentials(t *testing.T) {
	profile := gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: "https://git.example/", Username: "bot", SecretRef: "token"}}}
	raw, err := json.Marshal(profile)
	require.NoError(t, err)
	scope, err := gitprofile.NewScope(context.Background(), gitprofile.Selection{Explicit: true, Value: "test"}, jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{gitprofile.ProfilePrefix + "test": raw}}, func(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"token": {Value: "private-git-token"}}}, nil
	}, "/usr/bin/c2j")
	require.NoError(t, err)
	defer scope.Close()
	ctx := gitprofile.WithScope(context.Background(), scope)
	var socket string
	for _, v := range gitprofile.Env(ctx, nil) {
		if value, ok := strings.CutPrefix(v, gitprofile.SocketEnv+"="); ok {
			socket = value
		}
	}
	_, err = gitprofile.Call(ctx, socket, gitprofile.Request{Operation: "secret", Name: "token"})
	require.NoError(t, err)
	stdout, stderr, err := ExecuteProcess(ctx, RunRequest{WorkingDir: t.TempDir(), Shell: "sh", Run: "printf private-git-token; printf private-git-token >&2"})
	require.NoError(t, err)
	require.Equal(t, "[REDACTED]", string(stdout))
	require.Equal(t, "[REDACTED]", string(stderr))
}

func TestExecuteProcessTimeoutKillsHostProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell process-tree behavior")
	}

	root := t.TempDir()
	marker := filepath.Join(root, "child-survived")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, _, err := ExecuteProcess(ctx, RunRequest{
		WorkingDir: root,
		Shell:      "sh",
		Run:        "(sleep 1; echo leaked > child-survived) & sleep 5",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded), "expected deadline exceeded error, got %v", err)
	require.Less(t, time.Since(started), 2*time.Second)

	time.Sleep(1200 * time.Millisecond)
	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr), "descendant process wrote marker after timeout")
}

func TestExecuteProcessUsesNonLoginBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script fake bash")
	}

	binDir := t.TempDir()
	fakeBash := filepath.Join(binDir, "bash")
	script := `#!/bin/sh
if [ "$1" = "-lc" ]; then
  printf 'profile noise\n' >&2
fi
if [ "$1" != "-c" ] && [ "$1" != "-lc" ]; then
  printf 'unexpected shell args: %s\n' "$*" >&2
  exit 64
fi
shift
eval "$1"
`
	require.NoError(t, os.WriteFile(fakeBash, []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, err := ExecuteProcess(context.Background(), RunRequest{
		WorkingDir: t.TempDir(),
		Shell:      "bash",
		Run:        "printf ok",
	})
	require.NoError(t, err)
	require.Equal(t, "ok", string(stdout))
	require.Empty(t, string(stderr))
}

func TestExecuteProcessIgnoresShaiConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell")
	}
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".shai"), 0700))
	// Invalid configuration must not be discovered or parsed.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".shai", "config.yaml"), []byte("[invalid: yaml"), 0600))
	stdout, stderr, err := ExecuteProcess(context.Background(), RunRequest{
		WorkingDir: root, Shell: "sh", Run: `cat; printf '%s' "$C2J_PROCESS_TEST"; printf diagnostic >&2`,
		Stdin: []byte("input:"), Env: map[string]string{"C2J_PROCESS_TEST": "worker"},
	})
	require.NoError(t, err)
	require.Equal(t, "input:worker", string(stdout))
	require.Equal(t, "diagnostic", string(stderr))
}

func TestUnsetEnvironmentSuppressesInheritedAndConfiguredValues(t *testing.T) {
	t.Setenv("C2J_OPTIONAL_SECRET_TEST", "inherited")
	ctx := WithUnsetEnv(context.Background(), []string{"C2J_OPTIONAL_SECRET_TEST"})
	out, _, err := ExecuteProcess(ctx, RunRequest{
		WorkingDir: t.TempDir(), Shell: "sh",
		Env: map[string]string{"C2J_OPTIONAL_SECRET_TEST": "configured", "C2J_EMPTY_SECRET_TEST": ""},
		Run: "test -z \"${C2J_OPTIONAL_SECRET_TEST+x}\" && test \"${C2J_EMPTY_SECRET_TEST+x}\" = x && printf ok",
	})
	require.NoError(t, err)
	require.Equal(t, "ok", string(out))
	require.Equal(t, "inherited", os.Getenv("C2J_OPTIONAL_SECRET_TEST"))
}
