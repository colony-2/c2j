package workjob

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/executionflags"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestExecutionHandoffStopsAnyAndLoop(t *testing.T) {
	for _, declaration := range []string{"execution", "execution_needs"} {
		for _, mode := range []string{"any", "loop"} {
			t.Run(declaration+"/"+mode, func(t *testing.T) {
				c2jops.Register()
				rt := toy.New()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				body := "sequence: []\n"
				if declaration == "execution_needs" {
					body = "op: command_execution\ninputs: {run: 'true'}\n"
				}
				r, err := recipe.LoadRecipeFromString([]byte("id: resources\n" + declaration + ":\n  resources:\n    memory: 16Gi\n" + body))
				require.NoError(t, err)
				_, err = starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", RecipeName: "resources"}, jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}, *r)
				require.NoError(t, err)
				server := httptest.NewServer(remote.NewServer(rt))
				defer server.Close()
				var stdout, stderr bytes.Buffer
				small := "2Gi"
				flags := executionflags.Options{Memory: &small}
				uri := server.URL + "/tenant"
				if mode == "any" {
					err = RunOne(ctx, RunOneOptions{JobDBURI: uri, ExecutionFlags: flags, Stdout: &stdout, Stderr: &stderr})
				} else {
					err = Run(ctx, Options{JobDBURI: uri, ExecutionFlags: flags, Stdout: &stdout, Stderr: &stderr})
				}
				require.NoError(t, err)
				require.NoError(t, ctx.Err(), "must stop at handoff, not poll until timeout")
				require.Contains(t, stdout.String(), `"kind":"environment_required"`)
				require.NotContains(t, stdout.String(), "no jobs found")
			})
		}
	}
}
