package joblabel

import (
	"context"
	"encoding/json"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/clientpayload"
)

type Edit struct {
	Action          string   `json:"action"`
	Key             string   `json:"key"`
	ExpectedVersion *Version `json:"expected_version"`
	Value           *string  `json:"value,omitempty"`
	Color           *Color   `json:"color,omitempty"`
}

func (edit *Edit) UnmarshalJSON(raw []byte) error {
	if clientpayload.ValidateJSONWithLimits(raw, 8<<10, 1) != nil {
		return ErrInvalidEdit
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrInvalidEdit
	}
	for field, value := range fields {
		switch field {
		case "action", "key", "expected_version", "value", "color":
			if len(value) == 0 || value[0] != '"' {
				return ErrInvalidEdit
			}
		default:
			return ErrInvalidEdit
		}
	}
	type plain Edit
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*edit = Edit(decoded)
	return edit.Validate()
}

func (edit Edit) Validate() error {
	if err := ValidateKey(edit.Key); err != nil {
		return err
	}
	if edit.ExpectedVersion == nil || *edit.ExpectedVersion < 0 {
		return ErrInvalidVersion
	}
	switch edit.Action {
	case "set":
		if edit.Value == nil || edit.Color == nil {
			return ErrInvalidEdit
		}
		return (storedLabel{Value: *edit.Value, Color: *edit.Color}).validate()
	case "remove":
		if edit.Value == nil && edit.Color == nil {
			return nil
		}
	}
	return ErrInvalidEdit
}

func (edit Edit) apply(ctx context.Context, write func(context.Context, jobdb.JobMetadataEdit) (jobdb.JobMetadataEntry, error)) (State, error) {
	if err := edit.Validate(); err != nil {
		return State{}, err
	}
	request := jobdb.JobMetadataEdit{Key: prefix + edit.Key, ExpectedVersion: edit.ExpectedVersion, Remove: edit.Action == "remove"}
	if !request.Remove {
		request.Set, _ = json.Marshal(storedLabel{Value: *edit.Value, Color: *edit.Color})
	}
	receipt, err := write(ctx, request)
	if err != nil {
		return State{}, err
	}
	if !receipt.Present && receipt.Version == 0 {
		return State{Status: Absent, Version: &receipt.Version}, nil
	}
	return decode(edit.Key, receipt), nil
}

func (edit Edit) applyRuntime(ctx context.Context, runtime jobdb.ConsumerRuntime, job jobdb.JobKey) (State, error) {
	return edit.apply(ctx, func(ctx context.Context, request jobdb.JobMetadataEdit) (jobdb.JobMetadataEntry, error) {
		port, ok := runtime.(jobdb.JobMetadataRuntime)
		if !ok {
			return jobdb.JobMetadataEntry{}, jobdb.ErrJobMetadataUnsupported
		}
		return port.EditJobMetadata(ctx, job, request)
	})
}

func SetJobLabel(ctx context.Context, runtime jobdb.ConsumerRuntime, job jobdb.JobKey, key, value string, color Color, expected Version) (State, error) {
	return (Edit{Action: "set", Key: key, ExpectedVersion: &expected, Value: &value, Color: &color}).applyRuntime(ctx, runtime, job)
}

func RemoveJobLabel(ctx context.Context, runtime jobdb.ConsumerRuntime, job jobdb.JobKey, key string, expected Version) (State, error) {
	return (Edit{Action: "remove", Key: key, ExpectedVersion: &expected}).applyRuntime(ctx, runtime, job)
}

func EditWithLease(ctx context.Context, lease jobdb.JobMetadataLease, edit Edit) (State, error) {
	if lease == nil {
		return State{}, jobdb.ErrJobMetadataUnsupported
	}
	return edit.apply(ctx, lease.EditJobMetadata)
}
