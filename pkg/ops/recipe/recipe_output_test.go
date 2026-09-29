package recipe

import (
	"context"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// Child Git artifacts are dependency evidence, never the awaiting op's snapshot.
// The implicit root has no Workspace, including after a consultation exits.
func TestChildResultsKeepSnapshotProvenanceWithoutForwardingChildState(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "root"
		if explicit {
			name = "explicit_workspace"
		}
		t.Run(name, func(t *testing.T) {
			for _, method := range []string{"get_result", "await_result", "await_result_soft", "failed_await_result_soft", "child_group"} {
				t.Run(method, func(t *testing.T) {
					var artifacts []jobdb.Artifact
					expected := map[string]recipeartifacts.Ref{}
					for _, name := range []string{gitstate.ThinPackArtifactName, "diff_from_parent.diff", "diff_from_base.diff", "verification.txt"} {
						artifact := jobdb.NewArtifactFromBytes(name, []byte("child evidence"))
						key := jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 7, Name: name, SizeBytes: 14}
						jobdb.AssignArtifactKey(artifact, key)
						artifacts = append(artifacts, artifact)
						expected[name] = recipeartifacts.NewStoredRef(key)
					}
					expected["report"] = recipeartifacts.NewExternalRef("report", "https://example.com/report", false)
					expected["verification.json"] = recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "child", TaskOrdinal: 3, Name: "verification.json", SizeBytes: 42})
					data, err := jobdb.NewTaskData(map[string]any{
						"output":        map[string]any{"merged": true},
						"artifact_refs": map[string]recipeartifacts.Ref{"report": expected["report"], "verification.json": expected["verification.json"]},
					}, artifacts...)
					require.NoError(t, err)
					ctl := &fakeWorkflowControl{
						jobResultFunc: func(context.Context, jobdb.JobKey) (jobdb.JobData, error) { return data, nil },
						inspectFunc: func(_ context.Context, key jobdb.JobKey) (workflowctl.JobInspection, error) {
							status := "completed"
							if method == "failed_await_result_soft" {
								status = "failed"
							}
							return workflowctl.JobInspection{JobKey: key, Terminal: true, Status: status, Output: data}, nil
						},
					}
					git := ops.GitExecutionContext{CellName: "A"}
					if explicit {
						git.Workspace = &contextual.WorkspaceContext{Cell: "B", ScopeID: "consult"}
					}
					deps := ops.NewOpDependenciesBuilder().WithWorkflowControl(ctl).
						WithJobTool(&fakeJobTool{key: jobdb.JobKey{TenantId: "test", JobId: "parent"}}).
						WithGitContext(git).Build()
					var refs map[string]recipeartifacts.Ref
					switch method {
					case "get_result", "await_result":
						call := getRecipeOutput
						if method == "await_result" {
							call = waitAndGetRecipeOutput
						}
						out, err := call(deps, context.Background(), StartedJob{JobId: "child"})
						require.NoError(t, err)
						require.Equal(t, true, out.Outputs["merged"])
						refs = out.Artifacts
					case "child_group":
						out, err := getChildGroupRecipeOutput(deps, context.Background(), "child")
						require.NoError(t, err)
						refs = out.Artifacts
					default:
						out, err := awaitResultSoft(deps, context.Background(), AwaitResultSoftInput{JobID: "child"})
						require.NoError(t, err)
						refs = out.Artifacts
					}
					require.Equal(t, expected, refs, "all evidence must retain child artifact keys")
					forwarded := deps.GetOutputArtifacts()
					require.Len(t, forwarded, 1, "only user artifacts should join the parent's task result")
					require.Equal(t, "verification.txt", forwarded[0].Name())
					require.Equal(t, expected["report"], deps.GetExternalArtifacts()["report"])
				})
			}
		})
	}
}
