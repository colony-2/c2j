//go:build integration

package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/joblabel"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/ops/joblabelop"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type labelEditProbe struct {
	jobdb.ConsumerRuntime
	operatorCalls atomic.Int32
	leasedCalls   atomic.Int32
}

func (probe *labelEditProbe) EditJobMetadata(context.Context, jobdb.JobKey, jobdb.JobMetadataEdit) (jobdb.JobMetadataEntry, error) {
	probe.operatorCalls.Add(1)
	return jobdb.JobMetadataEntry{}, fmt.Errorf("operator label fallback must not run")
}

func (probe *labelEditProbe) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := probe.ConsumerRuntime.GetJobLease(ctx, req)
	if lease == nil || err != nil {
		return lease, err
	}
	return &labelLeaseProbe{lease.(jobdb.RenewableExecutionLease), &probe.leasedCalls}, nil
}

type labelLeaseProbe struct {
	jobdb.RenewableExecutionLease
	calls *atomic.Int32
}

func (probe *labelLeaseProbe) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	lease, err := probe.RenewableExecutionLease.Renew(ctx)
	if err != nil {
		return nil, err
	}
	return &labelLeaseProbe{lease, probe.calls}, nil
}

func (probe *labelLeaseProbe) EditJobMetadata(ctx context.Context, edit jobdb.JobMetadataEdit) (jobdb.JobMetadataEntry, error) {
	probe.calls.Add(1)
	return probe.RenewableExecutionLease.(jobdb.JobMetadataLease).EditJobMetadata(ctx, edit)
}

type labelPolicyDispatch struct {
	wf.TaskWorker
	policy *permissions.State
	calls  *atomic.Int32
}

func (dispatch labelPolicyDispatch) Run(ctx wf.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	request["permissions"], err = json.Marshal(dispatch.policy)
	if err != nil {
		return nil, err
	}
	artifacts, err := input.GetArtifacts()
	if err != nil {
		return nil, err
	}
	input, err = jobdb.NewTaskData(request, artifacts...)
	if err != nil {
		return nil, err
	}
	dispatch.calls.Add(1)
	return dispatch.TaskWorker.Run(ctx, input)
}

func runJobLabelFixture(t *testing.T, runtime jobdb.ConsumerRuntime, key jobdb.JobKey, node, pinned string, policy *permissions.State, wantLeaseCalls int32) (wf.JobRunOutcome, wf.Engine) {
	t.Helper()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	engine, err := wf.NewEngineBuilder().WithRuntime(runtime).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
	require.NoError(t, err)
	probe := &labelEditProbe{ConsumerRuntime: runtime}
	allocation := execution.Allocation{SchemaVersion: 2}
	guard := executionruntime.New(probe, allocation)
	if node != "" {
		require.NoError(t, guard.ConfigureJobLabelEdits(key, "git-lifecycle/"+node, pinned))
	}
	var policyCalls atomic.Int32
	wrap := func(worker wf.TaskWorker) wf.TaskWorker {
		guarded := guard.WrapTaskWorker(worker)
		if policy != nil && worker.Name() == "job_label_edit:job_label_edit" {
			return labelPolicyDispatch{guarded, policy, &policyCalls}
		}
		return guarded
	}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(&workerworkflow.SWFWorkflowControl{Engine: engine}).Build()
	workset, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{Allocation: allocation, StageExecution: guard.Stage, WrapTaskWorker: wrap})
	require.NoError(t, err)
	var tasks []wf.TaskWorker
	for _, worker := range workset.TaskWorkers {
		tasks = append(tasks, worker)
	}
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 45*time.Second)
	defer cancel()
	run, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: key, JobWorker: workset.JobWorker, TaskWorkers: tasks, WorkerID: "label-worker", LeaseDuration: time.Minute})
	require.NoError(t, err)
	outcome, err := run.Run(nil)
	require.NoError(t, err)
	if policy != nil {
		require.EqualValues(t, 1, policyCalls.Load(), "injected policy must reach guarded dispatch")
	}
	require.Zero(t, probe.operatorCalls.Load(), "operator edit attempts")
	require.Equal(t, wantLeaseCalls, probe.leasedCalls.Load(), "leased edit attempts")
	return outcome, engine
}

func assertJobLabelFixture(t *testing.T, runtime jobdb.ConsumerRuntime, key jobdb.JobKey, allowed bool) {
	t.Helper()
	state, err := joblabel.GetJobLabel(celltest.Context(context.Background()), runtime, key, "friend")
	require.NoError(t, err)
	if allowed {
		require.Equal(t, joblabel.Present, state.Status)
		require.Equal(t, &joblabel.Label{Key: "friend", Color: joblabel.Blue, Value: "foe", Version: 1}, state.Label)
	} else {
		require.Equal(t, joblabel.Unknown, state.Status)
	}
}

const labelBuiltinStep = `   op: job_label_edit
   inputs: {action: set, key: friend, value: foe, color: blue, expected_version: "0"}
`

const labelFixtureRecipe = `id: git-lifecycle
workspace: {cell: test, ref: main}
sequence:
 - id: label
`

func TestJobLabelsScopedRecipeEdits(t *testing.T) {
	withRegisteredOps(t, joblabelop.GetOp())
	const denied = "lease permission denied"
	for _, scenario := range []struct {
		name, node, yaml, wantErr string
		policy                    *permissions.State
	}{
		{"host-granted", "label", "", "", nil},
		{"explicit-policy", "label", "   perms: {jobdb: {jobs: {submit: false}}}\n", denied, nil},
		{"enclosing-limit", "label", "   perms: {limit: {jobdb: {}}}\n", denied, nil},
		{"host-off", "", "", denied, nil},
		{"wrong-node", "other", "", denied, nil},
		{"invalid-policy", "label", "", "unsupported permission policy version", &permissions.State{Version: -1}},
		{"child-grant", "label", "", denied, &permissions.State{Version: permissions.Version, ChildGrant: &permissions.JobDB{}}},
		{"overrides", "label", "", denied, &permissions.State{Version: permissions.Version, Overrides: []permissions.JobDBOverrides{{Jobs: permissions.JobOverrides{Submit: new(false)}}}}},
		{"hook-lineage", "label", "", denied, &permissions.State{Version: permissions.Version, ChildHookLineage: &permissions.HookLineage{Repository: "example.test/test"}}},
		{"foreign-input-scope", "label", "", denied, &permissions.State{Version: permissions.Version, Inputs: &permissions.InputPolicy{Foreign: true, RootRepository: "example.test/test"}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			runtime := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
			key := submitGitLifecycle(t, runtime, repo, labelFixtureRecipe+scenario.yaml+labelBuiltinStep)
			allowed := scenario.wantErr == ""
			var wantCalls int32
			if allowed {
				wantCalls = 1
			}
			outcome, _ := runJobLabelFixture(t, runtime, key, scenario.node, "", scenario.policy, wantCalls)
			if allowed {
				require.NoError(t, outcome.JobError)
				require.Equal(t, wf.JobRunCompleted, outcome.Status)
			} else {
				require.ErrorContains(t, outcome.JobError, scenario.wantErr)
			}
			assertJobLabelFixture(t, runtime, key, allowed)
		})
	}
}

func createLabelExtension(t *testing.T, repo, path string, failAfter bool) string {
	t.Helper()
	name, err := filepath.Abs(os.Args[0])
	require.NoError(t, err)
	manifest := fmt.Sprintf(`name: label
shell: sh
run: |
  exec env C2J_LABEL_TEST_CHILD=1 C2J_LABEL_TEST_FAIL=%t %q -test.run '^TestHelperJobLabelExtensionProcess$'
input_schema: {type: object}
output_schema: {type: object}
`, failAfter, name)
	commit := commitHookFile(t, repo, path+"/op.yaml", manifest)
	return fmt.Sprintf("git+file://%s//%s@%s", repo, path, commit)
}

func TestHelperJobLabelExtensionProcess(t *testing.T) {
	if os.Getenv("C2J_LABEL_TEST_CHILD") != "1" {
		return
	}
	broker, ok, err := jobcontext.ChildJobBrokerFromEnv(os.Getenv)
	require.True(t, ok)
	require.NoError(t, err)
	if broker.LabelToken != "" {
		for _, attempt := range []struct {
			body, session string
			status        int
		}{
			{`{"action":"remove","key":"friend","expected_version":"0","job_id":"foreign"}`, broker.SessionID, http.StatusBadRequest},
			{`{"action":"remove","key":"friend","expected_version":"0"}`, "forged", http.StatusForbidden},
		} {
			req, err := http.NewRequest(http.MethodPost, broker.Endpoint+"/labels/edit", bytes.NewBufferString(attempt.body))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+broker.Token)
			req.Header.Set("X-C2J-Child-Job-Session", attempt.session)
			req.Header.Set("X-C2J-Job-Label-Token", broker.LabelToken)
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			require.Equal(t, attempt.status, response.StatusCode)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	value, color := "foe", joblabel.Blue
	state, err := (childbroker.JobClient{Broker: broker}).EditCurrentJobLabel(ctx, joblabel.Edit{Action: "set", Key: "friend", Value: &value, Color: &color, ExpectedVersion: new(joblabel.Version)})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if !errors.Is(err, jobdb.ErrLeasePermissionDenied) && !errors.Is(err, jobdb.ErrConflict) {
			os.Exit(19)
		}
		os.Exit(18)
	}
	require.EqualValues(t, 1, *state.Version)
	if os.Getenv("C2J_LABEL_TEST_FAIL") == "true" {
		fmt.Fprintln(os.Stderr, "later extension failure")
		os.Exit(17)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"output": map[string]any{"label": state}})
	os.Exit(0)
}

func TestJobLabelsScopedExtensionEdits(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	for _, scenario := range []struct {
		name, node, policy        string
		changed, overrides, allow bool
	}{
		{"host-granted", "label", "", false, false, true},
		{"explicit-policy", "label", "   perms: {jobdb: {jobs: {submit: false}}}\n", false, false, false},
		{"host-off", "", "", false, false, false},
		{"wrong-node", "other", "", false, false, false},
		{"changed-resolved-selector", "label", "", true, false, false},
		{"caller-env-override", "label", "", false, true, false},
		{"unverifiable-local-source", "label", "", false, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			pinned := createLabelExtension(t, repo, "ops/label", false)
			actual := pinned
			if scenario.changed {
				actual = createLabelExtension(t, repo, "ops/another", false)
			}
			step := fmt.Sprintf("   op: %q\n", actual)
			if scenario.name == "unverifiable-local-source" {
				step = "   op: ./ops/label\n"
			}
			if scenario.overrides {
				step = fmt.Sprintf("   op: extension_execution\n   inputs: {selector: %q, env: {C2J_CHILD_JOB_LABEL_TOKEN: forged}}\n", actual)
			}
			runtime := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
			key := submitGitLifecycle(t, runtime, repo, labelFixtureRecipe+scenario.policy+step)
			var wantCalls int32
			if scenario.allow {
				wantCalls = 1
			}
			outcome, _ := runJobLabelFixture(t, runtime, key, scenario.node, pinned, nil, wantCalls)
			if scenario.allow {
				require.NoError(t, outcome.JobError)
				require.Equal(t, wf.JobRunCompleted, outcome.Status)
			} else {
				require.ErrorContains(t, outcome.JobError, "lease permission denied [exit 18]")
			}
			assertJobLabelFixture(t, runtime, key, scenario.allow)
		})
	}
}

func TestJobLabelsPersistAfterTaskFailure(t *testing.T) {
	withRegisteredOps(t, joblabelop.GetOp(), extops.GetExecutionOp(), commandop.GetOp())
	for _, kind := range []string{"builtin", "extension"} {
		t.Run(kind, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			var pinned string
			step := labelBuiltinStep + " - id: fail\n   op: command_execution\n   inputs: {run: 'exit 17'}\n"
			if kind == "extension" {
				pinned = createLabelExtension(t, repo, "ops/label", true)
				step = fmt.Sprintf("   op: %q\n   retry: {maximum_attempts: 2}\n", pinned)
			}
			runtime := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
			key := submitGitLifecycle(t, runtime, repo, labelFixtureRecipe+step)
			wantCalls := int32(1)
			if kind == "extension" {
				wantCalls = 2
			}
			outcome, engine := runJobLabelFixture(t, runtime, key, "label", pinned, nil, wantCalls)
			require.Equal(t, wf.JobRunFailed, outcome.Status)
			if kind == "extension" {
				require.ErrorContains(t, outcome.JobError, jobdb.ErrConflict.Error())
			} else {
				require.ErrorContains(t, outcome.JobError, "exit 17")
			}
			assertJobLabelFixture(t, runtime, key, true)
			_, err := engine.ReplayJobRun(celltest.Context(context.Background()), wf.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.EqualError(t, err, outcome.JobError.Error())
			assertJobLabelFixture(t, runtime, key, true)
		})
	}
}
