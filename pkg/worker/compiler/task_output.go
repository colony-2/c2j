package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/colony-2/c2j/internal/jsonexact"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type decodedTaskOutput struct {
	Kind     coretask.OutputKind
	Activity workerops.ActivityInvocationOutput
}

func decodeTaskOutput(outputData []byte) (decodedTaskOutput, error) {
	var env coretask.OutputEnvelope
	if err := decodeJSONStrict(outputData, &env); err != nil {
		return decodedTaskOutput{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("decode task output envelope: %w", err), jobdb.ExecutionInfrastructureDurableDecode, "c2j.activity_result", nil, nil)
	}

	if env.Version != 1 && env.Version != coretask.OutputEnvelopeVersion {
		return decodedTaskOutput{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("unsupported task output envelope version: %d", env.Version), jobdb.ExecutionInfrastructureDurableDecode, "c2j.activity_result", nil, nil)
	}

	switch env.Kind {
	case coretask.OutputKindActivityInvocationOutput:
		act, err := decodeActivityInvocationOutput(env.Payload)
		if err != nil {
			return decodedTaskOutput{}, err
		}
		return decodedTaskOutput{Kind: env.Kind, Activity: act}, nil
	default:
		return decodedTaskOutput{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("unsupported task output kind: %q", env.Kind), jobdb.ExecutionInfrastructureDurableDecode, "c2j.activity_result", nil, nil)
	}
}

func decodeActivityInvocationOutput(payload []byte) (workerops.ActivityInvocationOutput, error) {
	var activityOut workerops.ActivityInvocationOutput
	if err := decodeJSONStrict(payload, &activityOut); err == nil {
		activityOut.OpOutput = jsonexact.Native(activityOut.OpOutput).(map[string]any)
		return activityOut, nil
	}

	// Allow the "raw" variant (used by manual completions) as long as the output is an object.
	var raw workerops.ActivityInvocationOutputRaw
	if err := decodeJSONStrict(payload, &raw); err != nil {
		return workerops.ActivityInvocationOutput{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("decode activity output payload: %w", err), jobdb.ExecutionInfrastructureDurableDecode, "c2j.activity_result", nil, nil)
	}
	coerced := map[string]interface{}{}
	if raw.Output != nil {
		m, ok := raw.Output.(map[string]interface{})
		if !ok {
			return workerops.ActivityInvocationOutput{}, fmt.Errorf("decode activity output payload: raw output is not an object (got %T)", raw.Output)
		}
		coerced = jsonexact.Native(m).(map[string]any)
	}
	return workerops.ActivityInvocationOutput{
		WorkspaceBefore:   raw.WorkspaceBefore,
		WorkspaceAfter:    raw.WorkspaceAfter,
		FailureAttrs:      raw.FailureAttrs,
		WorkspaceScopeID:  raw.WorkspaceScopeID,
		Execution:         raw.Execution,
		GitResult:         raw.GitResult,
		NextTask:          raw.NextTask,
		NextTaskAlternate: raw.NextTaskAlternate,
		OpOutput:          coerced,
		ArtifactRefs:      raw.ArtifactRefs,
		Jobs:              raw.Jobs,
	}, nil
}

func decodeJSONStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
