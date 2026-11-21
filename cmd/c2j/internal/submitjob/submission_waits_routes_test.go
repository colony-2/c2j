package submitjob

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/parentagetest"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

type waitLeaseSubmitter struct{ lease jobdb.ExecutionLease }

func (s waitLeaseSubmitter) SubmitJobRef(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
	handle, err := s.lease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job})
	if err != nil {
		return jobref.Ref{}, err
	}
	return jobref.FromHandle(handle)
}

func TestSubmissionWaitCLIStateSurvivesSQLiteReopen(t *testing.T) {
	for _, kind := range []WaitKind{WaitJob, WaitJobSuccess, WaitGoal} {
		for _, timeFirst := range []bool{false, true} {
			order := "dependency-first"
			if timeFirst {
				order = "time-first"
			}
			t.Run(string(kind)+"/"+order, func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				path := filepath.Join(t.TempDir(), "jobs.db")
				var store *sqlite.Runtime
				var server *httptest.Server
				closeStore := func() {
					if server != nil {
						server.Close()
						server = nil
					}
					if store != nil {
						require.NoError(t, store.Close(context.Background()))
						store = nil
					}
				}
				t.Cleanup(closeStore)
				openStore := func() *waitFixture {
					var err error
					store, err = sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
					require.NoError(t, err)
					server = httptest.NewServer(celltest.Handler(remote.NewConsumerServer(store)))
					wire, err := remote.New(server.URL, server.Client())
					require.NoError(t, err)
					return &waitFixture{ctx: ctx, db: store, wire: wire, opts: Options{JobDBURI: server.URL + "/waits", WorkingDir: t.TempDir(), Cell: "app", CellRepo: "example.test/app", Prompt: "continue", JSONOutput: true, Stdout: io.Discard, Stderr: io.Discard}}
				}
				f := openStore()
				celltest.Create(t, store, "waits", "app", "example.test/app")
				target := f.target(t, "target", "")
				id := target.JobKey.JobId
				if kind == WaitGoal {
					id = target.GoalID
				}
				opts := f.opts
				opts.Waits = []Wait{{kind, id}}
				opts.WaitUntil = []string{"1h"}
				anchor := time.Now().UTC()
				if timeFirst {
					anchor = anchor.Add(-2 * time.Hour)
				}
				opts.Clock = func() time.Time { return anchor }
				key := f.submit(t, opts)
				if !timeFirst {
					f.finish(t, target.JobKey, "succeeded")
				}
				f.blocked(t, key)
				closeStore()
				f = openStore()
				rows, err := f.wire.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"waits"}, JobKeys: []jobdb.JobKey{key}})
				require.NoError(t, err)
				require.Len(t, rows.Jobs, 1)
				require.True(t, anchor.Add(time.Hour).Equal(rows.Jobs[0].AvailableAt))
				f.blocked(t, key)
				if timeFirst {
					f.finish(t, target.JobKey, "succeeded")
					lease, err := f.wire.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "after-reopen", Routes: []jobdb.Route{{JobType: "recipe"}}})
					require.NoError(t, err)
					require.NotNil(t, lease)
				}
			})
		}
	}
}

// Exercise the CLI service and broker against actual lease-bound publication,
// including a submit-only delegation. Request capture alone cannot establish
// that both scheduling gates survive the supported route.
func TestSubmissionWaitBrokerLeaseRoutes(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite"} {
		for _, route := range []string{"native", "http", "delegated"} {
			t.Run(backend+"/"+route, func(t *testing.T) {
				f := newWaitFixture(t, backend)
				require.NoError(t, jobdbschema.Register(f.ctx, f.db.(jobdb.JobSchemaRegistry), "waits"))
				parent := f.target(t, "parent", "")
				var db jobdb.ConsumerRuntime = f.db
				if route != "native" {
					db = f.wire
				}
				lease, err := db.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: parent.JobKey, WorkerID: "parent", Routes: []jobdb.Route{{JobType: "work"}}, LeaseDuration: time.Minute})
				require.NoError(t, err)
				require.NotNil(t, lease)
				if route == "delegated" {
					lease, err = f.wire.CreateDelegation(f.ctx, lease, jobdb.CreateDelegationRequest{Permissions: jobdb.DelegationPermissions{Submit: &jobdb.DelegationSubmitPermission{JobTypes: jobdb.DelegationNames{Names: []string{"recipe"}}, JobIDs: jobdb.DelegationNames{Any: true}}}})
					require.NoError(t, err)
					capability, err := remote.ExportLease(lease)
					require.NoError(t, err)
					encoded, err := capability.Encode()
					require.NoError(t, err)
					capability, err = remote.DecodeLeaseCapability(encoded)
					require.NoError(t, err)
					lease, err = f.wire.ImportLease(f.ctx, capability)
					require.NoError(t, err)
				}
				current := jobcontext.Current{TenantID: "waits", JobID: "parent", GoalID: parent.GoalID, InvocationHash: "invocation"}
				ctx := jobref.WithScope(f.ctx, jobref.Scope{ResolveCell: cellref.Registry(f.db, "waits")})
				broker, err := childbroker.Start(ctx, childbroker.Options{Current: current, Submitter: waitLeaseSubmitter{lease}})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, broker.Close()) })
				for k, v := range jobcontext.EnvForCurrent(current) {
					t.Setenv(k, v)
				}
				for k, v := range broker.Env() {
					t.Setenv(k, v)
				}
				a := f.target(t, "a", "")
				b := f.target(t, "b", "")
				c := f.target(t, "c", "")
				opts := f.opts
				opts.Waits = []Wait{{WaitJob, "a"}, {WaitJobSuccess, "b"}, {WaitGoal, c.GoalID}}
				opts.WaitUntil = []string{"1h"}
				anchor := time.Now().UTC()
				opts.Clock = func() time.Time { return anchor }
				key := f.submit(t, opts)
				rows, err := f.wire.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"waits"}, JobKeys: []jobdb.JobKey{key}})
				require.NoError(t, err)
				require.Len(t, rows.Jobs, 1)
				require.True(t, anchor.Add(time.Hour).Equal(rows.Jobs[0].AvailableAt))
				chapter, err := f.wire.GetChapter(f.ctx, jobdb.ChapterRef{JobKey: key, Ordinal: 0})
				require.NoError(t, err)
				var input workflowctl.StartJob
				require.NoError(t, json.Unmarshal(chapter.Body.(jobdb.JobStartChapter).Input.Data, &input))
				require.Len(t, input.Dependencies, 3)
				require.NotNil(t, input.AvailableAt)
				require.True(t, anchor.Add(time.Hour).Equal(*input.AvailableAt))
				f.blocked(t, key)
				f.finish(t, a.JobKey, "failed_app")
				f.blocked(t, key)
				f.finish(t, b.JobKey, "succeeded")
				f.blocked(t, key)
				f.finish(t, c.JobKey, "succeeded")
				f.blocked(t, key)
				// Resolution can expire during preparation; the retained instant
				// must not be refreshed by the broker or backend.
				opts.Clock = func() time.Time { return anchor.Add(-2 * time.Hour) }
				ready := f.submit(t, opts)
				consumer, err := f.wire.GetJobLease(f.ctx, jobdb.GetJobLeaseRequest{JobKey: ready, WorkerID: "consumer", Routes: []jobdb.Route{{JobType: "recipe"}}})
				require.NoError(t, err)
				require.NotNil(t, consumer)
				parentagetest.Check(t, f.ctx, f.wire, parent.JobKey, key.JobId, ready.JobId)
			})
		}
	}
}
