package compiler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// Older compilers serialized artifact dependency sets in map iteration order.
// Recover only this permutation, using the recorded outcome at the consumed
// ordinal. Never execute a task again to repair a replay mismatch.
type taskHistoryReader = workflow.TaskHistoryReader

func configureArtifactReplay(a *thinpackForwarder, ctx workflow.Context) {
	a.history = ctx.TaskHistory
	if a.history == nil && ctx.ServiceDependencies2 != nil {
		a.history, _ = ctx.WorkflowControl().(taskHistoryReader)
	}
}

func (a *thinpackForwarder) recoverArtifactOrder(taskType string, data jobdb.TaskData, err error) (jobdb.TaskData, bool, error) {
	mismatch, ok := jobworkflow.UnexpectedChapter(err)
	if !ok || a.history == nil || mismatch.TaskType != taskType || len(mismatch.CachedInput) == 0 {
		return nil, false, nil
	}
	current, e := data.GetData()
	if e != nil {
		return nil, false, e
	}
	cachedCanonical, e := canonicalArtifactOrder(mismatch.CachedInput)
	if e != nil {
		return nil, false, nil
	}
	currentCanonical, e := canonicalArtifactOrder(current)
	if e != nil || !bytes.Equal(cachedCanonical, currentCanonical) {
		return nil, false, nil
	}
	// Comparing JSON alone is insufficient: the automatic restore pack is also
	// part of JobDB's input hash. Verify the full hash using the original JSON.
	artifacts, e := data.GetArtifacts()
	if e != nil {
		return nil, false, e
	}
	// Remote JobDB may reorder JSON object properties in CachedInput. Rebuild
	// the original invocation encoding with only its recorded dependency order.
	original, e := withRecordedArtifactOrder(current, mismatch.CachedInput)
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
	out, e := a.history.CachedTaskOutput(context.Background(), a.GetJobKey(), taskType, mismatch.Ordinal, mismatch.CachedInputHash)
	if e != nil {
		return nil, false, e
	}
	if out == nil {
		return nil, false, fmt.Errorf("cached task %s ordinal %d has no output", taskType, mismatch.Ordinal)
	}
	return out, true, nil
}

func canonicalArtifactOrder(raw []byte) ([]byte, error) {
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	keys, ok := request["artifact_keys"].([]any)
	if !ok || len(keys) < 2 {
		return nil, fmt.Errorf("no artifact dependency permutation")
	}
	encoded := make([]json.RawMessage, len(keys))
	for i, key := range keys {
		b, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		encoded[i] = b
	}
	sort.Slice(encoded, func(i, j int) bool { return bytes.Compare(encoded[i], encoded[j]) < 0 })
	request["artifact_keys"] = encoded
	return json.Marshal(request)
}

// JobDB v0.0.19 hashes the exact input JSON followed by sorted name|SHA256
// artifact entries. Keep this compatibility check fail-closed if that format
// changes; integration tests exercise it against the pinned JobDB runtime.
func legacyTaskInputHash(raw []byte, artifacts []jobdb.Artifact) (string, error) {
	parts := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		digest, err := artifact.Sha256(context.Background())
		if err != nil {
			return "", err
		}
		parts = append(parts, artifact.Name()+"|"+digest)
	}
	sort.Strings(parts)
	hash := sha256.New()
	hash.Write(raw)
	for _, part := range parts {
		hash.Write([]byte(part))
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func withRecordedArtifactOrder(current, cached []byte) ([]byte, error) {
	var request, recorded workerops.ActivityInvocationRequest
	for _, item := range []struct {
		raw    []byte
		target *workerops.ActivityInvocationRequest
	}{{current, &request}, {cached, &recorded}} {
		decoder := json.NewDecoder(bytes.NewReader(item.raw))
		decoder.UseNumber()
		if err := decoder.Decode(item.target); err != nil {
			return nil, err
		}
	}
	request.ArtifactKeys = recorded.ArtifactKeys
	return json.Marshal(request)
}
