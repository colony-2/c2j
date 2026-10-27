package ops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/gitprofile"
	recipeops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestGitDiagnosticsSurviveFailedTaskAndReplay(t *testing.T) {
	for _, backend := range []string{"toy", "remote"} {
		for _, failure := range []string{"clone", "push"} {
			t.Run(backend+"/"+failure, func(t *testing.T) {
				ctx := context.Background()
				baseRepo, baseHash, cleanup := setupGitRepo(t)
				defer cleanup()
				missing := filepath.Join(t.TempDir(), "missing-repository")
				invoked := false
				reg := ActivityRegistration{TaskType: "git.diagnostic", Step: recipeops.TaskStep{
					Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
						invoked = true
						require.NoError(t, secrets.RedactorFromContext(ctx).Register("private-credential"))
						require.NoError(t, deps.AddOutputArtifact(jobdb.NewArtifactFromBytes("evidence.txt", []byte("retained"))))
						_, err := common.ExecuteGitCommand(ctx, deps.WorktreePath(), "push", "file://"+missing+"/private-credential", "HEAD:refs/heads/main")
						return map[string]interface{}{"status": "failed"}, err
					},
				}}
				worker := &taskWorker{name: reg.TaskType, reg: reg, doer: &opExecutor{
					deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: gitstate.NewController(nil),
				}}
				if failure == "clone" {
					server := httptest.NewServer(http.NotFoundHandler())
					defer server.Close()
					baseRepo = server.URL + "/missing-repository.git"
				}
				input := jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{
					Input: map[string]interface{}{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: baseRepo, BaseRef: baseHash},
				})
				var base jobdb.WorkflowRuntime = toy.New()
				if backend == "remote" {
					server := httptest.NewServer(remote.NewServer(base))
					defer server.Close()
					var err error
					base, err = remote.New(server.URL, server.Client())
					require.NoError(t, err)
				}
				rt := executionruntime.New(base, execution.Allocation{SchemaVersion: 1})
				orchestrator := jobworkflow.ExecutionWorker{JobType: "git-diagnostics", Execute: func(c jobworkflow.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
					return c.DoTask(jobworkflow.TaskCall{Site: "git", TaskType: worker.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`), Policy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}, func(context.Context) (jobdb.TaskData, error) { return data, nil })
				}}
				job, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: orchestrator.Name(), Data: input, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
				require.NoError(t, err)
				h, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: job.JobKey, JobWorker: orchestrator, TaskWorkers: []jobworkflow.TaskWorker{rt.WrapTaskWorker(worker)}, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, err)
				outcome, err := h.Run(nil)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunFailed, outcome.Status)
				require.Equal(t, failure == "push", invoked)
				run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: job.JobKey, IncludeOutputs: true, IncludeArtifacts: true})
				require.NoError(t, err)
				tasks, err := swfutil.ExecutionTasks(run)
				require.NoError(t, err)
				require.Len(t, tasks, 1)
				stored := tasks[0].Attempts[0]
				require.Equal(t, jobdb.TaskAttemptStateFailed, stored.State)
				require.NotNil(t, stored.Output)
				var diagnostic *jobdb.ArtifactInfo
				for i := range stored.Output.Artifacts {
					if stored.Output.Artifacts[i].Name == gitprofile.StderrArtifactName {
						diagnostic = &stored.Output.Artifacts[i]
					}
				}
				require.NotNil(t, diagnostic)
				require.NotNil(t, diagnostic.Key)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(job.JobKey.TenantId).PlusWorkers(orchestrator).BuildEngine()
				require.NoError(t, err)
				observer := &diagnosticObserver{}
				_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: job.JobKey, Observer: observer})
				require.Error(t, err)
				require.Len(t, observer.ends, 1)
				require.NotContains(t, observer.ends[0].Err.Error(), "private-credential")
				artifacts, err := observer.ends[0].Output.GetArtifacts()
				require.NoError(t, err)
				requireArtifactNamed(t, artifacts, gitprofile.StderrArtifactName)
				if failure == "push" {
					requireArtifactNamed(t, artifacts, "evidence.txt")
				}
				for _, artifact := range artifacts {
					if artifact.Name() != gitprofile.StderrArtifactName {
						continue
					}
					raw, err := artifact.Bytes(ctx)
					require.NoError(t, err)
					require.Contains(t, string(raw), "git "+failure+" (failed)")
					require.Contains(t, string(raw), "fatal:")
					require.Contains(t, string(raw), "missing-repository")
					require.NotContains(t, string(raw), "private-credential")
					if failure == "push" {
						require.Contains(t, string(raw), "[REDACTED]")
					}
				}
			})
		}
	}
}
