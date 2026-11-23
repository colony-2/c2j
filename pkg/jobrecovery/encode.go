package jobrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// These encoders implement only the original create/restart wire contracts.
// Keep nil selectors and raw application JSON unchanged; never prepare a schema.
func encodeSubmit(ctx context.Context, j jobdb.SubmitJob) (any, error) {
	if err := jobdb.ValidateIdentifier(j.JobType); err != nil {
		return nil, err
	}
	data, err := taskData(ctx, j.Data)
	if err != nil {
		return nil, err
	}
	job := map[string]any{"cell": j.Cell, "jobType": j.JobType, "data": data}
	if j.CellRepo != "" {
		job["cellRepo"] = j.CellRepo
	}
	if len(j.Dependencies) > 0 {
		job["dependencies"] = j.Dependencies
	}
	if j.ClientPayloadUpdate != nil {
		job["clientPayloadUpdate"] = j.ClientPayloadUpdate
	}
	if j.AvailableAt != nil {
		job["availableAt"] = j.AvailableAt
	}
	if len(j.Prerequisites) > 0 {
		job["prerequisites"] = prerequisites(j.Prerequisites)
	}
	if j.Schema != nil {
		job["schema"] = schema(j.Schema)
	}
	policy := map[string]any{}
	retry := map[string]any{}
	r := j.RunPolicy.Retry
	if r.InitialInterval != 0 {
		retry["initialInterval"] = r.InitialInterval.String()
	}
	if r.MaximumInterval != 0 {
		retry["maximumInterval"] = r.MaximumInterval.String()
	}
	if r.BackoffCoefficient != 0 {
		retry["backoffCoefficient"] = r.BackoffCoefficient
	}
	if r.MaximumAttempts != 0 {
		retry["maximumAttempts"] = r.MaximumAttempts
	}
	if len(r.NonRetryableErrorTypes) > 0 {
		retry["nonRetryableErrorTypes"] = r.NonRetryableErrorTypes
	}
	if len(retry) > 0 {
		policy["retry"] = retry
	}
	if j.RunPolicy.InvocationTimeout != nil {
		policy["invocationTimeout"] = j.RunPolicy.InvocationTimeout.String()
	}
	if j.RunPolicy.TotalTimeout != nil {
		policy["totalTimeout"] = j.RunPolicy.TotalTimeout.String()
	}
	if len(policy) > 0 {
		job["runPolicy"] = policy
	}
	if len(j.Metadata) > 0 && !bytes.Equal(bytes.TrimSpace(j.Metadata), []byte("null")) {
		var fields map[string]any
		decoder := json.NewDecoder(bytes.NewReader(j.Metadata))
		decoder.UseNumber()
		if err = decoder.Decode(&fields); err != nil || !json.Valid(j.Metadata) {
			return nil, fmt.Errorf("invalid frozen metadata")
		}
		converted, err := metadataFields(fields)
		if err != nil {
			return nil, err
		}
		job["metadata"] = converted
	}
	return map[string]any{"job": job}, nil
}
func encodeRestart(ctx context.Context, r jobdb.SubmitRestartJobRequest) (any, error) {
	j := r.Job
	job := map[string]any{"cell": j.Cell, "shareGoal": j.ShareGoal, "lastStepToKeep": j.LastStepToKeep, "priorJobKey": j.PriorJobKey}
	if j.CellRepo != "" {
		job["cellRepo"] = j.CellRepo
	}
	if len(j.Dependencies) > 0 {
		job["dependencies"] = j.Dependencies
	}
	if j.ClientPayloadUpdate != nil {
		job["clientPayloadUpdate"] = j.ClientPayloadUpdate
	}
	if j.ExecutionRestart != nil {
		job["executionRestart"] = j.ExecutionRestart
	}
	if j.Schema != nil {
		job["schema"] = schema(j.Schema)
	}
	if len(j.Prerequisites) > 0 {
		job["prerequisites"] = prerequisites(j.Prerequisites)
	}
	for name, data := range map[string]jobdb.TaskData{"extraTaskInput": j.ExtraTaskInput, "extraTaskOutput": j.ExtraTaskOutput} {
		if data != nil {
			encoded, err := taskData(ctx, data)
			if err != nil {
				return nil, err
			}
			job[name] = encoded
		}
	}
	body := map[string]any{"job": job, "requestId": r.RequestID}
	if r.WorkerID != "" {
		body["workerId"] = r.WorkerID
	}
	if !r.RequestTime.IsZero() {
		body["requestTime"] = r.RequestTime
	}
	return body, nil
}
func schema(s *jobdb.JobSchemaSelector) any {
	out := map[string]any{}
	if s.Hash != "" {
		out["schemaHash"] = s.Hash
	}
	if len(s.Schema) > 0 {
		out["schema"] = s.Schema
	}
	return out
}
func prerequisites(p []jobdb.JobPrerequisite) any {
	out := make([]map[string]any, 0, len(p))
	for _, v := range p {
		out = append(out, map[string]any{"jobId": v.JobID, "condition": v.Condition})
	}
	return out
}
func taskData(ctx context.Context, data jobdb.TaskData) (any, error) {
	if data == nil {
		return nil, fmt.Errorf("frozen task data is required")
	}
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 && !json.Valid(raw) {
		return nil, fmt.Errorf("invalid frozen application JSON")
	}
	artifacts, err := data.GetArtifacts()
	if err != nil {
		return nil, err
	}
	writes := make([]map[string]any, 0, len(artifacts))
	for _, a := range artifacts {
		if a == nil {
			return nil, fmt.Errorf("nil frozen artifact")
		}
		b, err := a.Bytes(ctx)
		if err != nil {
			return nil, err
		}
		writes = append(writes, map[string]any{"name": a.Name(), "size": a.Size(), "contentBase64": b})
	}
	return map[string]any{"data": json.RawMessage(raw), "artifacts": writes}, nil
}
func metadataFields(fields map[string]any) (any, error) {
	out := map[string]any{}
	for k, v := range fields {
		converted, err := metadataValue(v)
		if err != nil {
			return nil, err
		}
		out[k] = converted
	}
	return map[string]any{"fields": out}, nil
}
func metadataValue(v any) (any, error) {
	switch v := v.(type) {
	case nil:
		return map[string]any{"kind": "null"}, nil
	case bool:
		return map[string]any{"kind": "bool", "boolValue": v}, nil
	case string:
		return map[string]any{"kind": "string", "stringValue": v}, nil
	case json.Number:
		if !strings.ContainsAny(string(v), ".eE") {
			if n, err := v.Int64(); err == nil {
				return map[string]any{"kind": "int", "intValue": n}, nil
			}
		}
		n, err := v.Float64()
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": "double", "doubleValue": n}, nil
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			encoded, err := metadataValue(item)
			if err != nil {
				return nil, err
			}
			out = append(out, encoded)
		}
		return map[string]any{"kind": "list", "listValue": out}, nil
	case map[string]any:
		fields, err := metadataFields(v)
		return map[string]any{"kind": "map", "mapValue": fields}, err
	default:
		return nil, fmt.Errorf("invalid frozen metadata value")
	}
}
