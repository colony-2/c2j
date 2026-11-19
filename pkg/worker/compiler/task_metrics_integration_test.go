package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricshistory"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/colony-2/c2j/pkg/swfutil"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type metricFixtureInput struct{}
type metricFixtureOutput struct {
	Metrics string `json:"metrics"`
}

// Exercise the registered workers and their common admission/setup wrappers,
// including committed failures, rather than constructing task envelopes by hand.
func TestAutomaticMetricsThroughCompilerAndReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			const large = int64(9007199254740993)
			runs := 0
			native := coreops.NewActivityMappedOpV2[metricFixtureInput, metricFixtureOutput](coreops.OpMetadata{Type: "metrics_fixture"}, func(_ coreops.OpDependencies, ctx context.Context, _ metricFixtureInput) (metricFixtureOutput, error) {
				runs++
				metrics.FromContext(ctx).AddInt("llm_tokens_total", "{token}", map[string]string{"provider": "fixture", "model": "returned-model", "token_type": "input"}, large)
				return metricFixtureOutput{Metrics: "ordinary business output"}, nil
			})
			workspaceTestWorker(t, native, commandop.GetOp(), extops.GetExecutionOp())
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			calls := filepath.Join(t.TempDir(), "extension-calls")
			wire := `{"output":{},"metrics":{"version":1,"mutations":[{"name":"llm_tokens_total","kind":"counter","number_type":"int64","unit":"{token}","labels":{"provider":"fixture","model":"returned-model","token_type":"input"},"op":"add","value":2}]}}`
			body := shellcmd.LiteralLine("call") + " >> " + shellcmd.QuotePOSIX(calls) + "\n" + shellcmd.LiteralLine(wire)
			flag := shellcmd.QuotePOSIX(calls + "-first")
			for path, ending := range map[string]string{"op": "\nif test ! -f " + flag + "; then\n  " + shellcmd.LiteralLine("failed") + " > " + flag + "\n  exit 7\nfi", "op-failed": "\nexit 7"} {
				commitHookFile(t, repo, path+"/op.yaml", "name: metric-failure\nshell: sh\nrun: |\n  "+strings.ReplaceAll(body+ending, "\n", "\n  ")+"\ninput_schema: {type: object}\noutput_schema: {type: object}\n")
			}
			malformed := `{"output":{"ok":true},"metrics":{"version":1,"identity":"forged","mutations":[]}}`
			commitHookFile(t, repo, "op-malformed/op.yaml", "name: malformed-metrics\nshell: sh\nrun: |\n  "+shellcmd.LiteralLine(malformed)+"\ninput_schema: {type: object}\noutput_schema: {type: object}\n")
			rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
			key := submitGitLifecycle(t, rt, repo, fmt.Sprintf(`id: git-lifecycle
workspace: {cell: test, repo: %q, ref: main}
sequence:
 - id: native
   op: metrics_fixture
 - id: retried
   op: ./op
   retry: {maximum_attempts: 2, initial_interval: 1ms}
 - id: usage
   op: ./op-failed
   catch:
    - when: true
      continue: {outputs: {caught: true}}
 - id: command
   op: command_execution
   inputs: {shell: sh, run: 'printf ''%%s'' ''{"metrics":{"business":true}}'''}
 - id: malformed
   op: ./op-malformed
outputs:
 metrics: ordinary job metrics
 native: '${{ sequence.native.outputs.metrics }}'
 caught: '${{ sequence.usage.outputs.caught }}'
 command: '${{ sequence.command.outputs }}'
 malformed: '${{ sequence.malformed.outputs.ok }}'
 job_metrics: '${{ context.metrics }}'
 scope_metrics: '${{ scope.metrics }}'
 retry_metrics: '${{ sequence.retried.metrics }}'
 caught_metrics: '${{ sequence.usage.metrics }}'
`, celltest.Repo(repo)))
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			if out.Status != wf.JobRunCompleted {
				history, e := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
				require.NoError(t, e)
				for _, entry := range history.Execution {
					t.Logf("%d %s %s error=%s", entry.Ordinal, entry.Record.Kind, entry.Record.ID, entry.Record.Error)
				}
			}
			require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			var result map[string]any
			require.NoError(t, jsonexact.Unmarshal(out.Output.GetDataOrPanic(), &result))
			require.Equal(t, "ordinary job metrics", result["metrics"])
			require.Equal(t, "ordinary business output", result["native"])
			require.Equal(t, true, result["caught"])
			require.Equal(t, true, result["malformed"])
			require.Contains(t, fmt.Sprint(result["command"]), `{"metrics":{"business":true}}`)
			ctx := celltest.Context(context.Background())
			api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
			inspected, err := api.Inspect(ctx, jobinspect.Request{Operation: "metrics", JobID: key.JobId, Metrics: &metrics.Query{Deltas: true}})
			require.NoError(t, err)
			r := inspected.Metrics
			require.Equal(t, "complete", r.Snapshot.Coverage.History)
			require.Equal(t, large+6, r.Snapshot.Series["llm_tokens_total"][0].Value)
			for _, field := range []string{"job_metrics", "scope_metrics", "retry_metrics", "caught_metrics"} {
				raw, e := json.Marshal(result[field])
				require.NoError(t, e)
				var snapshot metrics.Snapshot
				require.NoError(t, json.Unmarshal(raw, &snapshot))
				if field == "job_metrics" {
					require.Equal(t, r.Snapshot.Series, snapshot.Series)
					require.Equal(t, r.Snapshot.Coverage, snapshot.Coverage)
					continue
				}
				scopeSite := map[string]string{"scope_metrics": "sequence:git-lifecycle", "retry_metrics": "op:retried", "caught_metrics": "op:usage"}[field]
				var scopeID string
				for _, report := range r.Reports {
					segments, e := metrics.Segments(report.Task)
					require.NoError(t, e)
					for i, segment := range segments {
						if segment == "scope:"+strconv.Quote(scopeSite)+"#1" {
							scopeID = "/" + strings.Join(segments[:i+1], "/")
							break
						}
					}
					if scopeID != "" {
						break
					}
				}
				require.NotEmpty(t, scopeID, field)
				selected, e := metricshistory.Read(ctx, rt, key, metrics.Query{Scope: scopeID, ThroughOrdinal: &snapshot.Cutoff})
				require.NoError(t, e)
				require.Equal(t, selected.Snapshot.Series, snapshot.Series, field)
				require.Equal(t, selected.Snapshot.Coverage, snapshot.Coverage, field)
			}
			producerCount := 0
			invalidCount := 0
			runtimeWorkers := map[string]bool{}
			for _, rec := range r.Reports {
				if rec.Channel == "producer" && rec.ChannelReport.Status == "invalid" {
					invalidCount++
					require.Contains(t, rec.Task, "op:malformed")
				}
				if rec.Channel == "producer" && rec.ChannelReport.Status == "valid" {
					producerCount++
					if strings.Contains(rec.Task, "op:usage") || strings.Contains(rec.Task, `op:retried"#1/attempt:1`) {
						logged, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Task: rec.Task})
						require.NoError(t, err)
						require.Equal(t, "not_produced", logged.Task.OutputAvailability)
					}
				}
				if rec.Channel == "runtime" {
					require.Equal(t, "valid", rec.ChannelReport.Status, rec.Task)
					require.Equal(t, "succeeded", rec.ChannelReport.Phases["preparation"], rec.Task)
					for _, m := range rec.ChannelReport.Report.Mutations {
						if m.Name == "c2j_task_duration_seconds" {
							runtimeWorkers[m.Labels["task_type"]] = true
						}
					}
				}
			}
			require.Equal(t, 4, producerCount, "one native, two failed and one successful extension reports")
			require.Equal(t, 1, invalidCount, "invalid producer metadata preserves automatic runtime reports and business outcome")
			for _, worker := range []string{"metrics_fixture:metrics_fixture", "command_execution:command_execution", "extension_execution:extension_execution", WorkspaceResolutionTaskType} {
				require.True(t, runtimeWorkers[worker], "missing automatic timing for %s: %v", worker, runtimeWorkers)
			}
			chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			stored, err := metricshistory.Stored(chapters[len(chapters)-1])
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.Equal(t, r.Snapshot.Series, stored.Series)
			require.Equal(t, r.Snapshot.Coverage, stored.Coverage)
			// The recipe did not request c2j_metrics. The job output and the last
			// chapter must expose all operations, including the failed retry and
			// caught failure, without relying on a metrics inspection request.
			var automatic struct {
				Metrics metrics.Snapshot `json:"c2j_metrics"`
			}
			require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &automatic))
			require.Equal(t, stored.Series, automatic.Metrics.Series)
			require.Equal(t, stored.Coverage, automatic.Metrics.Coverage)
			final := chapters[len(chapters)-1].Body.(jobdb.JobAttemptOutcomeChapter).Outcome.(jobdb.ApplicationOutputOutcome)
			require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(final.Output.Data))
			wholeOutput := ""
			jobResult, err := api.Inspect(ctx, jobinspect.Request{Operation: "result", JobID: key.JobId, Field: &wholeOutput})
			require.NoError(t, err)
			require.JSONEq(t, string(final.Output.Data), string(jobResult.Value))
			before, err := os.ReadFile(calls)
			require.NoError(t, err)
			require.Equal(t, "call\ncall\ncall\n", string(before))
			require.NoError(t, os.Rename(repo, repo+"-offline"))
			engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			replayed, err := engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			after, err := metricshistory.Read(ctx, rt, key, metrics.Query{Deltas: true})
			require.NoError(t, err)
			require.Equal(t, *r, after, "replay must not create new reports or CPU/timing measurements")
			unchanged, err := os.ReadFile(calls)
			require.NoError(t, err)
			require.Equal(t, before, unchanged)
			require.Equal(t, 1, runs)
		})
	}
}

func TestJobMetricOutputNameIsReserved(t *testing.T) {
	workspaceTestWorker(t)
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "source")
	rt := newTimeoutReproRuntime(t, "toy").ConsumerRuntime
	key := submitGitLifecycle(t, rt, repo, fmt.Sprintf(`id: reserved-metric-output
workspace: {cell: test, repo: %q, ref: main}
sequence:
 - id: probe
   op: workspace_probe
   inputs: {read: identity}
outputs: {c2j_metrics: business-value}
`, celltest.Repo(repo)))
	out, err := runGitLifecycle(t, rt, key)
	require.NoError(t, err)
	require.Equal(t, wf.JobRunFailed, out.Status)
	require.ErrorContains(t, out.JobError, `job output "c2j_metrics" is reserved for task metrics`)
}

// Every result for the requested registered worker must retain its two actual
// phase observations; this also catches wrappers that drop the local context.
func requireInternalWorkerMetrics(t *testing.T, run jobdb.GetJobRunResponse, worker string) {
	t.Helper()
	count := 0
	tasks, err := swfutil.ExecutionTasks(run)
	require.NoError(t, err)
	for _, taskRun := range tasks {
		if taskRun.TaskType != worker {
			continue
		}
		for _, attempt := range taskRun.Attempts {
			require.NotNil(t, attempt.Output)
			var env coretask.OutputEnvelope
			require.NoError(t, json.Unmarshal(attempt.Output.Data, &env))
			require.NotNil(t, env.Metrics, worker)
			require.Equal(t, "valid", env.Metrics.Runtime.Status)
			require.Equal(t, map[string]string{"preparation": "succeeded", "execution": "succeeded"}, env.Metrics.Runtime.Phases)
			phases := map[string]bool{}
			for _, m := range env.Metrics.Runtime.Report.Mutations {
				if m.Name == "c2j_task_duration_seconds" {
					require.Equal(t, worker, m.Labels["task_type"])
					phases[m.Labels["phase"]] = true
				}
			}
			require.Equal(t, map[string]bool{"preparation": true, "execution": true}, phases)
			reduced, err := metrics.Reduce([]metrics.Record{{Task: `/scope:"$job"#1/attempt:1/task:"internal"#1`, Attempt: 1, Ordinal: 1, Channel: "runtime", ChannelReport: env.Metrics.Runtime}}, metrics.Query{}, 1)
			require.NoError(t, err)
			for _, point := range reduced.Snapshot.Series["c2j_task_duration_seconds"] {
				require.Equal(t, int64(1), point.Count, "one observation per entered phase")
			}

			count++
		}
	}
	require.Positive(t, count, "no committed results for %s", worker)
}

func TestAutomaticMetricsForHookAndBootstrapWorkers(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "fixture")
	ws := workspaceTestWorker(t)
	commitHookFile(t, repo, ".c2/hooks/metrics.yaml", `id: metric-hook
version: "1"
input_schema: {}
sequence:
 - id: read
   op: workspace_probe
   inputs: {read: identity}
outputs: {text: '${{ sequence.read.outputs.text }}'}
`)
	ctx, engine, rt, _ := hookTestEngine(t, ws)
	key := startHookTest(t, ctx, engine, repo, `id: metric-parent
version: "1"
input_schema: {}
sequence:
 - id: check
   include: cell://.c2/hooks/metrics.yaml
outputs: {text: '${{ sequence.check.outputs.text }}'}
`)
	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
	require.NoError(t, err)
	for _, worker := range []string{hookPolicyTaskType, hookResolveTaskType, CellContextTaskType} {
		requireInternalWorkerMetrics(t, run, worker)
	}
	before, err := metricshistory.Read(ctx, rt, key, metrics.Query{Deltas: true})
	require.NoError(t, err)
	require.NoError(t, os.Rename(repo, repo+"-offline"))
	_, err = engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	after, err := metricshistory.Read(ctx, rt, key, metrics.Query{Deltas: true})
	require.NoError(t, err)
	require.Equal(t, before, after, "offline replay must not measure internal workers again")
}
