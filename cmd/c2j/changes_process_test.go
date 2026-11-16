package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

type shortChangesProcessAPI struct{ *jobinspect.Service }
type limitedChangesProcessBody struct {
	io.Reader
	io.Closer
}

func (s shortChangesProcessAPI) OpenDiff(ctx context.Context, q jobinspect.DiffRequest) (*jobinspect.ChangesDiff, io.ReadCloser, error) {
	m, r, e := s.Service.OpenDiff(ctx, q)
	if e != nil {
		return nil, nil, e
	}
	return m, limitedChangesProcessBody{io.LimitReader(r, m.Bytes/2), r}, nil
}

func TestChangesCLIProcessJ43J46L09(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "process.db")})
			require.NoError(t, e)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.WorkflowRuntime = db
			if backend == "http" {
				server := httptest.NewServer(celltest.Handler(remote.NewServer(db)))
				t.Cleanup(server.Close)
				rt, e = remote.New(server.URL, server.Client())
				require.NoError(t, e)
			}
			celltest.Create(t, rt, "tenant", "changes", "example.test/changes")
			f, e := changestest.Build(ctx, rt, changestest.Options{Directory: t.TempDir()})
			require.NoError(t, e)
			oracle, e := f.Oracle(ctx, f.Base, f.Head)
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			before, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			guard := &changestest.ReadOnlyRuntime{WorkflowRuntime: rt}
			service := &jobinspect.Service{Runtime: guard, Tenant: "tenant"}
			run := func(api jobinspect.API, args ...string) ([]byte, []byte, int) {
				t.Helper()
				current := jobcontext.Current{TenantID: "tenant", JobID: f.Job.JobId, GoalID: "goal"}
				server, e := childbroker.Start(jobinspect.WithAPI(ctx, api), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
				require.NoError(t, e)
				defer server.Close()
				processCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				command := exec.CommandContext(processCtx, os.Args[0], append([]string{"-test.run=^TestLeaseDiagnosticProcess$", "--", "job"}, args...)...)
				for _, entry := range os.Environ() {
					name, _, _ := strings.Cut(entry, "=")
					if !strings.HasPrefix(name, "C2J_") {
						command.Env = append(command.Env, entry)
					}
				}
				for k, v := range jobcontext.EnvForCurrent(current) {
					command.Env = append(command.Env, k+"="+v)
				}
				for k, v := range server.Env() {
					command.Env = append(command.Env, k+"="+v)
				}
				command.Env = append(command.Env, "C2J_TEST_LEASE_PROCESS=1")
				command.Dir = t.TempDir()
				var out, diagnostics bytes.Buffer
				command.Stdout = &out
				command.Stderr = &diagnostics
				err := command.Run()
				code := 0
				if err != nil {
					var exited *exec.ExitError
					require.ErrorAs(t, err, &exited)
					code = exited.ExitCode()
				}
				return out.Bytes(), diagnostics.Bytes(), code
			}
			out, diag, code := run(service, "changes", "self", "--json")
			require.Zero(t, code)
			require.Empty(t, diag)
			var h jobinspect.ChangesHistory
			require.NoError(t, json.Unmarshal(out, &h))
			require.Equal(t, f.Head, h.Head)
			out, diag, code = run(service, "diff", "self", "--all", "--json")
			require.Zero(t, code)
			require.Equal(t, oracle, out)
			var m jobinspect.ChangesDiff
			require.NoError(t, json.Unmarshal(diag, &m))
			require.Equal(t, h.Selection, m.Selection)
			out, diag, code = run(service, "diff", "self", "--json", "--selection", "wrong")
			require.Equal(t, 1, code)
			require.Empty(t, out)
			var failure jobinspect.ChangesError
			require.NoError(t, json.Unmarshal(diag, &failure), "stderr must contain exactly one JSON diagnostic")
			require.Equal(t, "stale_selection", failure.Code)
			partial := filepath.Join(t.TempDir(), "partial.patch")
			out, diag, code = run(shortChangesProcessAPI{service}, "diff", "self", "--json", "--output", partial)
			require.Equal(t, 1, code)
			require.Empty(t, out)
			require.NoError(t, json.Unmarshal(diag, &failure))
			require.Equal(t, "transport", failure.Code)
			_, e = os.Stat(partial)
			require.True(t, os.IsNotExist(e))
			after, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			require.Equal(t, string(before), string(after))
			require.Zero(t, guard.Mutations.Load())
		})
	}
}
