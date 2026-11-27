//go:build integration

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestJobLabelsCLIRoundTrip(t *testing.T) {
	binary := os.Getenv("C2J_TEST_JOB_LABELS_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "c2j")
		command := exec.Command("go", "build", "-mod=readonly", "-buildvcs=false", "-o", binary, ".")
		data, err := command.CombinedOutput()
		require.NoError(t, err, "%s", data)
	}
	var unexpected atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unexpected.Add(1)
		http.Error(w, "runtime should not have opened", http.StatusInternalServerError)
	}))
	defer trap.Close()
	for _, transport := range []string{"embedded", "http"} {
		t.Run(transport, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			ctx := celltest.Context(context.Background())
			tenant, target := "0", []string{"--embed"}
			var runtime jobdb.ConsumerRuntime
			var closeRuntime func() error
			if transport == "embedded" {
				handle, err := swfruntime.Open(ctx, "embed:///")
				require.NoError(t, err)
				runtime, closeRuntime = handle.Runtime, handle.Cleanup
			} else {
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "labels.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
				t.Cleanup(server.Close)
				runtime, tenant, target = db, "labels", []string{"--jobdb", server.URL + "/labels"}
			}
			celltest.Create(t, runtime, tenant, "test", "example.test/test")
			job, err := runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
				TenantId: tenant, JobID: "label-job", Cell: "test", CellRepo: "example.test/test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(nil),
			}})
			require.NoError(t, err)
			if closeRuntime != nil {
				require.NoError(t, closeRuntime())
			}
			id := job.JobKey.JobId
			dir := t.TempDir()
			run := func(env map[string]string, target []string, args ...string) (string, string, error) {
				t.Helper()
				call, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(call, binary, append(append([]string{"job"}, target...), args...)...)
				cmd.Dir = dir
				for _, entry := range os.Environ() {
					if !strings.HasPrefix(entry, "C2J_") {
						cmd.Env = append(cmd.Env, entry)
					}
				}
				for key, value := range env {
					cmd.Env = append(cmd.Env, key+"="+value)
				}
				var out, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &stderr
				err := cmd.Run()
				return out.String(), stderr.String(), err
			}
			ok := func(t *testing.T, args ...string) string {
				t.Helper()
				out, stderr, err := run(nil, target, args...)
				require.NoError(t, err, "%s", stderr)
				return out
			}
			baselineShow, baselineList := ok(t, "show", id), ok(t, "list")
			require.Contains(t, ok(t, "labels", "get", id, "friend", "--json"), `"status":"unknown"`)
			for _, invalid := range []string{"", "-0", "-1", "01", "+0", " 0", "9223372036854775808"} {
				_, stdErr, err := run(nil, []string{"--jobdb", trap.URL + "/labels"}, "labels", "set", id, "friend", "foe", "--color", "blue", "--if-version="+invalid)
				require.Error(t, err)
				require.Contains(t, stdErr, "canonical nonnegative int64")
			}
			require.Contains(t, ok(t, "labels", "set", id, "friend", "foe", "--color", "blue", "--if-version", "0", "--json"), `"version":"1"`)
			require.Contains(t, ok(t, "labels", "get", id, "friend"), "friend=foe  blue  version=1")
			require.Contains(t, ok(t, "labels", "list", id, "--json"), `"labels_projection":"projected"`)
			require.Contains(t, ok(t, "show", id, "--json"), `"value": "foe"`)
			require.Contains(t, ok(t, "list", "--json"), `"value": "foe"`)
			t.Run("ordinary-text-unchanged", func(t *testing.T) {
				require.Equal(t, baselineShow, ok(t, "show", id))
				require.Equal(t, baselineList, ok(t, "list"))
			})
			fails := func(t *testing.T, target []string, category string, args ...string) {
				t.Helper()
				stdout, stderr, err := run(nil, target, args...)
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Positive(t, exit.ExitCode())
				require.Empty(t, stdout, "failures must not report a receipt")
				require.Contains(t, stderr, "job label "+category+":")
			}
			t.Run("conflict", func(t *testing.T) {
				fails(t, target, "conflict", "labels", "remove", id, "friend", "--if-version", "0")
			})
			t.Run("not-found", func(t *testing.T) {
				fails(t, target, "not found", "labels", "get", "absent", "friend")
			})
			if transport == "http" {
				for _, failure := range []struct {
					name, category string
					status         int
					cause          error
				}{
					{"denied", "denied", http.StatusForbidden, jobdb.ErrLeasePermissionDenied},
					{"lease-lost", "lease lost", http.StatusConflict, jobdb.ErrExecutionLeaseLost},
					{"unsupported", "unsupported", http.StatusNotImplemented, jobdb.ErrJobMetadataUnsupported},
				} {
					t.Run(failure.name, func(t *testing.T) {
						var calls atomic.Int32
						server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
							if request.Method != http.MethodPatch || request.URL.Path != "/v1/tenants/"+tenant+"/jobs/"+id+"/extended-metadata" {
								http.NotFound(writer, request)
								return
							}
							calls.Add(1)
							http.Error(writer, failure.cause.Error(), failure.status)
						}))
						t.Cleanup(server.Close)
						fails(t, []string{"--jobdb", server.URL + "/" + tenant}, failure.category, "labels", "remove", id, "friend", "--if-version", "1", "--json")
						require.EqualValues(t, 1, calls.Load())
					})
				}
			}
			current := jobcontext.EnvForCurrent(jobcontext.Current{TenantID: tenant, JobID: id})
			_, stderr, err := run(current, []string{"--jobdb", trap.URL + "/labels"}, "labels", "remove", id, "friend", "--if-version", "1")
			require.Error(t, err)
			require.Contains(t, stderr, "unavailable inside a task")
			require.Zero(t, unexpected.Load())
			require.Contains(t, ok(t, "labels", "remove", id, "friend", "--if-version", "1", "--json"), `"version":"2"`)
			require.Contains(t, ok(t, "labels", "get", id, "friend", "--json"), `"status":"tombstone"`)
		})
	}
}
