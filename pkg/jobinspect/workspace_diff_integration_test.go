package jobinspect_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/c2j/pkg/jobinspect/workspacedifftest"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func savedBackend(t *testing.T, backend string) (context.Context, jobdb.ConsumerRuntime) {
	t.Helper()
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	var rt jobdb.ConsumerRuntime = db
	if backend == "HTTP" {
		server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
		t.Cleanup(server.Close)
		rt, err = remote.New(server.URL, server.Client())
		require.NoError(t, err)
	}
	celltest.Create(t, rt, "saved-tests", "saved", "example.test/saved")
	return ctx, rt
}
func strp(s string) *string { return &s }
func TestWorkspaceDiffNativeS01S03S05S10I01I02I03I06(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.Build(ctx, rt, t.TempDir())
			require.NoError(t, err)
			guard := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
			service := &jobinspect.Service{Runtime: guard, Tenant: f.Job.TenantId}
			server, err := childbroker.Start(jobinspect.WithAPI(ctx, service), childbroker.Options{Current: jobcontext.Current{TenantID: f.Job.TenantId, JobID: f.Job.JobId, GoalID: "goal"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, err)
			defer server.Close()
			broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
			require.NoError(t, err)
			client := childbroker.JobClient{Broker: broker}
			log, err := service.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
			require.NoError(t, err)
			require.Len(t, log.Tasks, 4)
			inventory, err := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, err)
			for _, surface := range []struct {
				name   string
				reader jobinspect.WorkspaceDiffAPI
				api    interface {
					Inspect(context.Context, jobinspect.Request) (*jobinspect.Response, error)
				}
			}{{"Service", service, service}, {"Helper", client, client}, {"CLI", workspacedifftest.CLI{Binary: workspacedifftest.PrepareCLI(t), Env: server.Env()}, workspacedifftest.CLI{Binary: workspacedifftest.PrepareCLI(t), Env: server.Env()}}} {
				t.Run(surface.name, func(t *testing.T) {
					q := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId}
					m, body, err := surface.reader.OpenWorkspaceDiff(ctx, q)
					require.NoError(t, err)
					require.NotNil(t, m)
					require.Equal(t, "available", m.Status, "%+v", m.Reasons)
					require.NotNil(t, body)
					require.NoError(t, jobinspect.ValidateWorkspaceDiff(q, m))
					data, err := io.ReadAll(body)
					require.NoError(t, err)
					require.NoError(t, body.Close())
					oracle := f.Steps[2].Base
					if oracle == nil {
						oracle = f.Steps[2].Parent
					}
					require.Equal(t, oracle, data)
					require.Equal(t, log.Tasks[3].ID, m.Resolved.Task)
					require.Equal(t, log.Tasks[3].ID, m.StateProducer.Task)
					require.Equal(t, log.Tasks[2].ID, m.Source.Producer.Task)
					for i, task := range log.Tasks {
						for _, boundary := range []string{"before", "after"} {
							t.Run(task.ID+"/"+boundary, func(t *testing.T) {
								q.WorkspaceDiffOptions = jobinspect.WorkspaceDiffOptions{Task: strp(task.ID), Boundary: strp(boundary)}
								m, body, err := surface.reader.OpenWorkspaceDiff(ctx, q)
								require.NoError(t, err)
								require.NoError(t, jobinspect.ValidateWorkspaceDiff(q, m))
								if i == 0 && boundary == "after" || i == 1 && boundary == "before" {
									require.Equal(t, "unavailable", m.Status, "the first generic producer lacks trusted parent-only proof")
									require.Nil(t, body)
									return
								}
								require.NotEqual(t, "unavailable", m.Status, "%+v", m.Reasons)
								bytes, err := io.ReadAll(body)
								require.NoError(t, err)
								require.NoError(t, body.Close())
								index := i
								if boundary == "before" {
									index--
								}
								if index > 2 {
									index = 2
								}
								var want []byte
								if index >= 0 {
									want = f.Steps[index].Base
									if want == nil {
										want = f.Steps[index].Parent
									}
								}
								require.Equal(t, len(want), len(bytes))
								require.Equal(t, string(want), string(bytes))
								response, err := surface.api.Inspect(ctx, jobinspect.Request{Operation: "workspace_diff", JobID: q.JobID, WorkspaceDiff: &q.WorkspaceDiffOptions})
								require.NoError(t, err)
								require.Equal(t, m, response.WorkspaceDiff)
							})
						}
					}
					for _, options := range []jobinspect.WorkspaceDiffOptions{{Workspace: strp("cell-alias")}, {Task: strp("quoted/μ")}, {Task: strp(log.Tasks[0].ID), Attempt: func() *int { n := 99; return &n }()}} {
						_, _, err := surface.reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: options})
						var typed *jobinspect.WorkspaceDiffError
						require.ErrorAs(t, err, &typed)
						require.Equal(t, "invalid_request", typed.Code)
					}
				})
			}
			after, err := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, err)
			require.Equal(t, json.RawMessage(inventory), json.RawMessage(after))
			require.Zero(t, guard.Mutations.Load())
		})
	}
}

func savedHelper(t *testing.T, ctx context.Context, service *jobinspect.Service, id string) childbroker.JobClient {
	t.Helper()
	server, err := childbroker.Start(jobinspect.WithAPI(ctx, service), childbroker.Options{Current: jobcontext.Current{TenantID: service.Tenant, JobID: id, GoalID: "saved"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	return childbroker.JobClient{Broker: broker}
}

func savedCLIReader(t *testing.T, ctx context.Context, service *jobinspect.Service, id string) workspacedifftest.CLI {
	t.Helper()
	server, err := childbroker.Start(jobinspect.WithAPI(ctx, service), childbroker.Options{Current: jobcontext.Current{TenantID: service.Tenant, JobID: id, GoalID: "saved"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	return workspacedifftest.CLI{Binary: workspacedifftest.PrepareCLI(t), Env: server.Env()}
}
func TestWorkspaceDiffNativeS20AmbiguousWorkspace(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{RootAmbiguous: true})
			require.NoError(t, err)
			service := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
			for _, surface := range []jobinspect.WorkspaceDiffAPI{service, savedHelper(t, ctx, service, f.Job.JobId)} {
				q := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId}
				m, body, err := surface.OpenWorkspaceDiff(ctx, q)
				var typed *jobinspect.WorkspaceDiffError
				require.ErrorAs(t, err, &typed)
				require.Equal(t, "invalid_request", typed.Code)
				require.Contains(t, typed.Detail, "exact workspace")
				require.Nil(t, m)
				require.Nil(t, body)
				q.Workspace = strp("root")
				m, body, err = surface.OpenWorkspaceDiff(ctx, q)
				require.NoError(t, err)
				require.Equal(t, "available", m.Status, "%v", m.Reasons)
				got, err := io.ReadAll(body)
				require.NoError(t, err)
				require.NoError(t, body.Close())
				require.Equal(t, f.Steps[2].Base, got)
			}
		})
	}
}

func TestWorkspaceDiffNativeS11S21UnprovenJoin(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{SplitScopes: true, OmitIncoming: map[int]bool{2: true}})
			require.NoError(t, err)
			service := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
			log, err := service.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
			require.NoError(t, err)
			for _, reader := range []jobinspect.WorkspaceDiffAPI{service, savedHelper(t, ctx, service, f.Job.JobId)} {
				m, body, err := reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
				require.NoError(t, err)
				require.Equal(t, "unavailable", m.Status, "divergent scopes without eligible adoption must not pick a last writer")
				require.Nil(t, body)
				m, body, err = reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: strp(log.Tasks[1].ID)}})
				require.NoError(t, err)
				require.Equal(t, "available", m.Status, "the exact earlier persisted boundary remains readable")
				got, err := io.ReadAll(body)
				require.NoError(t, err)
				require.NoError(t, body.Close())
				require.Equal(t, f.Steps[1].Base, got)
			}
		})
	}
}
