package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/metricshistory"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	googlecel "github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/stretchr/testify/require"
)

func submitStatusRecipe(t *testing.T, rt *timeoutReproRuntime, source string, legacy bool) jobdb.JobKey {
	t.Helper()
	ctx := celltest.Context(context.Background())
	start := workflowctl.StartJob{TenantId: "status-tests", Cell: "test", CellRepo: "example.test/test", RecipeName: "status-test"}
	celltest.Create(t, rt, start.TenantId, start.Cell, start.CellRepo)
	registry, ok := rt.ConsumerRuntime.(jobdb.JobSchemaRegistry)
	require.True(t, ok)
	require.NoError(t, jobdbschema.Register(ctx, registry, start.TenantId))
	if legacy {
		data := jobdb.NewTaskDataOrPanic(start, jobdb.NewArtifactFromBytes(start.RecipeName+starter.RecipeArtifactSuffix, []byte(source)))
		submission := jobdb.SubmitJob{TenantId: start.TenantId, Cell: start.Cell, CellRepo: start.CellRepo, JobType: starter.RecipeJobType, Data: data}
		require.NoError(t, jobdbschema.SetSubmitJobSchema(&submission))
		handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: submission})
		require.NoError(t, err)
		return handle.JobKey
	}
	parsed, err := recipe.LoadRecipeFromString([]byte(source))
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	ref, err := starter.StartRecipeJob(ctx, start, jobdbschema.WorkflowEngine{Engine: engine, Registry: registry}, *parsed)
	require.NoError(t, err)
	return ref.Key()
}

func runStatusRecipe(t *testing.T, ctx context.Context, rt jobdb.ConsumerRuntime, key jobdb.JobKey, opts RecipeJobWorkerOptions, overrides ...jobworkflow.TaskWorker) (jobworkflow.JobRunOutcome, error) {
	t.Helper()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	workers, err := NewRecipeWorkerWithOptions(ops.NewServiceDepsBuilder().Build(), registry, opts)
	require.NoError(t, err)
	for _, worker := range overrides {
		workers.TaskWorkers[worker.Name()] = worker
	}
	workersList := make([]jobworkflow.TaskWorker, 0, len(workers.TaskWorkers))
	for _, worker := range workers.TaskWorkers {
		workersList = append(workersList, worker)
	}
	job, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: workers.JobWorker, TaskWorkers: workersList, WorkerID: "status-test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	return job.Run(nil)
}

func recipeStatusRecords(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey) []jobdb.ExecutionRecord {
	t.Helper()
	run, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true})
	require.NoError(t, err)
	return statusRecordsFromRun(t, run)
}

func statusRecordsFromRun(t *testing.T, run jobdb.GetJobRunResponse) []jobdb.ExecutionRecord {
	t.Helper()
	var records []jobdb.ExecutionRecord
	for _, entry := range run.Execution {
		record := entry.Record
		if record.Kind == "result" && strings.Contains(record.ID, `task:"status:`) {
			require.NotNil(t, record.Output)
			require.Empty(t, record.Output.Artifacts)
			records = append(records, record)
		}
	}
	return records
}

func recipeStatusValues(t *testing.T, records []jobdb.ExecutionRecord) []string {
	t.Helper()
	values := make([]string, 0, len(records))
	for _, record := range records {
		var envelope struct {
			Kind    string          `json:"kind"`
			Status  json.RawMessage `json:"status"`
			Metrics json.RawMessage `json:"metrics"`
		}
		require.NoError(t, json.Unmarshal(record.Output.Data, &envelope))
		require.Equal(t, "internal_result", envelope.Kind)
		require.Empty(t, envelope.Metrics)
		values = append(values, string(envelope.Status))
	}
	return values
}

func TestRecipeStatusRecordedAtAcceptance(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, backend := range []string{"toy", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := newTimeoutReproRuntime(t, backend)
			key := submitStatusRecipe(t, rt, `id: status-test
sequence:
 - id: first
   op: scope_probe
   inputs: {name: keep-business-output-separate}
   status: working
 - id: next
   sequence: []
 - id: skipped
   when: 'false'
   sequence: []
   status: skipped
 - id: caught
   op: scope_probe
   inputs: {name: caught}
   status: '${{ {"message": failure.message, "count": 9007199254740993} }}'
   catch:
     - when: 'true'
       continue: {outputs: {name: recovered}}
 - id: clear
   sequence: []
   status: null
outputs:
  data: '${{ sequence.first.outputs.name }}'
  recovered: '${{ sequence.caught.outputs.name }}'
`, false)
			_, git := GenerateTestContext()
			op := &scopeProbeTask{calls: map[string]int{}, failOnce: "caught", git: git}
			outcome, err := runStatusRecipe(t, ctx, rt, key, RecipeJobWorkerOptions{}, op)
			require.NoError(t, err)
			output := gitLifecycleOutput(t, outcome)
			require.Equal(t, "keep-business-output-separate", output["data"])
			require.Equal(t, "recovered", output["recovered"])
			require.NotContains(t, output, "status")
			require.Equal(t, map[string]int{"keep-business-output-separate": 1, "caught": 1}, op.calls)
			records := recipeStatusRecords(t, rt, key)
			require.Equal(t, []string{`"working"`, `"working"`, `{"count":9007199254740993,"message":"probe failed"}`, `null`, `null`}, recipeStatusValues(t, records))
			for _, record := range records {
				require.NotContains(t, string(record.Output.Data), "keep-business-output-separate")
				if record.Input != nil {
					require.NotContains(t, string(record.Input.Data), "keep-business-output-separate")
				}
			}
			worker := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(worker).BuildEngine()
			require.NoError(t, err)
			replayed, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key})
			require.NoError(t, err)
			require.JSONEq(t, string(outcome.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			require.Equal(t, records, recipeStatusRecords(t, rt, key))
		})
	}
}

func TestRecipeStatusEncodedCap(t *testing.T) {
	accepted := []any{strings.Repeat("x", 1021), strings.Repeat("é", 511), map[string]any{"s": strings.Repeat("a", 1016)}}
	rejected := []any{strings.Repeat("x", 1023), strings.Repeat(`"`, 512), strings.Repeat("é", 512)}
	var source strings.Builder
	source.WriteString("id: status-test\nsequence:\n - sequence: []\n   status: '${{ unknown }}'\n")
	want := []string{""}
	for index, value := range accepted {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		require.LessOrEqual(t, len(encoded), 1024)
		fmt.Fprintf(&source, " - sequence: []\n   status: %s\n", encoded)
		want = append(want, string(encoded))
		oversize, err := json.Marshal(rejected[index])
		require.NoError(t, err)
		require.Greater(t, len(oversize), 1024)
		fmt.Fprintf(&source, " - sequence: []\n   status: %s\n", oversize)
		want = append(want, string(encoded))
	}
	source.WriteString(" - sequence: []\n   status: null\n - sequence: []\n   status: '${{ unknown }}'\n")
	want = append(want, "null", "null", "null")
	rt := newTimeoutReproRuntime(t, "remote")
	key := submitStatusRecipe(t, rt, source.String(), false)
	var logs bytes.Buffer
	ctx := celltest.Context(context.Background())
	options := RecipeJobWorkerOptions{Logger: slog.New(slog.NewTextHandler(&logs, nil))}
	outcome, err := runStatusRecipe(t, ctx, rt, key, options)
	require.NoError(t, err)
	gitLifecycleOutput(t, outcome)
	require.Equal(t, want, recipeStatusValues(t, recipeStatusRecords(t, rt, key)))
	require.Equal(t, 5, strings.Count(logs.String(), "Recipe status was not updated"))
	require.NotContains(t, logs.String(), "unknown")
	require.NotContains(t, logs.String(), strings.Repeat("x", 20))
}

type statusCELSpy struct {
	template.CELOptionsProvider
	calls   atomic.Int64
	invalid bool
}

func newStatusCELSpy(invalid bool) *statusCELSpy {
	return &statusCELSpy{CELOptionsProvider: funcregistry.NewBuilder().WithDefaults(), invalid: invalid}
}

func (spy *statusCELSpy) FunctionOptions(adapter types.Adapter) ([]googlecel.EnvOption, error) {
	options, err := spy.CELOptionsProvider.FunctionOptions(adapter)
	if err != nil {
		return nil, err
	}
	return append(options, googlecel.Function("record_status_spy", googlecel.Overload("record_status_spy_zero", nil, googlecel.StringType, googlecel.FunctionBinding(func(...ref.Val) ref.Val {
		call := spy.calls.Add(1)
		if spy.invalid {
			return types.NewErr("sensitive rejection evidence")
		}
		return types.String(fmt.Sprintf("status %d", call))
	})))), nil
}

func TestRecipeStatusLegacyAndNoDeclarations(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			rt := newTimeoutReproRuntime(t, "toy")
			source := "id: status-test\nsequence: []\noutputs: {business: unchanged}\n"
			if legacy {
				source += "status: '${{ record_status_spy() }}'\n"
			}
			spy := newStatusCELSpy(false)
			key := submitStatusRecipe(t, rt, source, legacy)
			ctx := celltest.Context(context.Background())
			outcome, err := runStatusRecipe(t, ctx, rt, key, RecipeJobWorkerOptions{CELOptionsProvider: spy})
			require.NoError(t, err)
			require.Equal(t, "unchanged", gitLifecycleOutput(t, outcome)["business"])
			require.Empty(t, recipeStatusRecords(t, rt, key))
			require.NoError(t, probeReplay(t, rt, key, NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true, CELOptionsProvider: spy})))
			require.Zero(t, spy.calls.Load())
		})
	}
}

func TestRecipeStatusRetainedRestart(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			runtime := newTimeoutReproRuntime(t, "remote")
			ctx := celltest.Context(context.Background())
			key := submitStatusRecipe(t, runtime, `id: status-test
sequence:
  - id: prefix
    sequence: []
    status: '${{ record_status_spy() }}'
  - id: restart-here
    sequence: []
outputs: {business: unchanged}
`, legacy)
			spy := newStatusCELSpy(false)
			options := RecipeJobWorkerOptions{CELOptionsProvider: spy}
			original, err := runStatusRecipe(t, ctx, runtime, key, options)
			require.NoError(t, err)
			require.Equal(t, "unchanged", gitLifecycleOutput(t, original)["business"])
			run, err := jobdb.GetJobRun(ctx, runtime, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			var boundary string
			for _, execution := range run.Execution {
				if execution.Record.Kind == "scope" && strings.HasSuffix(execution.Record.ID, `scope:"sequence:restart-here"#1`) {
					boundary = execution.Record.ID
					break
				}
			}
			require.NotEmpty(t, boundary)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
			require.NoError(t, err)
			registry := runtime.ConsumerRuntime.(jobdb.JobSchemaRegistry)
			restarted, err := starter.RestartRecipeJob(ctx, jobdbschema.WorkflowEngine{Engine: engine, Registry: registry}, key, boundary, nil, starter.RestartOptions{RequestID: "status-retained-restart"})
			require.NoError(t, err)
			outcome, err := runStatusRecipe(t, ctx, runtime, restarted.Key(), options)
			require.NoError(t, err)
			require.Equal(t, "unchanged", gitLifecycleOutput(t, outcome)["business"])
			records := recipeStatusRecords(t, runtime, restarted.Key())
			if legacy {
				require.Empty(t, records)
				require.Zero(t, spy.calls.Load())
			} else {
				require.Equal(t, []string{`"status 1"`, `"status 1"`, `"status 1"`}, recipeStatusValues(t, records))
				require.EqualValues(t, 1, spy.calls.Load(), "the retained prefix must not re-render")
			}
		})
	}
}

type statusResultInterruption struct {
	jobdb.ConsumerRuntime
	lease  jobdb.ExecutionLease
	cancel context.CancelFunc
	commit bool
	fired  bool
}

func (fault *statusResultInterruption) GetJobLease(ctx context.Context, request jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := fault.ConsumerRuntime.GetJobLease(ctx, request)
	fault.lease = lease
	return lease, err
}

func (fault *statusResultInterruption) PutChapter(ctx context.Context, request jobdb.PutChapterRequest) error {
	if body, ok := request.Chapter.Body.(jobdb.ExecutionChapter); ok && !fault.fired {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		if record.Kind == "result" && strings.Contains(record.ID, `task:"status:`) {
			if fault.commit {
				if err := fault.ConsumerRuntime.PutChapter(ctx, request); err != nil {
					return err
				}
			}
			if err := fault.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: starter.RecipeJobType}}); err != nil {
				return err
			}
			fault.fired = true
			fault.cancel()
			return fmt.Errorf("interrupted recipe status publication")
		}
	}
	return fault.ConsumerRuntime.PutChapter(ctx, request)
}

func TestRecipeStatusRecoversCommittedResult(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid=%t", invalid), func(t *testing.T) {
			backend := newTimeoutReproRuntime(t, "remote")
			key := submitStatusRecipe(t, backend, `id: status-test
sequence:
 - id: first
   sequence: []
   status: '${{ record_status_spy() }}'
 - id: next
   sequence: []
outputs: {business: complete}
`, false)
			ctx := celltest.Context(context.Background())
			interruptible, cancel := context.WithCancel(ctx)
			defer cancel()
			fault := &statusResultInterruption{ConsumerRuntime: backend, cancel: cancel, commit: true}
			spy := newStatusCELSpy(invalid)
			options := RecipeJobWorkerOptions{CELOptionsProvider: spy}
			_, err := runStatusRecipe(t, interruptible, fault, key, options)
			require.Error(t, err)
			require.True(t, fault.fired)
			require.EqualValues(t, 1, spy.calls.Load())
			before := recipeStatusRecords(t, backend, key)
			require.Len(t, before, 1)
			outcome, err := runStatusRecipe(t, ctx, backend, key, options)
			require.NoError(t, err)
			require.Equal(t, "complete", gitLifecycleOutput(t, outcome)["business"])
			after := recipeStatusRecords(t, backend, key)
			require.Equal(t, before[0], after[0])
			want := []string{`"status 1"`, `"status 1"`, `"status 1"`}
			if invalid {
				want = []string{""}
			}
			require.Equal(t, want, recipeStatusValues(t, after))
			options.ReadOnlyReplay = true
			require.NoError(t, probeReplay(t, backend, key, NewRecipeJobWorker(options)))
			require.EqualValues(t, 1, spy.calls.Load())
		})
	}
}

func TestRecipeStatusInspectionOfUnfinishedCheckpoint(t *testing.T) {
	backend := newTimeoutReproRuntime(t, "remote")
	key := submitStatusRecipe(t, backend, "id: status-test\nstatus: '${{ record_status_spy() }}'\nsequence: []\n", false)
	ctx := celltest.Context(context.Background())
	interruptible, cancel := context.WithCancel(ctx)
	defer cancel()
	fault := &statusResultInterruption{ConsumerRuntime: backend, cancel: cancel}
	spy := newStatusCELSpy(false)
	options := RecipeJobWorkerOptions{CELOptionsProvider: spy}
	_, err := runStatusRecipe(t, interruptible, fault, key, options)
	require.Error(t, err)
	require.True(t, fault.fired)
	require.EqualValues(t, 1, spy.calls.Load())
	before, err := backend.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(t, err)
	options.ReadOnlyReplay = true
	err = probeReplay(t, backend, key, NewRecipeJobWorker(options))
	var miss jobworkflow.ReplayCacheMissError
	require.ErrorAs(t, err, &miss)
	require.Equal(t, task.RecipeStatusTaskType, miss.TaskType)
	_, payload, err := decodeRecipeStatus(miss.Input)
	require.NoError(t, err)
	require.Empty(t, payload.Rejection)
	require.EqualValues(t, 1, spy.calls.Load())
	after, err := backend.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Empty(t, recipeStatusRecords(t, backend, key))
}

func TestRecipeStatusRepeatedStateVisits(t *testing.T) {
	backend := newTimeoutReproRuntime(t, "remote")
	key := submitStatusRecipe(t, backend, `id: status-test
state:
  initial: {to: loop, payload: {count: 1}}
  states:
    loop:
      sequence: []
      outputs: {count: '${{ transition.payload.count }}'}
      status: {visit: '${{ transition.payload.count }}', mark: '${{ record_status_spy() }}'}
      transitions:
        - to: loop
          when: 'outputs.count < 2'
          payload: {count: '${{ outputs.count + 1 }}'}
`, false)
	ctx := celltest.Context(context.Background())
	spy := newStatusCELSpy(false)
	options := RecipeJobWorkerOptions{CELOptionsProvider: spy}
	outcome, err := runStatusRecipe(t, ctx, backend, key, options)
	require.NoError(t, err)
	gitLifecycleOutput(t, outcome)
	records := recipeStatusRecords(t, backend, key)
	require.Equal(t, []string{`{"mark":"status 1","visit":1}`, `{"mark":"status 2","visit":2}`, `{"mark":"status 2","visit":2}`}, recipeStatusValues(t, records))
	require.NotEqual(t, records[0].ID, records[1].ID)
	options.ReadOnlyReplay = true
	require.NoError(t, probeReplay(t, backend, key, NewRecipeJobWorker(options)))
	require.EqualValues(t, 2, spy.calls.Load())
	require.Equal(t, records, recipeStatusRecords(t, backend, key))
}

func TestRecipeStatusInlineCompletionOwnership(t *testing.T) {
	for _, bind := range []string{"eager", "late"} {
		for _, container := range []string{"sequence", "state", "state-with-policy"} {
			t.Run(bind+"/"+container, func(t *testing.T) {
				fixture := newLateGitFixture(t)
				commit := commitHookFile(t, fixture.dir, "status-child.yaml", `id: selected
vars: {phase: callee}
status: '${{ vars.phase }}'
sequence:
  - id: detail
    status: descendant
    sequence: []
outputs: {data: finished}
`)
				selector := fixture.selector("status-child.yaml", commit)
				include := fmt.Sprintf("{recipe: %q}", selector)
				if bind == "late" {
					include = fmt.Sprintf("{bind: late, recipe: %q}", selector)
				}
				var source string
				if container != "sequence" {
					source = fmt.Sprintf("id: root\nstate:\n  initial: call\n  states:\n    call:\n      include: %s\n      status: null\noutputs: {data: '${{ states.call.outputs.data }}'}\n", include)
					if container == "state-with-policy" {
						source += "catch:\n  - when: 'false'\n    continue: {outputs: {data: unexpected}}\n"
					}
				} else {
					source = fmt.Sprintf("id: root\nsequence:\n  - id: call\n    include: %s\noutputs: {data: '${{ sequence.call.outputs.data }}'}\n", include)
				}
				rec := fixture.expand(t, source)
				rt := newTimeoutReproRuntime(t, "toy")
				guard := lateGuard(rt)
				worker := jobworkflow.ExecutionWorker{JobType: "status-include", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
					history := recipeStatusHistory{logger: exec.Logger()}
					job, git := GenerateTestContext()
					out, artifacts, err := ExecuteRecipe(workflow.Context{JobContext: exec, RecordStatus: history.record}, rec, nil, job, git)
					if err != nil {
						return nil, err
					}
					return jobdb.NewTaskData(out, artifacts...)
				}}
				key := probeSubmit(t, rt, worker)
				workers := append(lateTasks(t, guard), recipeStatusWorker{})
				outcome := lateRun(t, guard, key, worker, workers...)
				require.Equal(t, "finished", gitLifecycleOutput(t, outcome)["data"])
				want := []string{`"descendant"`, `"callee"`, `"callee"`}
				if container != "sequence" {
					want = []string{`"descendant"`, `null`, `null`}
				}
				require.Equal(t, want, recipeStatusValues(t, recipeStatusRecords(t, rt, key)))
				fixture.available.Store(false)
				require.NoError(t, probeReplay(t, rt, key, worker))
			})
		}
	}
}

func TestRecipeStatusParallelChildren(t *testing.T) {
	workspaceTestWorker(t)
	repo := filepath.Join(t.TempDir(), "source")
	workspaceRepo(t, repo, "root")
	engine := objectEngine(t)
	ctx := celltest.Context(context.Background())
	cellRepo := celltest.Repo(repo)
	celltest.Create(t, engine, "object-tests", "test", cellRepo)
	document, err := recipe.LoadRecipeFromString([]byte(`id: status-parent
sequence:
  - id: start
    sequence: []
    status: starting
  - id: join
    parallel:
      branches:
        - id: independent
          sequence: []
        - id: reporting
          sequence: []
          status: reporting
`))
	require.NoError(t, err)
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "status-parent", Cell: "test", CellRepo: cellRepo, GitRef: "main",
		JobContext: contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}, engine, *document)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, ref.Key(), engine))
	parent, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: ref.Key(), IncludeOutputs: true})
	require.NoError(t, err)
	require.Equal(t, []string{`"starting"`, `"starting"`, `"starting"`}, recipeStatusValues(t, statusRecordsFromRun(t, parent)))
	for _, record := range metricshistory.Records(parent) {
		require.NotContains(t, record.Task, `task:"status:`)
	}
	children, err := engine.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{ref.Key().TenantId}, ParentJobIDs: []string{ref.Key().JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, children.Jobs, 2)
	var histories []string
	for _, child := range children.Jobs {
		require.Equal(t, starter.RecipeJobType, child.JobType)
		run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: child.JobKey, IncludeInputs: true, IncludeOutputs: true})
		require.NoError(t, err)
		require.NotNil(t, run.Start.Input)
		input, _, err := decodeRecipeInput(run.Start.Input.Data, nil)
		require.NoError(t, err)
		require.Equal(t, workflowctl.StatusHistoryVersion, input.StatusHistoryVersion)
		histories = append(histories, strings.Join(recipeStatusValues(t, statusRecordsFromRun(t, run)), ","))
	}
	require.ElementsMatch(t, []string{"", `"reporting"`}, histories)
}
