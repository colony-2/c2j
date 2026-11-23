package recipejob_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/segmentio/ksuid"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

// This adapter changes only the returned handle shape over real storage.
// It does not fabricate receipt assertions or replace backend behavior.
type exactHandleAdapter struct{ jobdbschema.WorkflowEngine }

func (a exactHandleAdapter) SubmitJobRef(c context.Context, j jobdb.SubmitJob) (jobref.Ref, error) {
	r, e := a.WorkflowEngine.SubmitJobRef(c, j)
	r.GoalID = ""
	return r, e
}
func (a exactHandleAdapter) SubmitRestartJobRef(c context.Context, j jobdb.SubmitRestartJobRequest) (jobref.Ref, error) {
	r, e := a.WorkflowEngine.SubmitRestartJobRef(c, j)
	r.GoalID = ""
	return r, e
}

func TestExactJobConsumerReceiptsAndRestart(t *testing.T) {
	for _, backend := range exactJobBackends() {
		t.Run(backend, func(t *testing.T) {
			for _, registered := range []bool{false, true} {
				t.Run(fmt.Sprintf("schema-registered=%t", registered), func(t *testing.T) {
					tenant := "tenant"
					if backend == "postgres" {
						tenant = ksuid.New().String()
					}
					ctx := celltest.Context(context.Background())
					var rt jobdb.ConsumerRuntime = toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
					if backend == "sqlite" || backend == "http" {
						db, e := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
						require.NoError(t, e)
						t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
						rt = db
					}
					if backend == "postgres" {
						var err error
						rt, err = remote.New(os.Getenv("C2J_EXACT_JOB_TEST_URL"), nil)
						require.NoError(t, err)
					}
					celltest.Create(t, rt, tenant, "app", "example.test/app")
					var routesMu sync.Mutex
					var routes []string
					if backend == "http" {
						h := remote.NewConsumerServer(rt)
						s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							routesMu.Lock()
							routes = append(routes, r.URL.Path)
							routesMu.Unlock()
							h.ServeHTTP(w, r.WithContext(celltest.Context(r.Context())))
						}))
						t.Cleanup(s.Close)
						var e error
						rt, e = remote.New(s.URL, s.Client())
						require.NoError(t, e)
					}
					if registered {
						_, err := rt.(jobdb.JobSchemaRegistry).RegisterJobSchema(ctx, jobdb.RegisterJobSchemaRequest{TenantId: tenant, Schema: jobdbschema.Schema()})
						require.NoError(t, err)
					}
					engine := goalEngine(t, rt)
					adapter := exactHandleAdapter{engine}
					start := workflowctl.StartJob{TenantId: tenant, JobID: "destination", Cell: "app", CellRepo: "example.test/app", RecipeName: "build", Inputs: map[string]any{"goal_id": "opaque", "n": json.Number("9007199254740993")}}
					job, e := starter.PrepareRecipeJob(ctx, start, starter.StartRecipeJobOptions{})
					require.NoError(t, e)
					d, e := starter.NewDescriptor(ctx, job)
					require.NoError(t, e)
					frozen, e := json.Marshal(d)
					require.NoError(t, e)
					var refs [4]jobref.Ref
					var errs [4]error
					var wg sync.WaitGroup
					for i := range refs {
						wg.Add(1)
						go func(i int) { defer wg.Done(); refs[i], errs[i] = d.Submit(ctx, adapter) }(i)
					}
					wg.Wait()
					for i := range refs {
						if errs[i] != nil {
							t.Logf("concurrent original request %d was uncertain: %v", i, errs[i])
							refs[i], e = d.Submit(ctx, adapter)
							require.NoError(t, e, "retry the original frozen descriptor after concurrent uncertainty")
						}
						require.Equal(t, refs[0], refs[i])
						require.Empty(t, refs[i].GoalID)
						require.NoError(t, refs[i].ValidateSubmission(job))
					}
					changed := d
					changed.Input = json.RawMessage(strings.Replace(string(d.Input), "9007199254740993", "9007199254740994", 1))
					_, e = changed.Submit(ctx, adapter)
					require.Error(t, e)
					after, e := json.Marshal(d)
					require.NoError(t, e)
					require.Equal(t, frozen, after)
					request, e := starter.PrepareRestartJob(ctx, adapter, refs[0].Key(), starter.SubmissionRestartScopeID, nil, starter.RestartOptions{RequestID: "restart-original"})
					require.NoError(t, e)
					require.False(t, request.Job.ShareGoal)
					restartFrozen, e := json.Marshal(request)
					require.NoError(t, e)
					var restarted [4]jobref.Ref
					var restartErrors [4]error
					for i := range restarted {
						wg.Add(1)
						go func(i int) {
							defer wg.Done()
							restarted[i], restartErrors[i] = starter.ReplayRestartJob(ctx, adapter, request)
						}(i)
					}
					wg.Wait()
					for i := range restarted {
						if restartErrors[i] != nil {
							t.Logf("concurrent original restart %d was uncertain: %v", i, restartErrors[i])
							restarted[i], restartErrors[i] = starter.ReplayRestartJob(ctx, adapter, request)
						}
						require.NoError(t, restartErrors[i], "reconcile concurrent restart %d through the original request ID", i)
					}
					first := restarted[0]
					for i := range restarted {
						require.Equal(t, first, restarted[i])
					}
					require.Empty(t, first.GoalID)
					require.NotEqual(t, refs[0].JobID, first.JobID)
					again, e := starter.ReplayRestartJob(ctx, adapter, request)
					require.NoError(t, e)
					require.Equal(t, first, again)
					request.Job.ShareGoal = true
					_, e = starter.ReplayRestartJob(ctx, adapter, request)
					require.Error(t, e, "request ID cannot acquire a new semantic mode")
					require.NoError(t, json.Unmarshal(restartFrozen, &request))
					request.Job.PriorJobKey.JobId = "missing-source"
					_, e = starter.ReplayRestartJob(ctx, adapter, request)
					require.Error(t, e)
					source, e := rt.GetJob(ctx, refs[0].Key())
					require.NoError(t, e)
					require.NotEqual(t, jobdb.JobStatusCancelled, source.Status)
					_, e = recipejob.BuildListJobsRequest(recipejob.ListRecipeJobsRequest{TenantID: tenant, GoalIDs: []string{"retired"}})
					require.ErrorContains(t, e, "unsupported")
					for _, path := range routes {
						// Replaying the deliberately changed legacy sharing request may
						// discover server capabilities; it must not select/query a goal.
						if path == "/v1/capabilities/job-goals" {
							continue
						}
						require.NotContains(t, path, "goals")
						require.NotContains(t, path, "primary-goal")
					}
				})
			}
		})
	}
}

// CJ12 follows the human's request-ID direction: recover the unchanged
// accepted operation through the original idempotent endpoint, not a new API.
func TestExactJobLegacyFrozenRecovery(t *testing.T) {
	for _, backend := range exactJobBackends() {
		t.Run(backend, func(t *testing.T) {
			tenant := "tenant"
			if backend == "postgres" {
				tenant = ksuid.New().String()
			}
			ctx := jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(context.Background()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })
			var rt jobdb.ConsumerRuntime = toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
			if backend == "sqlite" || backend == "http" {
				db, e := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "legacy.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
				require.NoError(t, e)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				rt = db
			}
			if backend == "postgres" {
				var err error
				rt, err = remote.New(os.Getenv("C2J_EXACT_JOB_TEST_URL"), nil)
				require.NoError(t, err)
			}
			celltest.Create(t, rt, tenant, "app", "example.test/app")
			if backend == "http" {
				h := remote.NewConsumerServer(rt)
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					h.ServeHTTP(w, r.WithContext(jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(r.Context()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })))
				}))
				t.Cleanup(s.Close)
				var e error
				rt, e = remote.New(s.URL, s.Client())
				require.NoError(t, e)
			}
			engine := goalEngine(t, rt)
			start := workflowctl.StartJob{TenantId: tenant, JobID: "source", Cell: "app", CellRepo: "example.test/app", RecipeName: "build"}
			source, e := starter.StartRecipeJob(ctx, start, engine)
			require.NoError(t, e)
			// Historical selection was accepted only after the prior candidate
			// stopped. Retained receipt recovery must preserve that operation.
			require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: source.Key()}))
			start.JobID = "legacy-destination"
			job, e := starter.PrepareRecipeJob(ctx, start, starter.StartRecipeJobOptions{})
			require.NoError(t, e)
			job.GoalID = source.GoalID
			d, e := starter.NewDescriptor(ctx, job)
			require.NoError(t, e)
			d.Version = 1 // legacy fixture, not fresh selected preparation
			original, e := d.Request()
			require.NoError(t, e)
			accepted, e := engine.SubmitJobRef(ctx, original)
			require.NoError(t, e) // response is then lost to the caller
			before, e := json.Marshal(d)
			require.NoError(t, e)
			recovered, e := d.Submit(ctx, engine)
			require.NoError(t, e)
			require.Equal(t, accepted, recovered)
			after, e := json.Marshal(d)
			require.NoError(t, e)
			require.Equal(t, before, after)
			require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: recovered.Key()}))
			legacy := jobdb.SubmitRestartJobRequest{RequestID: "legacy-restart", Job: jobdb.SubmitRestartJob{PriorJobKey: source.Key(), Cell: "app", ShareGoal: true}}
			// An old caller submitted this nil-schema request directly. Recovery
			// through today's wrapper must not inject a different schema selector.
			nativeAccepted, e := rt.SubmitRestartJob(ctx, legacy)
			require.NoError(t, e)
			accepted, e = jobref.FromHandle(nativeAccepted)
			require.NoError(t, e)
			recovered, e = starter.ReplayRestartJob(ctx, engine, legacy)
			require.NoError(t, e)
			require.Equal(t, accepted, recovered)
			require.Equal(t, source.GoalID, recovered.GoalID)
			changed := legacy
			changed.Job.ShareGoal = false
			_, e = starter.ReplayRestartJob(ctx, engine, changed)
			require.Error(t, e)
			_, e = starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: tenant, JobID: "new-selected", GoalID: source.GoalID, Cell: "app", CellRepo: "example.test/app", RecipeName: "build"}, engine)
			require.ErrorContains(t, e, "unsupported")
			_, e = rt.GetJob(ctx, jobdb.JobKey{TenantId: tenant, JobId: "new-selected"})
			require.ErrorIs(t, e, jobdb.ErrJobNotFound)
		})
	}
}

func exactJobBackends() []string {
	backends := []string{"toy", "sqlite", "http"}
	if os.Getenv("C2J_EXACT_JOB_TEST_URL") != "" {
		backends = append(backends, "postgres")
	}
	return backends
}

// Unsupported historical recovery must remain unresolved. An SDK capability
// refusal must never become a fresh independent submission or restart.
func TestExactJobUnsupportedHistoricalRecovery(t *testing.T) {
	for _, discovery := range []string{"absent", "unsupported"} {
		t.Run(discovery, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls = append(calls, r.Method+" "+r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities/primary-goal-selection":
					_, _ = w.Write([]byte(`{"supported":true}`))
				case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities/job-goals":
					if discovery == "absent" {
						http.NotFound(w, r)
					} else {
						_, _ = w.Write([]byte(`{"immutableMembership":false,"atomicAdmission":false}`))
					}
				default:
					http.Error(w, "unexpected recovery fallback", http.StatusInternalServerError)
				}
			}))
			t.Cleanup(server.Close)
			rt, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			engine := goalEngine(t, rt)
			descriptor := starter.Descriptor{Version: 1, Operation: "submit", Job: jobdb.SubmitJob{
				TenantId: "tenant", JobID: "reserved", GoalID: "historical-goal", Cell: "app", CellRepo: "example.test/app",
			}, Input: json.RawMessage(`{"inputs":{"n":9007199254740993}}`)}
			restart := jobdb.SubmitRestartJobRequest{RequestID: "original-request", Job: jobdb.SubmitRestartJob{
				PriorJobKey: jobdb.JobKey{TenantId: "tenant", JobId: "source"}, JobID: "reserved-restart",
				Cell: "app", CellRepo: "example.test/app", ShareGoal: true,
				ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: "original-scope", Payload: json.RawMessage(`{"n":9007199254740993}`)},
			}}
			before, err := json.Marshal([]any{descriptor, restart})
			require.NoError(t, err)
			for attempt := 0; attempt < 2; attempt++ {
				ref, err := descriptor.Submit(context.Background(), engine)
				require.ErrorIs(t, err, jobdb.ErrJobGoalSemanticsUnsupported)
				require.Empty(t, ref.JobID)
				ref, err = starter.ReplayRestartJob(context.Background(), engine, restart)
				require.ErrorIs(t, err, jobdb.ErrJobGoalSemanticsUnsupported)
				require.Empty(t, ref.JobID)
			}
			after, err := json.Marshal([]any{descriptor, restart})
			require.NoError(t, err)
			require.Equal(t, before, after, "retain the complete unresolved requests for explicit disposition")
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, calls, 6)
			for _, call := range calls {
				require.Contains(t, []string{"GET /v1/capabilities/primary-goal-selection", "GET /v1/capabilities/job-goals"}, call,
					"unsupported recovery must not read another identity or issue any mutation")
			}
		})
	}
}
