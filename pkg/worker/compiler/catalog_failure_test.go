package compiler

import (
	"encoding/json"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExecutionBaseCatalogSerializedFailure(t *testing.T) {
	for _, code := range []string{executioncatalog.Unsupported, executioncatalog.UnsupportedRequirements, "OTHER_APPLICATION_ERROR"} {
		original := jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: "refused", Attrs: map[string]any{"code": code, "selector": "bad", "named_bases": []string{"linux"}, "source": "task invocation"}}}
		raw, err := json.Marshal(original)
		require.NoError(t, err)
		var decoded jobdb.AppError
		require.NoError(t, json.Unmarshal(raw, &decoded))
		failure := normalizeRuntimeFailure(&decoded, nil, recipe.NodeMetadata{}, recipe.FailureNodeOp, "probe")
		require.Equal(t, code, failure.Code)
		require.Equal(t, code != executioncatalog.Unsupported && code != executioncatalog.UnsupportedRequirements, failure.Retryable)
		require.Equal(t, "task invocation", failure.Attrs["source"])
		require.Equal(t, []any{"linux"}, failure.Attrs["named_bases"])
	}
}
