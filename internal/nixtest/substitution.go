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

// InSubstitutionConsumer builds signed fixtures in a producer and copies only
// public cache data into independent cold consumers. No daemon socket or host
// store is mounted, and private signing keys never leave the producer.
func InSubstitutionConsumer(t *testing.T, fixture string, cases []string) bool {
	t.Helper()
	if testing.Short() {
		t.Skip("signed substitution tests require disposable Docker stores")
	}
	if os.Getenv("C2J_NIX_SUBSTITUTION_CASE") != "" {
		return true
	}
	docker, arch := dockerPlatform(t)
	ctx, cancel := context.WithTimeout(t.Context(), 14*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := buildBinary(t, ctx, dir, arch)
	run := func(t *testing.T, args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, docker, args...).CombinedOutput()
		require.NoError(t, err, "docker %v: %s", args, out)
		return out
	}
	create := func(t *testing.T, args ...string) string {
		t.Helper()
		// Image-pull progress shares CombinedOutput with stdout on a cold daemon.
		// Read Docker's ID file instead so that progress cannot become an ID.
		cidFile := filepath.Join(t.TempDir(), "container-id")
		run(t, append([]string{"create", "--cidfile", cidFile}, args...)...)
		cid, err := os.ReadFile(cidFile)
		require.NoError(t, err)
		id := strings.TrimSpace(string(cid))
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			out, err := exec.CommandContext(cleanup, docker, "rm", "-f", id).CombinedOutput()
			require.NoError(t, err, string(out))
		})
		return id
	}
	producer := create(t, "--workdir", "/tmp", "--entrypoint", "/bin/sh", "nixos/nix:2.35.1", "-eu", "-c", `
export NIX_CONFIG='experimental-features = nix-command flakes
sandbox = false'
mkdir -p /tmp/export/cache
nix key generate-secret --key-name c2j-fixture > /tmp/private-key
nix key convert-secret-to-public < /tmp/private-key > /tmp/export/public-key
nix key generate-secret --key-name c2j-other | nix key convert-secret-to-public > /tmp/export/wrong-key
nix build --no-link 'path:/tmp/nix-substitution#default^out,dev,bin,lib' 'path:/tmp/nix-substitution#payload'
nix eval --json --apply 'p: { drv = p.drvPath; paths = map (x: x.outPath) [p p.dev p.bin p.lib]; }' 'path:/tmp/nix-substitution' > /tmp/export/metadata.json
nix eval --raw 'path:/tmp/nix-substitution#payload.outPath' > /tmp/export/payload
paths=$(nix eval --raw --apply 'p: builtins.concatStringsSep " " (map (x: x.outPath) [p p.dev p.bin p.lib])' 'path:/tmp/nix-substitution')
nix store sign --recursive --key-file /tmp/private-key $paths
nix copy --to file:///tmp/export/cache $paths
`)
	run(t, "cp", fixture, producer+":/tmp/nix-substitution")
	t.Logf("producer: %s", run(t, "start", "--attach", producer))
	require.Equal(t, "0", strings.TrimSpace(string(run(t, "inspect", "--format", "{{.State.ExitCode}}", producer))))
	run(t, "cp", producer+":/tmp/export", dir+"/export")
	testName := t.Name()
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			consumer := create(t, "-e", "C2J_NIX_TEST_CONTAINER=1", "-e", "C2J_NIX_SUBSTITUTION_CASE="+name,
				"--workdir", "/tmp", "--entrypoint", "/tmp/toolenv.test", "nixos/nix:2.35.1",
				"-test.run", "^"+regexp.QuoteMeta(testName)+"$", "-test.v", "-test.timeout", "10m")
			run(t, "cp", binary, consumer+":/tmp/toolenv.test")
			run(t, "cp", fixture, consumer+":/tmp/nix-substitution")
			run(t, "cp", dir+"/export", consumer+":/tmp/substitution")
			t.Logf("consumer: %s", run(t, "start", "--attach", consumer))
			require.Equal(t, "0", strings.TrimSpace(string(run(t, "inspect", "--format", "{{.State.ExitCode}}", consumer))))
		})
	}
	return false
}
