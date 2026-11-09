package story_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/jobrestart"
	restartservice "github.com/colony-2/c2j/pkg/jobrestart/service"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/story"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type unavailableRecipeRuntime struct {
	jobdb.WorkflowRuntime
	cause error
}

func (r unavailableRecipeRuntime) OpenArtifact(context.Context, jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	return nil, r.cause
}
func TestRestartDiscoveryWithMissingRecipeBytes(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, restartSequenceRecipe)
			cause := errors.New("retained recipe bytes expired")
			engine, err := wf.NewEngineBuilder().WithRuntime(unavailableRecipeRuntime{f.runtime, cause}).WithWorkerTenantId(f.key.TenantId).PlusWorkers(f.worker).BuildEngine()
			require.NoError(t, err)
			svc := &restartservice.Service{Engine: engine, Tenant: f.key.TenantId}
			points, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "points", JobID: f.key.JobId})
			require.NoError(t, err)
			require.NotEmpty(t, points.Points.RestartScopes)
			var scope string
			for _, point := range points.Points.RestartScopes {
				if strings.Contains(point.ScopeID, `scope:"op:target"`) {
					scope = point.ScopeID
				}
			}
			require.NotEmpty(t, scope)
			result, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "context", JobID: f.key.JobId, Selector: jobrestart.Selector{Scope: scope}})
			require.ErrorIs(t, err, story.ErrRestartContextUnavailable)
			require.ErrorContains(t, err, cause.Error())
			require.Nil(t, result.Context)
			require.Equal(t, int32(4), f.task.calls.Load())
		})
	}
}
func TestRestartInspectionBeforeInvalidInputsPreservesReferences(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			digest := strings.Repeat("a", 64)
			key := jobdb.ArtifactKey{JobId: "not-readable", TaskOrdinal: 1, Name: objects.ArtifactPrefix + digest + ".tar", SizeBytes: 12}
			input := map[string]any{"configuration": configuration.Reference{Kind: "config", Name: "not-resolved", ExpectedType: "string"}, "object": objects.Ref{Format: objects.Version, Type: "fixture/v1", TenantID: "restart-tests", Artifact: key, SHA256: digest}, "artifact": artifacts.NewStoredRef(key)}
			f := newRestartFixtureInputs(t, backend, `schema_version: 2
id: safety
input_schema:
 configuration: {type: string}
 object: {type: checkpoint, object_type: fixture/v1}
 artifact: {type: artifact}
inputs:
 configuration: '${{ inputs.configuration }}'
 object: '${{ inputs.object }}'
 artifact: '${{ inputs.artifact }}'
sequence:
 - id: target
   op: restart_context_probe
   inputs: {value: '${{ inputs.missing }}'}
`, input, true)
			before, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			svc := &restartservice.Service{Engine: f.engine, Tenant: f.key.TenantId}
			points, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "points", JobID: f.key.JobId})
			require.NoError(t, err)
			var scope string
			for _, point := range points.Points.RestartScopes {
				if strings.Contains(point.ScopeID, `scope:"op:target"`) {
					scope = point.ScopeID
				}
			}
			require.NotEmpty(t, scope, "%+v", *before.Attempts[0].Outcome.Error)
			result, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "context", JobID: f.key.JobId, Selector: jobrestart.Selector{Scope: scope}})
			require.NoError(t, err)
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			visible, err := json.Marshal(result.Context.Visible["inputs"])
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(visible))
			zero := int64(0)
			submission, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "context", JobID: f.key.JobId, Selector: jobrestart.Selector{Chapter: &zero}})
			require.NoError(t, err)
			require.NotContains(t, submission.Context.Visible, "sequence")
			submitted, err := json.Marshal(submission.Context.Visible["inputs"])
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(submitted))

			require.Zero(t, f.task.calls.Load(), "selected inputs fail when evaluated; inspection stops before they resolve")
			after, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
