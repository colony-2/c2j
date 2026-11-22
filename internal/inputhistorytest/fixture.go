// Package inputhistorytest supplies real retained records for bridge acceptance.
// Native input package tests retain their independent fixtures.
package inputhistorytest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/input"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

const Tenant = "history-tests"

type Fixture struct {
	Runtime              jobdb.ConsumerRuntime
	Control              *workerworkflow.SWFWorkflowControl
	Input                *input.Runtime
	Job                  jobdb.JobKey
	Forms                []input.InputForm
	Source, Agent, Empty jobdb.ArtifactKey
	Binary, Text         []byte
	worker               wf.ExecutionWorker
}
type task struct {
	name string
	run  func(wf.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
}

func (t task) Name() string { return t.name }
func (t task) Run(c wf.TaskContext, d jobdb.TaskData) (jobdb.TaskData, error) {
	if t.run != nil {
		return t.run(c, d)
	}
	return d, nil
}
func Build(ctx context.Context, rt jobdb.ConsumerRuntime) (*Fixture, error) {
	if _, e := celltest.Register(ctx, rt, Tenant, "history", "example.test/history"); e != nil {
		return nil, e
	}
	f := &Fixture{Runtime: rt, Job: jobdb.JobKey{TenantId: Tenant, JobId: "history-job"}, Binary: []byte{0, 255, 128, 10, 0, 1}, Text: []byte("first\né雪🙂\n")}
	source := jobdb.NewArtifactFromBytes("same.bin", f.Binary)
	empty := jobdb.NewArtifactFromBytes("empty.bin", nil)
	_, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: Tenant, JobID: "source-job", JobType: "source", Cell: "history", CellRepo: "example.test/history", Data: jobdb.NewTaskDataOrPanic(nil, source, empty)}})
	if e != nil {
		return nil, e
	}
	sourceRun, e := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: Tenant, JobId: "source-job"}, IncludeInputs: true, IncludeArtifacts: true})
	if e != nil {
		return nil, e
	}
	for _, a := range sourceRun.Start.Input.Artifacts {
		if a.Name == "empty.bin" && a.Key != nil {
			f.Empty = *a.Key
		}
		if a.Name == "same.bin" && a.Key != nil {
			f.Source = *a.Key
		}
	}
	if f.Source.JobId == "" {
		return nil, fmt.Errorf("source artifact not retained")
	}

	agent := jobdb.NewArtifactFromBytes("same.bin", f.Text)
	env, e := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"output": map[string]any{"answer": "produced"}})
	if e != nil {
		return nil, e
	}
	producer := task{name: "agent:invoke", run: func(wf.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) { return jobdb.NewTaskData(env, agent) }}
	aw := wf.ExecutionWorker{JobType: "agent", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		return c.DoTask(wf.TaskCall{Site: "agent", TaskType: producer.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskDataOrPanic(nil), nil })
	}}
	h, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: Tenant, JobID: "agent-job", JobType: "agent", Cell: "history", CellRepo: "example.test/history", Data: jobdb.NewTaskDataOrPanic(nil)}})
	if e != nil {
		return nil, e
	}
	runnable, e := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: aw, TaskWorkers: []wf.TaskWorker{producer}, WorkerID: "history", LeaseDuration: time.Minute})
	if e != nil {
		return nil, e
	}
	if _, e = runnable.Run(nil); e != nil {
		return nil, e
	}
	run, e := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: h.JobKey, IncludeOutputs: true, IncludeArtifacts: true})
	if e != nil {
		return nil, e
	}
	for _, rec := range run.Execution {
		if rec.Record.Output != nil {
			for _, a := range rec.Record.Output.Artifacts {
				if a.Name == "same.bin" && a.Key != nil {
					f.Agent = *a.Key
				}
			}
		}
	}
	if f.Agent.JobId == "" {
		return nil, fmt.Errorf("agent result artifact not retained: %+v", run.Execution)
	}
	engine, e := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	if e != nil {
		return nil, e
	}
	f.Control = &workerworkflow.SWFWorkflowControl{Engine: engine}
	f.Input, e = input.NewRuntime(f.Control, nil)
	if e != nil {
		return nil, e
	}
	ref := artifacts.NewStoredRef(f.Source)
	agentRef := artifacts.NewStoredRef(f.Agent)
	f.Forms = []input.InputForm{
		{RequestID: "request-1", RequestedAt: "2026-11-20T00:00:00Z", Title: "Evidence", Request: map[string]any{"large": json.Number("9007199254740993"), "null": nil, "previous": map[string]any{"source": ref}, "carried": map[string]any{"Outputs": map[string]any{"report": artifacts.Ref{Kind: artifacts.RefKindAttached, Name: "same.bin"}}, "Artifacts": map[string]artifacts.Ref{"same.bin": agentRef}}, "reported_role": "agent"}, Documents: map[string]artifacts.Ref{"source": ref, "agent": agentRef, "empty": artifacts.NewStoredRef(f.Empty)}, ResponseSchema: map[string]any{}},
		{RequestID: "request-2", Title: "Second", Request: map[string]any{"message": "next"}, ResponseSchema: map[string]any{}},
		{RequestID: "request-3", Title: "Private", Request: map[string]any{"escaped": strings.Repeat("<", 65536)}, ResponseSchema: map[string]any{}},
	}
	f.worker = wf.ExecutionWorker{JobType: "history", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		var out jobdb.TaskData
		for i, form := range f.Forms {
			raw, e := json.Marshal(form)
			if e != nil {
				return nil, e
			}
			out, e = c.DoTask(wf.TaskCall{Site: fmt.Sprintf("input-%d", i), TaskType: "input:collect_user_input", ContractVersion: 1, Command: raw}, func(context.Context) (jobdb.TaskData, error) {
				var value map[string]any
				d := json.NewDecoder(strings.NewReader(string(raw)))
				d.UseNumber()
				if e := d.Decode(&value); e != nil {
					return nil, e
				}
				return workerops.NewInvocationTaskData(workerops.ActivityInvocationRequest{Input: value})
			})
			if e != nil {
				return nil, e
			}
		}
		return out, nil
	}}
	_, e = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: Tenant, JobID: f.Job.JobId, JobType: "history", Cell: "history", CellRepo: "example.test/history", Data: jobdb.NewTaskDataOrPanic(nil)}})
	if e != nil {
		return nil, e
	}
	return f, f.Run(ctx)
}
func (f *Fixture) Run(ctx context.Context) error {
	r, e := wf.GetJobForRun(ctx, f.Runtime, wf.GetJobForRunRequest{JobKey: f.Job, JobWorker: f.worker, TaskWorkers: []wf.TaskWorker{task{name: "prepare"}}, WorkerID: "history", LeaseDuration: time.Minute})
	if e != nil {
		return e
	}
	_, e = r.Run(nil)
	return e
}
func (f *Fixture) Advance(ctx context.Context, index int) error {
	response := any(json.Number("9007199254740993"))
	if index == 0 {
		response = map[string]any{"upload": jobdb.NewArtifactFromBytes("same.bin", []byte("accepted upload")), "large": json.Number("9007199254740993")}
	}
	_, e := f.Input.SubmitStructuredResponse(ctx, Tenant, f.Job.JobId, input.StructuredSubmission{RequestID: f.Forms[index].RequestID, SubmissionID: fmt.Sprintf("submission-%d", index), Response: response}, input.Actor{ID: "authenticated-human", Kind: "human"})
	if e != nil {
		return e
	}
	return f.Run(ctx)
}
