package childbroker_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type recipeCapture struct {
	job   jobdb.SubmitJob
	calls int
}

func (c *recipeCapture) SubmitJob(_ context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	c.job = job
	c.calls++
	return jobdb.JobKey{TenantId: job.TenantId, JobId: job.JobID}, nil
}

func TestBrokerCompiledIncludeRoundTrip(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		name := "include"
		if workspace {
			name = "workspace-include"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			source := "id: child\nsequence:\n  - id: phase\n    include: ./phase.yaml\n"
			if workspace {
				source += "    workspace: {cell: cellB, ref: release}\n"
			}
			phase := []byte("id: phase\ninput_schema:\n  message: {type: string, default_value: hello}\ninputs:\n  message: '{{ inputs.message }}'\nsequence:\n  - id: echo\n    op: command_execution\n    inputs:\n      run: '{{ inputs.message }}'\noutputs:\n  message: '{{ inputs.message }}'\n")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "phase.yaml"), phase, 0600))
			authored, err := recipe.LoadRecipeFromString([]byte(source))
			require.NoError(t, err)
			compiled, err := compiler.ResolveInlineRecipes(ctx, *authored, compiler.InlineResolutionOptions{RootFile: filepath.Join(dir, "child.yaml")})
			require.NoError(t, err)
			capture := &recipeCapture{}
			server, err := childbroker.Start(ctx, childbroker.Options{Current: jobcontext.Current{TenantID: "test", JobID: "parent"}, Submitter: capture})
			require.NoError(t, err)
			defer server.Close()
			env := server.Env()
			broker, ok, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return env[k] })
			require.NoError(t, err)
			require.True(t, ok)
			req, err := childbroker.NewSubmitRequest(ctx, workflowctl.StartJob{TenantId: "test", JobID: "child", RecipeName: "child"}, nil, compiled.Recipe)
			require.NoError(t, err)
			// The source parser must still reject compiler-owned metadata.
			_, err = recipe.LoadRecipeFromReader(bytes.NewReader(req.EmbeddedRecipes[0].YAML))
			require.ErrorContains(t, err, "reserved")
			format := req.EmbeddedRecipes[0].Format
			require.NotEmpty(t, format)
			req.EmbeddedRecipes[0].Format = ""
			_, err = childbroker.Submit(ctx, broker, req)
			require.ErrorContains(t, err, "reserved")
			req.EmbeddedRecipes[0].Format = "future-format"
			_, err = childbroker.Submit(ctx, broker, req)
			require.ErrorContains(t, err, "unsupported embedded recipe format")
			require.Zero(t, capture.calls)
			req.EmbeddedRecipes[0].Format = format
			response, err := childbroker.Submit(ctx, broker, req)
			require.NoError(t, err)
			require.Equal(t, "child", response.JobID)
			require.Equal(t, 1, capture.calls)
			artifacts, err := capture.job.Data.GetArtifacts()
			require.NoError(t, err)
			require.Len(t, artifacts, 1)
			raw, err := artifacts[0].Bytes(ctx)
			require.NoError(t, err)
			restored, err := recipe.LoadInternalRecipeFromString(raw)
			require.NoError(t, err)
			require.Equal(t, compiled.Recipe, *restored)
		})
	}
}
