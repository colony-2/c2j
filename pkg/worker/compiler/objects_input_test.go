package compiler

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/pkg/objects"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestObjectInputDependenciesAreNestedOpaqueAndStable(t *testing.T) {
	first, _, err := objects.Parse(objects.ValidationRef("test.session/v1"))
	require.NoError(t, err)
	first.Artifact.JobId = "source-a"
	second := first
	second.Artifact.JobId = "source-b"
	type typedInput struct {
		Session objects.Ref              `json:"session"`
		Nested  map[string][]objects.Ref `json:"nested"`
	}
	input := typedInput{Session: first, Nested: map[string][]objects.Ref{"sessions": {second, first}}}
	raw, err := objects.JSONValue(input)
	require.NoError(t, err)
	for i := 0; i < 30; i++ {
		normalized, err := NormalizeOpInput(reflect.TypeOf(input), raw.(map[string]any))
		require.NoError(t, err)
		require.Equal(t, []string{"source-a", "source-b"}, []string{normalized.StoredArtifactKeys[0].JobId, normalized.StoredArtifactKeys[1].JobId})
		require.Len(t, normalized.StoredArtifactKeys, 2)
		before, err := json.Marshal(raw)
		require.NoError(t, err)
		after, err := json.Marshal(normalized.Data)
		require.NoError(t, err)
		require.JSONEq(t, string(before), string(after))
	}
	malformed := objects.ValidationRef("test.session/v1")
	malformed["sha256"] = "bad"
	_, err = NormalizeOpInput(nil, map[string]any{"nested": []any{malformed}})
	require.Error(t, err)
}

func TestNativeObjectOutputValidationDoesNotInventRuntimeCheckpoints(t *testing.T) {
	type output struct {
		Session *objects.Ref `json:"session,omitempty" object_type:"test.session/v1"`
	}
	zeros, err := zeroOutputFromType(reflect.TypeOf(output{}))
	require.NoError(t, err)
	ref, ok, err := objects.Parse(zeros["session"])
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "test.session/v1", ref.Type)
	require.NotContains(t, normalizeOpOutput(reflect.TypeOf(output{}), nil), "session")
}

func TestNativeObjectRecipeValidationRoutesCheckpointTypes(t *testing.T) {
	type input struct {
		Session objects.Ref `json:"session"`
	}
	type output struct {
		Session objects.Ref `json:"session" object_type:"test.session/v1"`
	}
	producer := coreops.NewActivityMappedOpV2[struct{}, output](coreops.OpMetadata{Type: "object_producer"}, func(coreops.OpDependencies, context.Context, struct{}) (output, error) {
		t.Error("validation executed producer")
		return output{}, nil
	})
	consumer := coreops.NewActivityMappedOpV2[input, output](coreops.OpMetadata{Type: "object_consumer"}, func(coreops.OpDependencies, context.Context, input) (output, error) {
		t.Error("validation executed consumer")
		return output{}, nil
	})
	withRegisteredOps(t, producer, consumer)
	rec, err := recipe.LoadRecipeFromString([]byte(`id: validate-objects
version: "1"
sequence:
 - id: produce
   op: object_producer
 - id: consume
   op: object_consumer
   inputs:
    session: "${{ sequence.produce.outputs.session }}"
outputs:
 session: "${{ sequence.consume.outputs.session }}"
`))
	require.NoError(t, err)
	job, git := GenerateTestContext()
	inner := &countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}}
	out, _, err := ExecuteRecipe(newWorkflowContext(inner), *rec, nil, job, git, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.Zero(t, inner.calls)
	ref, ok, err := objects.Parse(out["session"])
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "test.session/v1", ref.Type)
}
