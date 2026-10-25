package compiler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestSecretIncludeBoundarySchemaAndOutputs(t *testing.T) {
	dir := t.TempDir()
	rootFile := filepath.Join(dir, "root.yaml")
	included := filepath.Join(dir, "included.yaml")
	for _, marked := range []bool{true, false} {
		t.Run(map[bool]string{true: "secret", false: "ordinary"}[marked], func(t *testing.T) {
			flag := "false"
			if marked {
				flag = "true"
			}
			require.NoError(t, os.WriteFile(included, []byte("id: included\ninput_schema:\n  token: {type: string, secret: "+flag+", required: true}\nsequence: []\n"), 0600))
			r, err := recipe.LoadRecipeFromString([]byte("id: root\nsequence:\n  - include: ./included.yaml\n    perms: {limit: {jobdb: {}}}\n    inputs: {token: '${{ context.secrets.token }}'}\n"))
			require.NoError(t, err)
			expanded, err := ResolveInlineRecipes(context.Background(), *r, InlineResolutionOptions{RootFile: rootFile})
			require.NoError(t, err)
			job, git := GenerateTestContext()
			c := newWorkflowContext(&countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}})
			_, _, err = ExecuteRecipe(c, expanded.Recipe, nil, job, git)
			if marked {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "not marked secret")
			}
		})
	}
	ctx := newChildGroupRenderContext(t, nil, nil)
	input, err := renderChildInputs(ctx, map[string]any{"token": "${{ context.secrets.token }}"}, nil)
	require.NoError(t, err)
	require.Equal(t, secrets.Reference{Name: "token"}, input["token"])
	_, err = ctx.ResolveMap(map[string]any{"token": "${{ {'$c2j_secret': 'token'} }}"})
	require.Error(t, err)
}
