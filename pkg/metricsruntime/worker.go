package metricsruntime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

type worker struct {
	wf.TaskWorker
	kind task.OutputKind
}

func (w *worker) MetricOutputKind() task.OutputKind { return w.kind }
func (w *worker) Run(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w *worker) RunTask(ctx context.Context, tc wf.TaskContext, in jobdb.TaskData) (out jobdb.TaskData, err error) {
	if Current(ctx) != nil {
		return task.RunLocal(w.TaskWorker, ctx, tc, in)
	}
	if _, ok := w.TaskWorker.(interface{ MetricAdmission() }); ok {
		return task.RunLocal(w.TaskWorker, WithOutputKind(ctx, w.kind), tc, in)
	}
	redactor := secrets.RedactorFromContext(ctx)
	if redactor == nil {
		redactor = secrets.NewRedactor()
		defer redactor.Close()
		ctx = secrets.WithRedactor(ctx, redactor)
	}
	retained := redactor.NewSession()
	defer retained.Close()
	ctx, t := Begin(ctx, w.Name(), redactor.CheckOutput)
	defer func() { out, err = FinishTask(t, w.kind, out, err) }()
	return task.RunLocal(w.TaskWorker, ctx, tc, in)
}

// WrapWorker marks known c2j workers; the admission wrapper starts timing before
// its common setup. Standalone embedded wiring also works through RunTask.
func WrapWorker(w wf.TaskWorker, kind task.OutputKind) wf.TaskWorker { return &worker{w, kind} }
func Kind(w wf.TaskWorker) task.OutputKind {
	if v, ok := w.(interface{ MetricOutputKind() task.OutputKind }); ok {
		return v.MetricOutputKind()
	}
	return ""
}
func FinishTask(t *Timing, kind task.OutputKind, out jobdb.TaskData, err error) (jobdb.TaskData, error) {
	if extensionwait.IsDiscard(err) {
		t.Discard()
		return nil, err
	}
	channels := t.Finish(err)
	if execution.IsRequired(err) || wf.IsExecutionSuspended(err) {
		return out, err
	}
	var env task.OutputEnvelope
	var artifacts []jobdb.Artifact
	if out != nil {
		raw, e := out.GetData()
		if e != nil {
			return out, errors.Join(err, e)
		}
		artifacts, e = out.GetArtifacts()
		if e != nil {
			return out, errors.Join(err, e)
		}
		if kind == task.OutputKindActivityInvocationOutput {
			if len(raw) > 0 && string(raw) != "null" {
				if e = json.Unmarshal(raw, &env); e != nil {
					return out, errors.Join(err, e)
				}
			}
		} else {
			env.Payload = raw
		}
	}
	env.Version = task.OutputEnvelopeVersion
	env.Kind = kind
	env.Metrics = &channels
	result, e := jobdb.NewTaskData(env, artifacts...)
	return result, errors.Join(err, e)
}

type outputKindKey struct{}

func WithOutputKind(ctx context.Context, kind task.OutputKind) context.Context {
	return context.WithValue(ctx, outputKindKey{}, kind)
}
func OutputKindFromContext(ctx context.Context) task.OutputKind {
	kind, _ := ctx.Value(outputKindKey{}).(task.OutputKind)
	return kind
}
