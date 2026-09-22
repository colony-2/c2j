package joblist_test

import (
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestStandaloneConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("standalone module builds disabled by -short")
	}
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	source, err := os.ReadFile(filepath.Join(root, "examples/listjobs/main.go"))
	require.NoError(t, err)
	module := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(module, "main.go"), source, 0600))
	// A replace validates the not-yet-published change from an independent module.
	// Release consumers use the published version without this replacement.
	goMod := "module example.com/listing-consumer\n\ngo 1.26\n\nrequire github.com/colony-2/c2j v0.0.0\n\nreplace github.com/colony-2/c2j => " + filepath.ToSlash(root) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte(goMod), 0600))
	goBin, err := exec.LookPath("go")
	require.NoError(t, err)
	buildEnv := append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOOS=linux")
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			binary := filepath.Join(t.TempDir(), "listjobs")
			cmd := exec.CommandContext(ctx, goBin, "build", "-mod=mod", "-trimpath", "-buildvcs=false", "-o", binary, ".")
			cmd.Dir, cmd.Env = module, append(buildEnv, "GOARCH="+arch)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			f, err := elf.Open(binary)
			require.NoError(t, err)
			for _, prog := range f.Progs {
				require.NotEqual(t, elf.PT_INTERP, prog.Type, "binary must not depend on a dynamic linker")
			}
			require.NoError(t, f.Close())
			var runner string
			if runtime.GOOS != "linux" || runtime.GOARCH != arch {
				if runtime.GOOS == "linux" {
					emulator := "qemu-aarch64"
					if arch == "amd64" {
						emulator = "qemu-x86_64"
					}
					for _, name := range []string{emulator + "-static", emulator} {
						if path, err := exec.LookPath(name); err == nil {
							runner = path
							break
						}
					}
				}
				if runtime.GOOS != "linux" {
					t.Log("static build verified; native execution requires a matching host or QEMU")
					return
				}
			}
			s := standaloneServer(t)
			args := []string{"-jobdb", s.URL + "/tenant", "-repo", "github.com/acme/app"}
			if runner != "" {
				args = append([]string{binary}, args...)
				binary = runner
			}
			cmd = exec.CommandContext(ctx, binary, args...)
			cmd.Dir = t.TempDir() // No checkout or configuration.
			cmd.Env = []string{"PATH=/no-tools-installed"}
			output, err = cmd.Output()
			if runtime.GOARCH != arch && runner == "" && errors.Is(err, syscall.ENOEXEC) {
				t.Log("static build verified; execution requires native hardware or an emulator")
				return
			}
			require.NoError(t, err)
			var page joblist.Page
			require.NoError(t, json.Unmarshal(output, &page))
			require.Empty(t, page.Jobs)
		})
	}
	cmd := exec.Command(goBin, "list", "-mod=mod", "-deps", ".")
	cmd.Dir, cmd.Env = module, append(buildEnv, "GOARCH=amd64")
	deps, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", deps)
	for _, forbidden := range []string{"/c2j/pkg/worker", "/c2j/pkg/config", "/c2j/pkg/starter", "/c2j/cmd/", "/runtime/sqlite", "modernc.org/sqlite", "github.com/docker/docker"} {
		require.False(t, strings.Contains(string(deps), forbidden), "unexpected listing dependency: %s", forbidden)
	}
}

func standaloneServer(t *testing.T) *httptest.Server {
	return serve(t, func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{}}, nil
	})
}
