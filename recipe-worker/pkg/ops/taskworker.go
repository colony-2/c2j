package ops

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/colony-2/c2j/recipe-core/pkg/contextual"
	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	coretask "github.com/colony-2/c2j/recipe-core/pkg/task"
	"github.com/colony-2/swf-go/pkg/swf"
)

type taskWorker struct {
	name string
	reg  ActivityRegistration
	doer *opExecutor
}

func (t *taskWorker) Name() string {
	return t.name
}

func (t *taskWorker) Run(ctx swf.TaskContext, input swf.TaskData) (swf.TaskData, error) {
	d, err := input.GetData()
	if err != nil {
		return nil, err
	}
	air := ActivityInvocationRequest{}
	if err := json.Unmarshal(d, &air); err != nil {
		return nil, err
	}
	inArt, err := input.GetArtifacts()
	if err != nil {
		return nil, err
	}
	out, outArt, err := t.doer.do(context.Background(), &ops.TaskBasedJobTool{TaskContext: ctx}, air, inArt)
	if err != nil {
		td, tdErr := failedTaskData(out, outArt)
		if tdErr != nil {
			return nil, errors.Join(err, tdErr)
		}
		if td != nil {
			return td, err
		}
		return nil, err
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, out)
	if err != nil {
		return nil, err
	}
	return swf.NewTaskData(env, outArt...)
}

var _ swf.TaskWorker = &taskWorker{}

func failedTaskData(output ActivityInvocationOutput, artifacts []swf.Artifact) (swf.TaskData, error) {
	if !hasFailedActivityPayload(output, artifacts) {
		return nil, nil
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, output)
	if err != nil {
		return nil, err
	}
	return swf.NewTaskData(env, artifacts...)
}

func hasFailedActivityPayload(output ActivityInvocationOutput, artifacts []swf.Artifact) bool {
	if len(artifacts) > 0 {
		return true
	}
	if len(output.OpOutput) > 0 || output.NextTask != "" || len(output.ArtifactRefs) > 0 {
		return true
	}
	return output.GitResult != (contextual.GitCommitContext{})
}
