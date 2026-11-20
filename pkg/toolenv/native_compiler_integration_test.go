package toolenv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/nixtest"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/stretchr/testify/require"
)

// The test runner may be static; the declared Go child must compile and call C.
// Fixtures are copied too, since Docker may be on a remote daemon.
func TestRealNativeCompilerEnvironment(t *testing.T) {
	if !nixtest.InContainer(t, "testdata") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	const pin = "nix:github:NixOS/nixpkgs/39ad350a0602fa0a58a544344e3e9187526ea45c#"
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "op:compiler-probe", Packages: []string{pin + "gcc", pin + "go_1_26"}}}
	var cold Environment
	for _, phase := range []string{"prepared", "reused"} {
		env, diag, err := m.Prepare(ctx, scopes)
		raw, marshalErr := json.Marshal(diag)
		require.NoError(t, marshalErr)
		t.Logf("%s preparation: %s", phase, raw)
		require.NoError(t, err)
		require.NotNil(t, diag.Native)
		require.Equal(t, phase, diag.Native.Outcome)
		require.True(t, env.Ready())
		require.True(t, env.Tools[0].Nix.Compiler)
		if phase == "prepared" {
			cold = env
		} else {
			require.Equal(t, cold, env)
			for _, tool := range diag.Tools {
				require.Equal(t, "reused", tool.Outcome)
			}
		}
		work := t.TempDir()
		for _, name := range []string{"probe.c", "cgo/go.mod", "cgo/probe.go", "cgo/probe_test.go"} {
			data, err := os.ReadFile(filepath.Join("testdata/native/probes", name))
			require.NoError(t, err)
			dest := filepath.Join(work, name)
			require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0700))
			require.NoError(t, os.WriteFile(dest, data, 0600))
		}
		// Each phase gets a fresh cache: warm activation must still support a
		// complete C/CGO/race compilation rather than reuse compiled Go objects.
		run := `set -eu
cc --version
go version
CGO_ENABLED=1 go env GOOS GOARCH CGO_ENABLED CC
cc probe.c -o probe
./probe
cd cgo
CGO_ENABLED=1 go test -count=1 ./...
CGO_ENABLED=1 go test -race -count=1 ./...
`
		run = "export GOCACHE=" + shellcmd.QuotePOSIX(filepath.Join(work, "go-build")) + "\nexport GOTOOLCHAIN=local GOWORK=off GOFLAGS=-mod=readonly\n" + run
		out, stderr, err := process.ExecuteProcess(env.Bind(ctx), process.RunRequest{WorkingDir: work, Shell: "sh", Run: run})
		t.Logf("%s probes:\n%s%s", phase, out, stderr)
		require.NoError(t, err, string(stderr))
		require.Contains(t, string(out), "native-c-ok")
	}
}
