package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/c2j/pkg/jobinspect/workspacedifftest"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

type shortChangesProcessAPI struct{ *jobinspect.Service }

func (s shortChangesProcessAPI) Download(ctx context.Context, q jobinspect.Request) (io.ReadCloser, error) {
	r, err := s.Service.Download(ctx, q)
	if err != nil {
		return nil, err
	}
	return &shortSavedBody{Reader: io.LimitReader(r, 2), Closer: r}, nil
}

type shortSavedBody struct {
	io.Reader
	io.Closer
}

func TestWorkspaceDiffCLIProcessC01C02C03C05I06K03K04(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "c2j")
	build := exec.Command("go", "build", "-o", binary, ".")
	result, err := build.CombinedOutput()
	require.NoError(t, err, string(result))
	var ambientRequests atomic.Int32
	ambient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ambientRequests.Add(1)
		http.Error(w, "ambient credential trap", 500)
	}))
	defer ambient.Close()
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "process.db")})
			require.NoError(t, e)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.ConsumerRuntime = db
			if backend == "http" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
				t.Cleanup(server.Close)
				rt, e = remote.New(server.URL, server.Client())
				require.NoError(t, e)
			}
			celltest.Create(t, rt, "saved-tests", "saved", "example.test/saved")
			f, e := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{Scope: true})
			require.NoError(t, e)
			oracle := f.Steps[2].Base
			if oracle == nil {
				oracle = f.Steps[2].Parent
			}
			before, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			guard := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
			service := &jobinspect.Service{Runtime: guard, Tenant: "saved-tests"}
			run := func(api jobinspect.API, args ...string) ([]byte, []byte, int) {
				t.Helper()
				current := jobcontext.Current{TenantID: "saved-tests", JobID: f.Job.JobId, GoalID: "goal"}
				server, e := childbroker.Start(jobinspect.WithAPI(ctx, api), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
				require.NoError(t, e)
				defer server.Close()
				processCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				command := exec.CommandContext(processCtx, binary, append([]string{"job"}, args...)...)
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
				command.Env = append(command.Env, "C2J_JOBDB="+ambient.URL)
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
			out, diag, code := run(service, "diff", "self")
			require.Zero(t, code, string(diag))
			require.Equal(t, oracle, out)
			require.Empty(t, diag)
			out, diag, code = run(service, "diff", "self", "--json")
			require.Zero(t, code, string(diag))
			var m jobinspect.WorkspaceDiff
			require.NoError(t, json.Unmarshal(out, &m))
			require.Equal(t, "available", m.Status)
			require.Empty(t, diag)
			out, diag, code = run(service, "changes", "self")
			require.Zero(t, code, string(diag))
			require.Contains(t, string(diag), "deprecated")
			require.NoError(t, json.Unmarshal(out, &m))
			path := filepath.Join(t.TempDir(), "exact.patch")
			out, diag, code = run(service, "diff", "self", "--output", path)
			require.Zero(t, code, string(diag))
			require.Empty(t, out)
			actual, e := os.ReadFile(path)
			require.NoError(t, e)
			require.Equal(t, oracle, actual)
			_, _, code = run(service, "diff", "self", "--output", path)
			require.NotZero(t, code)
			actual, e = os.ReadFile(path)
			require.NoError(t, e)
			require.Equal(t, oracle, actual)
			partial := filepath.Join(t.TempDir(), "partial.patch")
			_, diag, code = run(shortChangesProcessAPI{service}, "diff", "self", "--output", partial)
			require.NotZero(t, code, string(diag))
			_, e = os.Stat(partial)
			require.True(t, os.IsNotExist(e))
			out, diag, code = run(service, "diff", "self", "--task", m.Resolved.Task, "--attempt", "1", "--workspace", "root", "--boundary", "before", "--json")
			require.Zero(t, code, string(diag))
			require.NoError(t, json.Unmarshal(out, &m))
			require.NotNil(t, m.Requested.Task)
			require.Equal(t, "before", *m.Requested.Boundary)
			out, diag, code = run(service, "diff", "self", "--all=false", "--json")
			require.NotZero(t, code)
			require.Empty(t, out)
			var diagnostic jobinspect.WorkspaceDiffError
			require.NoError(t, json.Unmarshal(diag, &diagnostic))
			require.Equal(t, "invalid_request", diagnostic.Code)
			story, e := service.Inspect(ctx, jobinspect.Request{Operation: "story", JobID: f.Job.JobId, Limit: 100})
			require.NoError(t, e)
			require.NotEmpty(t, story.Scopes)
			out, diag, code = run(service, "story", "self", "--json", "--limit", "100")
			require.Zero(t, code, string(diag))
			var discovered jobinspect.Response
			require.NoError(t, json.Unmarshal(out, &discovered))
			require.Equal(t, story.Scopes, discovered.Scopes)
			require.NotEmpty(t, discovered.Scopes[len(discovered.Scopes)-1].Attempts)
			scope := story.Scopes[len(story.Scopes)-1].ID
			for _, boundary := range []string{"before", "after"} {
				out, diag, code = run(service, "diff", "self", "--scope", scope, "--attempt", "1", "--workspace", "root", "--boundary", boundary, "--json")
				require.Zero(t, code, string(diag))
				require.NoError(t, json.Unmarshal(out, &m))
				require.Equal(t, scope, *m.Requested.Scope)
				require.Equal(t, boundary, *m.Requested.Boundary)
				out, diag, code = run(service, "diff", "self", "--scope", scope, "--boundary", boundary)
				require.Zero(t, code, string(diag))
				if boundary == "before" {
					require.Empty(t, out)
				} else {
					require.Equal(t, oracle, out)
				}
			}
			missing, e := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{Transform: func(s []workspacedifftest.Step) []workspacedifftest.Step { s[2].Omit = true; return s }})
			require.NoError(t, e)
			out, diag, code = run(service, "diff", missing.Job.JobId, "--json")
			require.NotZero(t, code)
			require.NoError(t, json.Unmarshal(out, &m))
			require.Equal(t, "unavailable", m.Status)
			require.NotEmpty(t, diag)
			_, diag, code = run(unsupportedSavedAPI{service}, "diff", "self")
			require.NotZero(t, code)
			require.NotEmpty(t, diag)
			require.Zero(t, ambientRequests.Load(), "an older helper must never fall back to ambient credentials")
			after, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			require.Equal(t, string(before), string(after))
			require.Zero(t, guard.Mutations.Load())
		})
	}
}

type unsupportedSavedAPI struct{ jobinspect.API }

func (unsupportedSavedAPI) Inspect(context.Context, jobinspect.Request) (*jobinspect.Response, error) {
	return nil, errors.New("unknown inspection operation workspace_diff")
}
