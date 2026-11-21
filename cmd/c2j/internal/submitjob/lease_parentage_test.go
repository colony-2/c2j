package submitjob

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/internal/parentagetest"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

const parentageRecipe = `id: parentage
version: "1"
input_schema:
 prompt: {type: string}
sequence: []
outputs: {}
`

func TestLeaseParentageMissingBroker(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		for _, target := range []string{"flag", "environment", "project", "embed", "invalid-project"} {
			for _, mode := range []string{"ordinary", "incoming", "live", "selected", "replay", "run", "legacy"} {
				t.Run(backend+"/"+target+"/"+mode, func(t *testing.T) {
					clearSubmissionEnv(t)
					f := newWaitFixture(t, backend)
					parent := f.target(t, "parent", "")
					lease, err := f.db.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: parent.JobKey, WorkerID: "parent", Routes: []jobdb.Route{{JobType: "work"}}, LeaseDuration: time.Minute})
					require.NoError(t, err)
					require.NotNil(t, lease)
					for k, v := range jobcontext.EnvForCurrent(jobcontext.Current{TenantID: "waits", JobID: "parent", GoalID: parent.GoalID, InvocationHash: "invocation"}) {
						t.Setenv(k, v)
					}
					var calls atomic.Int32
					handler := celltest.Handler(remote.NewConsumerServer(f.db))
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler.ServeHTTP(w, r) }))
					defer server.Close()
					opts := f.opts
					opts.JobDBURI = ""
					uri := server.URL + "/waits"
					switch target {
					case "flag":
						opts.JobDBURI = uri
					case "environment":
						t.Setenv("C2J_JOBDB", uri)
					case "project", "invalid-project":
						require.NoError(t, os.MkdirAll(filepath.Join(opts.WorkingDir, ".c2"), 0700))
						config := "jobdb: " + uri + "\n"
						if target == "invalid-project" {
							config = "[invalid"
						}
						require.NoError(t, os.WriteFile(filepath.Join(opts.WorkingDir, ".c2/config.yaml"), []byte(config), 0600))
					case "embed":
						opts.JobDBURI = "embed:///"
					}
					recipePath := filepath.Join(opts.WorkingDir, "child.yaml")
					require.NoError(t, os.WriteFile(recipePath, []byte(parentageRecipe), 0600))
					opts.RecipeFile = recipePath
					opts.JobID = "must-not-exist"
					retained := filepath.Join(opts.WorkingDir, "request.json")
					switch mode {
					case "incoming":
						opts.FromWorkspace = true
					case "live":
						opts.FromWorkspaceNow = true
					case "selected":
						opts.GoalID = parent.GoalID
						opts.RequestFile = retained
					case "replay":
						opts = Options{WorkingDir: opts.WorkingDir, JobDBURI: opts.JobDBURI, ReplayFile: retained, Stdout: io.Discard, Stderr: io.Discard}
					case "run":
						opts.RunAfterSubmit = true
						opts.JSONOutput = false
					case "legacy":
						t.Setenv(jobcontext.CurrentContextVersionEnv, "")
					}
					err = Run(f.ctx, opts)
					require.ErrorContains(t, err, "current job context requires a child job broker")
					require.Zero(t, calls.Load(), "route must reject before contacting a direct runtime")
					jobs, err := f.db.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"waits"}})
					require.NoError(t, err)
					require.Len(t, jobs.Jobs, 1, "only the leased parent may exist")
					_, err = os.Stat(retained)
					require.True(t, os.IsNotExist(err), "must reject before retained request creation or replay reads")
				})
			}
		}
	}
}

func TestLeaseParentageStandalone(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			clearSubmissionEnv(t)
			f := newWaitFixture(t, backend)
			parent := f.target(t, "unrelated", "")
			path := filepath.Join(f.opts.WorkingDir, "child.yaml")
			require.NoError(t, os.WriteFile(path, []byte(parentageRecipe), 0600))
			opts := f.opts
			opts.RecipeFile = path
			key := f.submit(t, opts)
			rows, err := f.db.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"waits"}, JobKeys: []jobdb.JobKey{key}})
			require.NoError(t, err)
			require.Len(t, rows.Jobs, 1)
			require.Empty(t, rows.Jobs[0].ParentJobID)
			parentagetest.Check(t, f.ctx, f.wire, parent.JobKey)
		})
	}
}

// A valid direct target must never become a fallback after broker publication
// fails. Expiry/revocation here occur before the publication RPC, unlike response loss.
func TestLeaseParentageBrokerBoundaries(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		for _, fault := range []string{"allowed", "cross-cell", "build", "existing-conflict", "denied", "forbidden-cell", "tenant", "unavailable", "session", "expired", "revoked", "selected"} {
			t.Run(backend+"/"+fault, func(t *testing.T) {
				clearSubmissionEnv(t)
				f := newWaitFixture(t, backend)
				require.NoError(t, jobdbschema.Register(f.ctx, f.db.(jobdb.JobSchemaRegistry), "waits"))
				celltest.Create(t, f.db, "waits", "other", "example.test/other")
				parent := f.target(t, "parent", "")
				duration := time.Minute
				if fault == "expired" {
					duration = time.Millisecond
				}
				lease, err := f.wire.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: parent.JobKey, WorkerID: "parent", Routes: []jobdb.Route{{JobType: "work"}}, LeaseDuration: duration})
				require.NoError(t, err)
				require.NotNil(t, lease)
				grant := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: fault != "denied"}}
				if fault == "forbidden-cell" {
					grant.Jobs.CellNames = []string{"other"}
				}
				var prepared jobdb.SubmitJob
				access := permissions.NewAccess(f.ctx, grant, nil, func(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
					prepared = job
					return (waitLeaseSubmitter{lease}).SubmitJobRef(ctx, job)
				})
				ctx := jobref.WithScope(f.ctx, jobref.Scope{ResolveCell: cellref.Registry(f.db, "waits")})
				current := jobcontext.Current{TenantID: "waits", JobID: "parent", GoalID: parent.GoalID, InvocationHash: "invocation"}
				broker, err := childbroker.Start(ctx, childbroker.Options{Current: current, Submitter: parentageSubmitter{access}})
				require.NoError(t, err)
				defer broker.Close()
				for k, v := range jobcontext.EnvForCurrent(current) {
					t.Setenv(k, v)
				}
				for k, v := range broker.Env() {
					t.Setenv(k, v)
				}
				var directCalls atomic.Int32
				trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					directCalls.Add(1)
					http.Error(w, "direct fallback", 500)
				}))
				defer trap.Close()
				opts := f.opts
				opts.JobDBURI = trap.URL + "/waits"
				opts.JobID = "destination"
				opts.RecipeFile = filepath.Join(opts.WorkingDir, "child.yaml")
				require.NoError(t, os.WriteFile(opts.RecipeFile, []byte(parentageRecipe), 0600))
				switch fault {
				case "build":
					opts.RecipeFile = ""
				case "existing-conflict":
					f.target(t, "destination", "")
				case "cross-cell":
					opts.Cell = "other"
					opts.CellRepo = "example.test/other"
				case "tenant":
					opts.JobDBURI = trap.URL + "/wrong"
				case "unavailable":
					require.NoError(t, broker.Close())
				case "session":
					t.Setenv(jobcontext.ChildJobSessionIDEnv, "invalid")
				case "expired":
					time.Sleep(10 * time.Millisecond)
					require.Error(t, lease.KeepAlive(f.ctx))
				case "revoked":
					require.NoError(t, lease.Reschedule(f.ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "work"}}))
					require.Error(t, lease.KeepAlive(f.ctx))
				case "selected":
					opts.GoalID = parent.GoalID
					opts.RequestFile = filepath.Join(opts.WorkingDir, "request.json")
				}
				err = Run(ctx, opts)
				if fault == "allowed" || fault == "cross-cell" || fault == "build" {
					require.NoError(t, err)
					before, e := f.db.GetChapter(f.ctx, jobdb.ChapterRef{JobKey: jobdb.JobKey{TenantId: "waits", JobId: "destination"}, Ordinal: 0})
					require.NoError(t, e)
					// Retry the identical accepted request through the original lease.
					retry, e := (waitLeaseSubmitter{lease}).SubmitJobRef(f.ctx, prepared)
					require.NoError(t, e)
					require.Equal(t, "destination", retry.JobID)
					// A fresh CLI preparation has a new timestamp and is a conflicting request.
					require.ErrorContains(t, Run(ctx, opts), "does not match request")
					after, e := f.db.GetChapter(f.ctx, jobdb.ChapterRef{JobKey: jobdb.JobKey{TenantId: "waits", JobId: "destination"}, Ordinal: 0})
					require.NoError(t, e)
					require.Equal(t, before, after)
					if fault == "build" {
						var start workflowctl.StartJob
						require.NoError(t, json.Unmarshal(after.Body.(jobdb.JobStartChapter).Input.Data, &start))
						require.Equal(t, "build", start.Inputs["type"])
						require.Equal(t, "continue", start.Inputs["prompt"])
						require.Equal(t, "git+https://github.com/colony-2/recipes.git//build.yaml@main", start.RecipeName)
					}
					parentagetest.Check(t, f.ctx, f.wire, parent.JobKey, "destination")
					rows, e := f.db.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"waits"}})
					require.NoError(t, e)
					require.Len(t, rows.Jobs, 2)
				} else {
					require.Error(t, err)
					rows, e := f.db.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"waits"}})
					require.NoError(t, e)
					if fault == "existing-conflict" {
						require.Len(t, rows.Jobs, 2)
						for _, row := range rows.Jobs {
							require.Empty(t, row.ParentJobID)
						}
					} else {
						require.Len(t, rows.Jobs, 1)
					}
					parentagetest.Check(t, f.ctx, f.wire, parent.JobKey)
				}
				require.Zero(t, directCalls.Load())
			})
		}
	}
}

type parentageSubmitter struct{ access *permissions.Access }

func (s parentageSubmitter) SubmitJobRef(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
	return s.access.Submit(ctx, job)
}
