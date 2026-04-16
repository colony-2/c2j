package task

import (
	"encoding/json"
	"fmt"
)

// OutputEnvelope is a versioned wrapper for persisted task outputs.
//
// We store outputs in an envelope so SWF cached chapters can safely carry
// non-task results (e.g. context patches) and workers can branch at replay time.
type OutputEnvelope struct {
	Version int             `json:"v"`
	Kind    OutputKind      `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type OutputKind string

const (
	OutputEnvelopeVersion = 1
)

const (
	OutputKindActivityInvocationOutput OutputKind = "activity_output"
	OutputKindContextPatch             OutputKind = "context_patch"
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
	return json.Unmarshal(e.Payload, dst)
}
