package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobinspect"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// This proves the runtime mechanism, not resolver routing, model/checkpoint
// normalization, candidate conservation or actual child behavior. Historical
// resolver product expectations remain unchanged. No retained-response YAML replay.
const activeAnswerRecipe = `id: active-answer
version: "1"
input_schema: {}
state:
 initial: discuss
 states:
  discuss:
   sequence:
    - id: question
      op: input
      inputs:
       form:
        kind: review
        title: Ask a question
        fields:
         - {id: question, type: paragraph_text, question: Question, required: true}
    - id: answer
      op: active_answer
      inputs: {value: '${{ sequence.question.outputs.fields.question }}'}
    - id: review
      op: input
      inputs:
       form:
        kind: review
        title: '${{ sequence.answer.outputs.value }}'
        fields:
         - id: action
           type: multiple_choice
           question: Proceed?
           required: true
           options: [{value: continue, label: Continue}]
    - id: continue
      op: active_continue
      inputs: {value: '${{ sequence.review.outputs.fields.action }}', receipt: '${{ sequence.review.outputs.receipt }}'}
   catch:
    - when: failure.kind != 'cancellation'
      to: recover
  recover:
   sequence:
    - id: recovery
      op: active_recovery
      inputs: {value: '${{ string(transition.failure) }}'}
    - id: review
      op: input
      inputs:
       form: {kind: review, question: Recovery, type: paragraph_text}
outputs: {}
`

type activeValue struct {
	Value   string         `json:"value"`
	Receipt map[string]any `json:"receipt,omitempty"`
}
type activeCalls struct {
	mu    sync.Mutex
	calls []string
}

func (h *activeCalls) record(s string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, s)
}
func (h *activeCalls) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// Observe attempts before native dispatch, including review creation. Forward the
// local lease-bound context as well as native TaskContext; do not replace input.
type activeTaskObserver struct {
	jobworkflow.TaskWorker
	h *activeCalls
}

func (w activeTaskObserver) Run(tc jobworkflow.TaskContext, d jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, d)
}
func (w activeTaskObserver) RunTask(ctx context.Context, tc jobworkflow.TaskContext, d jobdb.TaskData) (jobdb.TaskData, error) {
	if w.Name() == "input:generate_form" {
		w.h.record("review")
	}
	if strings.HasPrefix(w.Name(), "active_") {
		w.h.record(w.Name())
		w.h.record(fmt.Sprintf("identity job=%s task=%s step=%d", tc.JobKey.JobId, tc.InvocationID, tc.Step))
	}
	if r, ok := w.TaskWorker.(interface {
		RunTask(context.Context, jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
	}); ok {
		return r.RunTask(ctx, tc, d)
	}
	return w.TaskWorker.Run(tc, d)
}

// Preserve the native renewable lease contract through observation. Renewal is
// owned by the worker; this wrapper never injects cancellation or renews itself.
type activeRuntime struct {
	jobdb.WorkflowRuntime
	t *testing.T
}

func (r *activeRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	if err != nil {
		return nil, err
	}
	native, ok := l.(jobdb.RenewableExecutionLease)
	if !ok {
		return nil, fmt.Errorf("SQLite lease is not renewable")
	}
	r.t.Logf("LEASE job=%s id=%s worker=%s expiry=%s", l.Job().JobKey.JobId, l.LeaseID(), native.LeaseWorkerID(), native.LeaseExpiry())
	return &activeLease{native, r.t}, nil
}
func (r *activeRuntime) CreateDelegation(ctx context.Context, parent jobdb.ExecutionLease, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	if l, ok := parent.(*activeLease); ok {
		parent = l.RenewableExecutionLease
	}
	return r.WorkflowRuntime.(jobdb.LeaseDelegator).CreateDelegation(ctx, parent, req)
}

type activeLease struct {
	jobdb.RenewableExecutionLease
	t *testing.T
}

func (l *activeLease) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	next, err := l.RenewableExecutionLease.Renew(ctx)
	l.t.Logf("RENEW job=%s id=%s err=%v", l.Job().JobKey.JobId, l.LeaseID(), err)
	if err != nil {
		return nil, err
	}
	return &activeLease{next, l.t}, nil
}
func (l *activeLease) LeaseToken() string {
	if native, ok := l.RenewableExecutionLease.(interface{ LeaseToken() string }); ok {
		return native.LeaseToken()
	}
	return ""
}

// Native task outputs retain receipts, allowing byte-for-byte comparisons after
// cancellation, independently of the attempted-call ledger.
func activeStored(t *testing.T, rt jobdb.WorkflowRuntime, key jobdb.JobKey) map[string]string {
	t.Helper()
	run, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true, IncludeArtifacts: true})
	require.NoError(t, err)
	result := map[string]string{}
	for _, task := range executionTaskRuns(run) {
		for _, a := range task.Attempts {
			if a.Output != nil {
				b, e := json.Marshal(a.Output)
				require.NoError(t, e)
				result[fmt.Sprintf("%s:%d", task.TaskType, a.Ordinal)] = string(b)
			}
		}
	}
	return result
}

func TestActiveAnswerCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "healthy", "application-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cleanup := context.WithCancel(celltest.Context(context.Background()))
			defer cleanup()
			h := &activeCalls{}
			cause := make(chan error, 1)
			entered, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			const question = "Why is continuation safe?"
			const answer = "The answer is ready for review."
			answerOp := coreops.NewActivityMappedOpV2[activeValue, activeValue](coreops.OpMetadata{Type: "active_answer"}, func(_ coreops.OpDependencies, local context.Context, in activeValue) (activeValue, error) {
				defer close(exited)
				if in.Value != question {
					return activeValue{}, fmt.Errorf("question changed: %q", in.Value)
				}
				h.record("entry")
				close(entered)
				select {
				case <-local.Done():
					cause <- context.Cause(local)
					h.record(fmt.Sprintf("cancel cause=%v", context.Cause(local)))
					return activeValue{}, local.Err()
				case <-release:
					if mode == "application-error" {
						return activeValue{}, fmt.Errorf("ordinary application failure")
					}
					return activeValue{Value: answer}, nil
				}
			})
			var continueReceipt *input.Receipt
			probe := func(name string) coreops.RegisterableOp {
				return coreops.NewActivityMappedOpV2[activeValue, activeValue](coreops.OpMetadata{Type: name}, func(_ coreops.OpDependencies, _ context.Context, in activeValue) (activeValue, error) {
					h.record(name)
					t.Logf("PROBE %s input=%+v", name, in)
					if name == "active_recovery" {
						if !strings.Contains(in.Value, "ordinary application failure") || !strings.Contains(in.Value, `"kind":"task_error"`) {
							return in, fmt.Errorf("unexpected recovery: %s", in.Value)
						}
					}
					if name == "active_continue" {
						raw, err := json.Marshal(continueReceipt)
						if err != nil {
							return in, err
						}
						var expected map[string]any
						if err := json.Unmarshal(raw, &expected); err != nil {
							return in, err
						}
						if in.Receipt == nil || continueReceipt == nil || !reflect.DeepEqual(in.Receipt, expected) {
							return in, fmt.Errorf("continuation receipt changed: got %+v want %+v", in.Receipt, continueReceipt)
						}
					}
					if name == "active_continue" && in.Value != "continue" {
						return in, fmt.Errorf("wrong continuation: %q", in.Value)
					}
					return in, nil
				})
			}
			workspaceTestWorker(t, input.GetOp(), answerOp, probe("active_continue"), probe("active_recovery"))
			repo := t.TempDir()
			workspaceRepo(t, repo, "source")
			rt := &activeRuntime{newTimeoutReproRuntime(t, "sqlite").WorkflowRuntime, t}
			key := submitGitLifecycle(t, rt, repo, activeAnswerRecipe)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			ctl := &workerworkflow.SWFWorkflowControl{Engine: engine}
			reviews, err := input.NewRuntime(ctl, nil)
			require.NoError(t, err)
			run := func() (jobworkflow.JobRunOutcome, error) {
				reg, e := workerops.NewActivityRegistry()
				if e != nil {
					return jobworkflow.JobRunOutcome{}, e
				}
				ws, e := NewRecipeWorker(coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build(), reg)
				if e != nil {
					return jobworkflow.JobRunOutcome{}, e
				}
				guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
				guard.Issuer = permissions.JobDBIssuer{}
				tasks := []jobworkflow.TaskWorker{}
				for _, tw := range ws.TaskWorkers {
					tasks = append(tasks, guard.WrapTaskWorker(activeTaskObserver{tw, h}))
				}
				r, e := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "active-answer", LeaseDuration: time.Second})
				if e != nil {
					return jobworkflow.JobRunOutcome{}, e
				}
				return r.Run(nil)
			}
			out, err := run()
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status, "%v", out.JobError)
			form, err := reviews.GetForm(ctx, key.TenantId, key.JobId)
			require.NoError(t, err)
			require.NotEmpty(t, form.RequestID)
			require.Equal(t, "review", form.Kind)
			first, err := reviews.SubmitFormResponse(ctx, key.TenantId, key.JobId, input.FormSubmission{RequestID: form.RequestID, SubmissionID: "question", Fields: map[string]any{"question": question}}, input.Actor{ID: "test-human", Kind: "human"})
			require.NoError(t, err)
			require.NotNil(t, first.Receipt)
			t.Logf("FIRST job=%s request=%s receipt=%+v", key.JobId, form.RequestID, first.Receipt)
			type result struct {
				out jobworkflow.JobRunOutcome
				err error
			}
			done := make(chan result, 1)
			go func() { o, e := run(); done <- result{o, e} }()
			// Even on assertion failure, join the worker before the disposable DB closes.
			joined := false
			defer func() {
				if !joined {
					cleanup()
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("cleanup worker join timed out")
					}
				}
			}()
			select {
			case <-entered:
			case r := <-done:
				joined = true
				t.Fatalf("stopped before entry: %+v calls=%v stored=%v", r, h.snapshot(), activeStored(t, rt, key))
			case <-time.After(30 * time.Second):
				t.Fatal("entry timeout")
			}
			baseline := h.snapshot()
			stored := activeStored(t, rt, key)
			require.NotEmpty(t, stored)
			require.Len(t, stored, 2, "only initial form and accepted question receipt")
			require.Equal(t, "review", baseline[0])
			require.Len(t, baseline, 4, "one review, answer dispatch, identity and entry")
			require.Equal(t, "active_answer:active_answer", baseline[1])
			require.Equal(t, "entry", baseline[3])
			t.Logf("BASELINE attempts=%v stored=%v", baseline, stored)
			if mode == "cancel" {
				control := celltest.Context(context.Background())
				require.NoError(t, control.Err())
				h.record("CancelJob")
				require.NoError(t, rt.CancelJob(control, jobdb.CancelJobRequest{JobKey: key, Reason: "Cancel active answer"}))
				require.NoError(t, control.Err())
			} else {
				close(release)
			}
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Fatal("operation did not exit")
			}
			var got result
			select {
			case got = <-done:
				joined = true
			case <-time.After(10 * time.Second):
				t.Fatal("worker did not join")
			}
			require.NoError(t, ctx.Err())
			t.Logf("JOIN status=%s err=%v calls=%v", got.out.Status, got.err, h.snapshot())
			if mode == "cancel" {
				if got.err != nil {
					require.True(t, errors.Is(got.err, jobdb.ErrExecutionLeaseLost), "%v", got.err)
				}
				calls := h.snapshot()
				require.Len(t, calls, len(baseline)+2)
				require.Equal(t, "CancelJob", calls[len(baseline)])
				require.Contains(t, calls[len(baseline)+1], "cancel cause=")
				require.ErrorIs(t, <-cause, jobdb.ErrExecutionLeaseLost)
				require.Equal(t, stored, activeStored(t, rt, key), "no additional outputs; original accepted receipt unchanged")
				inspection, e := ctl.InspectJob(ctx, key)
				require.NoError(t, e)
				require.Equal(t, "cancellation", inspection.FailureKind)
				require.Nil(t, inspection.Output)
				require.Nil(t, got.out.Output)
				shown, e := (&jobinspect.Service{Runtime: rt, Tenant: key.TenantId}).Inspect(ctx, jobinspect.Request{Operation: "show", JobID: key.JobId})
				require.NoError(t, e)
				require.Equal(t, "CANCELLED", shown.Job.Status)
				require.Equal(t, "cancelled", shown.Job.CompletionStatus)
				t.Logf("CANCELLED status=%s completion=%s failure=%s recovery/review/continuation delta attempted=0/0/0 persisted=0/0/0 receipt unchanged", shown.Job.Status, shown.Job.CompletionStatus, inspection.FailureKind)
			} else {
				require.NoError(t, got.err)
				require.Equal(t, jobworkflow.JobRunSuspended, got.out.Status, "%v", got.out.JobError)
				second, e := reviews.GetForm(ctx, key.TenantId, key.JobId)
				require.NoError(t, e)
				require.NotEqual(t, form.RequestID, second.RequestID)
				require.Equal(t, "review", second.Kind)
				t.Logf("SECOND job=%s request=%s form=%+v stored=%v", key.JobId, second.RequestID, second, activeStored(t, rt, key))
				if mode == "healthy" {
					require.Equal(t, answer, second.Title)
					receipt, e := reviews.SubmitFormResponse(ctx, key.TenantId, key.JobId, input.FormSubmission{RequestID: second.RequestID, SubmissionID: "continue", Fields: map[string]any{"action": "continue"}}, input.Actor{ID: "test-human", Kind: "human"})
					require.NoError(t, e)
					require.NotNil(t, receipt.Receipt)
					require.NotEqual(t, first.Receipt, receipt.Receipt)
					continueReceipt = receipt.Receipt
					out, e = run()
					require.NoError(t, e)
					gitLifecycleOutput(t, out)
					t.Logf("CONTINUE receipt=%+v", receipt.Receipt)
				} else {
					require.Equal(t, "Recovery", second.Question)
				}
				calls := h.snapshot()
				count := func(s string) int {
					n := 0
					for _, c := range calls {
						if c == s {
							n++
						}
					}
					return n
				}
				require.Equal(t, 2, count("review"))
				recovery, continuation := 0, 1
				if mode == "application-error" {
					recovery, continuation = 1, 0
				}
				require.Equal(t, recovery, count("active_recovery:active_recovery"))
				require.Equal(t, continuation, count("active_continue:active_continue"))
				require.Equal(t, recovery, count("active_recovery"))
				require.Equal(t, continuation, count("active_continue"))
				persisted := activeStored(t, rt, key)
				for name, want := range map[string]int{"active_recovery:": recovery, "active_continue:": continuation, "input:generate_form:": 2} {
					n := 0
					for k := range persisted {
						if strings.HasPrefix(k, name) {
							n++
						}
					}
					require.Equal(t, want, n, "stored %s", name)
				}
				t.Logf("FINAL calls=%v recovery=%d continuation=%d stored=%v", calls, recovery, continuation, persisted)
			}
		})
	}
}
