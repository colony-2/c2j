package compiler

import (
	"bytes"
	"encoding/json"

	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// Recover only the historical empty-struct encoding, optionally combined with
// the existing artifact dependency permutation. Never return cached output
// directly: retry the verified bytes at JobDB's unchanged replay ordinal.
func recoverEmptyPreparation(taskType string, data jobdb.TaskData, err error, invoke func(jobdb.TaskData) (jobdb.TaskData, error)) (jobdb.TaskData, bool, error) {
	mismatch, ok := jobworkflow.UnexpectedChapter(err)
	if !ok || mismatch.TaskType != taskType || mismatch.Meta.TaskType != taskType || len(mismatch.CachedInput) == 0 {
		return nil, false, nil
	}
	current, e := data.GetData()
	if e != nil {
		return nil, false, e
	}
	currentCanonical, currentPresent, e := withoutEmptyPreparation(current)
	if e != nil {
		return nil, false, nil
	}
	cachedCanonical, cachedPresent, e := withoutEmptyPreparation(mismatch.CachedInput)
	if e != nil || currentPresent == cachedPresent {
		return nil, false, nil
	}
	if !bytes.Equal(currentCanonical, cachedCanonical) {
		// Legacy dependency sets could also have been emitted in map order.
		currentCanonical, e = canonicalArtifactOrder(currentCanonical)
		if e != nil {
			return nil, false, nil
		}
		cachedCanonical, e = canonicalArtifactOrder(cachedCanonical)
		if e != nil || !bytes.Equal(currentCanonical, cachedCanonical) {
			return nil, false, nil
		}
	}
	original, e := withRecordedArtifactOrder(current, mismatch.CachedInput)
	if e != nil {
		return nil, false, e
	}
	artifacts, e := data.GetArtifacts()
	if e != nil {
		return nil, false, e
	}
	hash, e := legacyTaskInputHash(original, artifacts)
	if e != nil {
		return nil, false, e
	}
	if hash != mismatch.CachedInputHash {
		return nil, false, nil
	}
	recorded := jobworkflow.WithTaskOptions(&jobdb.SimpleTaskData{Data: original, Artifacts: artifacts}, jobworkflow.TaskOptionsFor(data))
	out, e := invoke(recorded)
	return out, true, e
}

// Preserve every other field, including unknown fields and exact JSON numbers.
// null, nonempty preparation, and objects containing empty scopes are not the
// historical {} encoding and must not be treated as absent.
func withoutEmptyPreparation(raw []byte) ([]byte, bool, error) {
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil, false, err
	}
	prep, present := fields["preparation"]
	if object, ok := prep.(map[string]any); ok && len(object) == 0 {
		delete(fields, "preparation")
	}
	raw, err := json.Marshal(fields)
	return raw, present, err
}

// Field order is part of the hash. Reconstruct the known struct encoding rather
// than marshalling CachedInput as a map (HTTP may reorder that map's fields).
func marshalRecordedPreparation(request workerops.ActivityInvocationRequest, cached []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(cached, &fields); err != nil {
		return nil, err
	}
	prep, present := fields["preparation"]
	if present && bytes.Equal(bytes.TrimSpace(prep), []byte(`{}`)) && request.Preparation.IsZero() {
		type invocationFields workerops.ActivityInvocationRequest
		return json.Marshal(struct {
			Preparation workerops.PreparationRequest `json:"preparation"`
			invocationFields
		}{Preparation: request.Preparation, invocationFields: invocationFields(request)})
	}
	return json.Marshal(request)
}
