package recipe

import (
	"context"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestChildTitlesSeparateFromInputsAndIdentity(t *testing.T) {
	for _, count := range []int{1, 2} {
		specs := make([]SingleRecipe, count)
		for i := range specs {
			specs[i] = SingleRecipe{Name: "strict", Title: "  Child café\n ", Inputs: map[string]any{"value": "ok"}}
		}
		ctl := &fakeWorkflowControl{}
		ctx := celltest.Context(context.Background())
		parent := jobdb.JobKey{TenantId: "tenant", JobId: "parent"}
		_, err := startJobs(ctx, nil, parent, "invocation", ctl, "example.test/repo", "main", specs, "main", jobcontext.Parent{})
		require.NoError(t, err)
		require.Len(t, ctl.startRequests, count)
		for i, req := range ctl.startRequests {
			require.Equal(t, specs[i].Title, req.Title)
			require.Equal(t, specs[i].Inputs, req.Inputs)
			require.Equal(t, deterministicChildJobID(parent, "invocation", i), req.JobID)
			specs[i].Title = "changed"
		}
		replay := &fakeWorkflowControl{}
		_, err = startJobs(ctx, nil, parent, "invocation", replay, "example.test/repo", "main", specs, "main", jobcontext.Parent{})
		require.NoError(t, err)
		require.Len(t, replay.startRequests, count)
		for i, req := range replay.startRequests {
			require.Equal(t, ctl.startRequests[i].JobID, req.JobID)
		}
	}
}

func TestChildTitleRejectsSecretReferences(t *testing.T) {
	for _, title := range []any{secrets.Reference{Name: "private"}, "${{ context.secrets.private }}", "${{ context.secrets.?private.orValue(\"public\") }}"} {
		if text, ok := title.(string); ok {
			ref, recognized := secrets.Template(text)
			require.True(t, recognized)
			title = ref // The template evaluator supplies opaque references to operation validation.
		}
		require.Error(t, secrets.ValidateType(reflect.TypeFor[SingleRecipeWithRef](), map[string]any{"name": "child", "title": title}))
		require.Error(t, secrets.ValidateType(reflect.TypeFor[MultipleRecipes](), map[string]any{"recipes": []any{map[string]any{"name": "child", "title": title}}}))
	}
}
