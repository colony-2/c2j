package runjob

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/executionflags"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRunReportsEnvironmentHandoffAndResumes(t *testing.T) {
	rt := toy.New()
	ctx := context.Background()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString([]byte("id: resources\nexecution:\n  resources:\n    memory: 16Gi\nsequence: []\n"))
	require.NoError(t, err)
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", RecipeName: "resources"}, jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}, *r)
	require.NoError(t, err)
	server := httptest.NewServer(remote.NewServer(rt))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	small := "2Gi"
	opts := Options{JobID: key.JobId, JobDBURI: server.URL + "/tenant", ExecutionFlags: executionflags.Options{Memory: &small}, Stdout: &stdout, Stderr: &stderr, Stdin: bytes.NewReader(nil), CI: true}
	require.NoError(t, Run(ctx, opts))
	require.Contains(t, stdout.String(), `"kind":"environment_required"`)
	require.NotContains(t, stdout.String(), "waiting:")
	info, err := rt.GetJob(ctx, key)
	require.NoError(t, err)
	revision := info.ClientPayloadRevision
	stdout.Reset()
	require.NoError(t, Run(ctx, opts))
	require.Contains(t, stdout.String(), `"published":false`)
	info, err = rt.GetJob(ctx, key)
	require.NoError(t, err)
	require.Equal(t, revision, info.ClientPayloadRevision)
	large := "16Gi"
	opts.ExecutionFlags.Memory = &large
	stdout.Reset()
	require.NoError(t, Run(ctx, opts))
	require.NotContains(t, stdout.String(), `"kind":"environment_required"`)
	info, err = rt.GetJob(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", string(info.Status))
}

func TestRunNodeExecutionNeedsGuardsRealOperation(t *testing.T) {
	c2jops.Register()
	rt := toy.New()
	ctx := context.Background()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString([]byte("id: node-needs\nexecution_needs: {resources: {memory: 16Gi}}\nop: command_execution\ninputs: {run: 'true'}\n"))
	require.NoError(t, err)
	jobContext, git := compiler.GenerateTestContext()
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", RecipeName: "node-needs", JobContext: jobContext, GitRef: git.ParentRef}, jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}, *r)
	require.NoError(t, err)
	server := httptest.NewServer(remote.NewServer(rt))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	memory := "2Gi"
	opts := Options{JobID: key.JobId, JobDBURI: server.URL + "/tenant", ExecutionFlags: executionflags.Options{Memory: &memory}, Stdout: &stdout, Stderr: &stderr, Stdin: bytes.NewReader(nil), CI: true}
	require.NoError(t, Run(ctx, opts))
	require.Contains(t, stdout.String(), `"kind":"environment_required"`)
	memory = "16Gi"
	stdout.Reset()
	require.NoError(t, Run(ctx, opts))
	require.NotContains(t, stdout.String(), `"kind":"environment_required"`)
	info, err := rt.GetJob(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", string(info.Status))
}
