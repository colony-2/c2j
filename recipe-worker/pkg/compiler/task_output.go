package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"

	coretask "github.com/colony-2/c2j/recipe-core/pkg/task"
	workerops "github.com/colony-2/c2j/recipe-worker/pkg/ops"
)

type decodedTaskOutput struct {
	Kind     coretask.OutputKind
	Activity workerops.ActivityInvocationOutput
	Patch    coretask.ContextPatch
}

func decodeTaskOutput(outputData []byte) (decodedTaskOutput, error) {
	var env coretask.OutputEnvelope
	if err := decodeJSONStrict(outputData, &env); err != nil {
		return decodedTaskOutput{}, fmt.Errorf("decode task output envelope: %w", err)
	}

	if env.Version != coretask.OutputEnvelopeVersion {
		return decodedTaskOutput{}, fmt.Errorf("unsupported task output envelope version: %d", env.Version)
	}

	switch env.Kind {
	case coretask.OutputKindActivityInvocationOutput:
		act, err := decodeActivityInvocationOutput(env.Payload)
		if err != nil {
			return decodedTaskOutput{}, err
		}
		return decodedTaskOutput{Kind: env.Kind, Activity: act}, nil
	case coretask.OutputKindContextPatch:
		var patch coretask.ContextPatch
		if len(env.Payload) > 0 {
			if err := decodeJSONStrict(env.Payload, &patch); err != nil {
				return decodedTaskOutput{}, fmt.Errorf("decode context patch payload: %w", err)
			}
		}
		return decodedTaskOutput{Kind: env.Kind, Patch: patch}, nil
	default:
		return decodedTaskOutput{}, fmt.Errorf("unsupported task output kind: %q", env.Kind)
	}
}

func decodeActivityInvocationOutput(payload []byte) (workerops.ActivityInvocationOutput, error) {
	var activityOut workerops.ActivityInvocationOutput
	if err := decodeJSONStrict(payload, &activityOut); err == nil {
		return activityOut, nil
	}

	// Allow the "raw" variant (used by manual completions) as long as the output is an object.
	var raw workerops.ActivityInvocationOutputRaw
	if err := decodeJSONStrict(payload, &raw); err != nil {
		return workerops.ActivityInvocationOutput{}, fmt.Errorf("decode activity output payload: %w", err)
	}
	coerced := map[string]interface{}{}
	if raw.Output != nil {
		m, ok := raw.Output.(map[string]interface{})
		if !ok {
			return workerops.ActivityInvocationOutput{}, fmt.Errorf("decode activity output payload: raw output is not an object (got %T)", raw.Output)
		}
		coerced = m
	}
	return workerops.ActivityInvocationOutput{
		GitResult:    raw.GitResult,
		NextTask:     raw.NextTask,
		OpOutput:     coerced,
		ArtifactRefs: raw.ArtifactRefs,
	}, nil
}

func decodeJSONStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
