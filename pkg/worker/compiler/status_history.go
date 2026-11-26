package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

const recipeStatusLimit = 1024

type recipeStatusHistory struct {
	current  json.RawMessage
	readOnly bool
	logger   *slog.Logger
}

type recipeStatusOutcome struct {
	Rejection string `json:"rejection,omitempty"`
}

func (history *recipeStatusHistory) record(exec jobworkflow.ExecutionContext, site string, status workflow.CompletionStatus) error {
	if status.Declaration == nil && history.current == nil {
		return nil
	}
	live := false
	result, err := exec.DoTask(jobworkflow.TaskCall{Site: "status:" + site, TaskType: task.RecipeStatusTaskType, ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(ctx context.Context) (jobdb.TaskData, error) {
		live = !history.readOnly
		value := history.current
		var outcome recipeStatusOutcome
		if live && status.Declaration != nil {
			rendered, err := status.Resolver.ResolveValueWithContext(ctx, status.Declaration.Value, template.ModeInterpolation)
			if err == nil {
				var encoded []byte
				encoded, err = json.Marshal(rendered)
				if err == nil {
					if len(encoded) <= recipeStatusLimit {
						value = encoded
					} else {
						outcome.Rejection = "exceeds 1024 JSON bytes"
					}
				}
			}
			if err != nil {
				outcome.Rejection = "invalid status value"
			}
		}
		envelope, err := task.NewOutputEnvelope(task.OutputKindInternalResult, outcome)
		if err != nil {
			return nil, err
		}
		envelope.Status = value
		return jobdb.NewTaskData(envelope)
	})
	if err != nil {
		return &executionControlError{err}
	}
	value, outcome, err := decodeRecipeStatus(result)
	if err != nil {
		return jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDurableDecode, task.RecipeStatusTaskType, nil, nil)
	}
	history.current = value
	if live && outcome.Rejection != "" {
		history.logger.Warn("Recipe status was not updated", "reason", outcome.Rejection)
	}
	return nil
}

func decodeRecipeStatus(result jobdb.TaskData) (json.RawMessage, recipeStatusOutcome, error) {
	var outcome recipeStatusOutcome
	raw, err := result.GetData()
	if err != nil {
		return nil, outcome, err
	}
	var envelope task.OutputEnvelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return nil, outcome, err
	}
	if envelope.Version != task.OutputEnvelopeVersion || envelope.Kind != task.OutputKindInternalResult || len(envelope.Status) > recipeStatusLimit {
		return nil, outcome, fmt.Errorf("invalid recipe status envelope")
	}
	if err = envelope.DecodePayload(&outcome); err != nil {
		return nil, outcome, err
	}
	return envelope.Status, outcome, nil
}

type recipeStatusWorker struct{}

func (recipeStatusWorker) Name() string { return task.RecipeStatusTaskType }
func (recipeStatusWorker) Run(_ jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	if _, _, err := decodeRecipeStatus(input); err != nil {
		return nil, jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDurableDecode, task.RecipeStatusTaskType, nil, nil)
	}
	return input, nil
}
