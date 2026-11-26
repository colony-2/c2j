package task

import (
	"encoding/json"
	"fmt"

	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// OutputEnvelope is a versioned wrapper for persisted task outputs.
//
// We store outputs in an envelope so SWF cached chapters can safely carry
// non-task results (e.g. context patches) and workers can branch at replay time.
type OutputEnvelope struct {
	Version int               `json:"v"`
	Kind    OutputKind        `json:"kind"`
	Metrics *metrics.Channels `json:"metrics,omitempty"`
	Payload json.RawMessage   `json:"payload,omitempty"`
	Status  json.RawMessage   `json:"status,omitempty"`
}

type OutputKind string

const (
	OutputEnvelopeVersion = 2
	RecipeStatusTaskType  = "c2j-recipe-status"
)

const (
	OutputKindActivityInvocationOutput OutputKind = "activity_output"
	OutputKindInternalResult           OutputKind = "internal_result"
)

func NewOutputEnvelope(kind OutputKind, payload any) (OutputEnvelope, error) {
	env := OutputEnvelope{
		Version: OutputEnvelopeVersion,
		Kind:    kind,
	}
	if payload == nil {
		return env, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return OutputEnvelope{}, err
	}
	env.Payload = raw
	return env, nil
}

func (e OutputEnvelope) DecodePayload(dst any) error {
	if dst == nil {
		return fmt.Errorf("DecodePayload: dst is nil")
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("DecodePayload: empty payload for kind %q", e.Kind)
	}
	return jsonexact.Unmarshal(e.Payload, dst)
}

// UnwrapInternal is only for known internal-worker result readers. Legacy raw
// results remain valid. It must not be applied to arbitrary business objects.
func UnwrapInternal(raw []byte) (payload []byte, resultErr error) {
	defer func() {
		resultErr = jobdb.ClassifyExecutionInfrastructure(resultErr, jobdb.ExecutionInfrastructureDurableDecode, "c2j.internal_result", nil, nil)
	}()
	var env OutputEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Version == 0 && env.Kind == "" {
		return raw, nil
	}
	if env.Version != 1 && env.Version != OutputEnvelopeVersion {
		return nil, fmt.Errorf("unsupported task output envelope version: %d", env.Version)
	}
	if env.Kind != OutputKindInternalResult {
		return nil, fmt.Errorf("unsupported internal task output kind: %q", env.Kind)
	}
	return env.Payload, nil
}
func DecodeInternal(raw []byte, dst any) error {
	payload, e := UnwrapInternal(raw)
	if e != nil {
		return e
	}
	return jobdb.ClassifyExecutionInfrastructure(jsonexact.Unmarshal(payload, dst), jobdb.ExecutionInfrastructureDurableDecode, "c2j.internal_result", nil, nil)
}
