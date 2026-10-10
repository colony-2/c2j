// Package nixtest runs integration tests in a disposable official Nix container.
package nixtest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// InContainer runs this test in Docker when called on the host. Tests return
// when it returns false. Missing Docker and short mode skip integration tests.
func InContainer(t *testing.T) bool {
	t.Helper()
	if testing.Short() {
		t.Skip("Nix integration test requires Docker and network access")
	}
	if os.Getenv("C2J_NIX_TEST_CONTAINER") == "1" {
		return true
	}
	run(t)
	return false
}

func run(t *testing.T) {
	t.Helper()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Nix integration test: Docker is not installed")
	}
	probeCtx, cancelProbe := context.WithTimeout(t.Context(), 15*time.Second)
	out, err := exec.CommandContext(probeCtx, docker, "info", "--format", "{{.OSType}}/{{.Architecture}}").Output()
	cancelProbe()
	if err != nil {
		t.Skipf("Nix integration test: Docker is unavailable: %v: %s", err, out)
	}
	var arch string
	switch strings.TrimSpace(string(out)) {
	case "linux/aarch64", "linux/arm64":
		arch = "arm64"
	case "linux/x86_64", "linux/amd64":
		arch = "amd64"
	default:
		t.Skipf("Nix integration test: unsupported Docker platform %s", out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "toolenv.test")
	// A static Linux binary also works with a remote daemon or Docker Desktop.
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	out, err = build.CombinedOutput()
	require.NoError(t, err, string(out))
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		require.NoError(t, err, "docker %v: %s", args, out)
		return out
	}
	cidFile := filepath.Join(dir, "container-id")
	run("create", "--cidfile", cidFile, "-e", "C2J_NIX_TEST_CONTAINER=1",
		"--entrypoint", "/tmp/toolenv.test", "nixos/nix:2.35.1",
		"-test.run", "^"+regexp.QuoteMeta(t.Name())+"$", "-test.v", "-test.timeout", "8m")
	cid, err := os.ReadFile(cidFile)
	require.NoError(t, err)
	id := strings.TrimSpace(string(cid))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cleanupCtx, docker, "rm", "-f", id).CombinedOutput()
		if err != nil {
			t.Errorf("remove Nix test container: %v: %s", err, out)
		}
	})
	// Copy instead of bind mounting: the daemon need not share the host filesystem.
	run("cp", binary, id+":/tmp/toolenv.test")
	t.Logf("%s", run("start", "--attach", id))
	require.Equal(t, "0", strings.TrimSpace(string(run("inspect", "--format", "{{.State.ExitCode}}", id))), "Nix container tests failed")
}
