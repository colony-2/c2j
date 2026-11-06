package compiler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/stretchr/testify/require"
)

func TestRecipeInspectionExportsActualWorkspaceAndFinalFields(t *testing.T) {
	workspaceTestWorker(t)
	repo := filepath.Join(t.TempDir(), "repo")
	base := workspaceRepo(t, repo, "source")
	rt := newTimeoutReproRuntime(t, "sqlite")
	key := submitGitLifecycle(t, rt, repo, `id: inspection
version: "1"
input_schema: {}
sequence:
 - id: implement
   op: workspace_probe
   inputs: {write: candidate.txt}
 - id: inspect
   const: true
   op: workspace_probe
   inputs: {read: candidate.txt}
outputs:
 text: '${{ sequence.inspect.outputs.text }}'
 candidate: '${{ sequence.inspect.outputs.head }}'
`)
	out, e := runGitLifecycle(t, rt, key)
	require.NoError(t, e)
	expected := gitLifecycleOutput(t, out)
	ctx := celltest.Context(context.Background())
	api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
	p := "/text"
	result, e := api.Inspect(ctx, jobinspect.Request{Operation: "result", JobID: key.JobId, Field: &p})
	require.NoError(t, e)
	require.Contains(t, string(result.Value), expected["text"])
	log, e := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Limit: 100})
	require.NoError(t, e)
	story, e := api.Inspect(ctx, jobinspect.Request{Operation: "story", JobID: key.JobId, Limit: 100})
	require.NoError(t, e)
	require.NotEmpty(t, story.Scopes)
	require.Equal(t, "succeeded", story.Scopes[0].Status)
	var task string
	for _, entry := range log.Tasks {
		if strings.Contains(entry.Path, "inspect") {
			task = entry.ID
		}
	}
	require.NotEmpty(t, task)
	dir := filepath.Join(t.TempDir(), "recovered")
	snapshot, e := jobinspect.Export(ctx, api, jobinspect.Request{JobID: key.JobId, Task: task}, dir)
	require.NoError(t, e)
	require.Len(t, snapshot.Workspaces, 1)
	w := snapshot.Workspaces[0]
	require.Equal(t, base, w.BaseCommit)
	require.Equal(t, expected["candidate"], w.HeadCommit)
	data, e := os.ReadFile(filepath.Join(dir, w.Directory, "candidate.txt"))
	require.NoError(t, e)
	require.Equal(t, expected["text"], string(data))
	before, e := api.Inspect(ctx, jobinspect.Request{Operation: "snapshot", JobID: key.JobId, Task: task, Boundary: "before"})
	require.NoError(t, e)
	require.Equal(t, w.HeadCommit, before.Snapshot.Workspaces[0].HeadCommit)
}
