package compiler

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExecuteRecipePathMatchExample(t *testing.T) {
	data, err := os.ReadFile("../../../examples/path-match.yaml")
	require.NoError(t, err)
	rec := mustLoadRecipe(t, string(data))
	for _, tt := range []struct {
		reference, expected string
	}{
		{"outbox/design.md", `{"status":"matched","path":"design.md","candidates":["design.md"]}`},
		{"reply.md", `{"status":"ambiguous","path":"","candidates":["archive/reply.md","current/reply.md"]}`},
		{"missing.md", `{"status":"not_found","path":"","candidates":[]}`},
		{"../design.md", `{"status":"invalid","path":"","candidates":[]}`},
	} {
		t.Run(tt.reference, func(t *testing.T) {
			jobCtx, gitCtx := GenerateTestContext()
			ctx := newWorkflowContext(&countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}})
			result, _, err := ExecuteRecipe(ctx, rec, map[string]any{"reference": tt.reference}, jobCtx, gitCtx)
			require.NoError(t, err)
			encoded, err := json.Marshal(result)
			require.NoError(t, err)
			require.JSONEq(t, `{"match":`+tt.expected+`}`, string(encoded))
		})
	}
}
