package recipe

import (
	"context"
	"fmt"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type bindingTool struct {
	fakeJobTool
	call func(...jobdb.Dependency) ([]jobdb.DependencyResolution, error)
}

func (t *bindingTool) AwaitDependencies(d ...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
	return t.call(d...)
}
func TestGoalWaitContractAndBindings(t *testing.T) {
	tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}}
	ctl := &fakeWorkflowControl{jobResultFunc: func(_ context.Context, key jobdb.JobKey) (jobdb.JobData, error) {
		require.Equal(t, "winner", key.JobId)
		return jobdb.NewTaskDataOrPanic(map[string]any{"output": map[string]any{"answer": 42}}), nil
	}}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).Build()
	tool.call = func(d ...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
		require.Equal(t, []jobdb.Dependency{{Target: jobdb.DependencyTarget{GoalID: "g"}, Condition: jobdb.JobPrereqSuccess}}, d)
		return []jobdb.DependencyResolution{{Target: d[0].Target, State: jobdb.DependencySatisfied, SatisfiedByJobID: "winner"}}, nil
	}
	out, err := waitAndGetRecipeOutput(deps, celltest.Context(context.Background()), StartedJob{JobID: "original", GoalID: "g"})
	require.NoError(t, err)
	require.Equal(t, ResultSource{Goal: "g", Job: "winner"}, out.Result)
	joined, err := waitAndNoResult(deps, celltest.Context(context.Background()), StartedJobs{Items: []StartedJob{{JobID: "original", GoalID: "g"}}})
	require.NoError(t, err)
	require.Equal(t, "original", joined.Items[0].JobID)
	require.Equal(t, &out.Result, joined.Items[0].Result)
	for _, in := range []StartedJob{{GoalID: "g", Wait: WaitPolicy{Condition: "complete"}}, {Target: WaitTarget{Goal: "g"}, Wait: WaitPolicy{On: "job"}}, {Target: WaitTarget{Goal: "g", Job: "j"}}, {GoalID: "g", TenantID: "other"}, {GoalID: "g", Wait: WaitPolicy{On: "nonsense"}}} {
		_, err = awaitChild(deps, celltest.Context(context.Background()), in, false)
		require.Error(t, err)
	}
	tool.call = func(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) { return nil, context.DeadlineExceeded }
	_, err = awaitChild(deps, celltest.Context(context.Background()), StartedJob{GoalID: "g"}, true)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
func TestChildGroupCollectsAfterExplicitGoalFailure(t *testing.T) {
	failure := jobdb.GoalFailure{Code: "goal_failed", GoalID: "failed", Message: "exhausted", TerminalJobIDs: []string{"a"}}
	tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}}
	var seen []string
	tool.call = func(d ...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
		id := d[0].Target.GoalID
		seen = append(seen, id)
		if id == "failed" {
			return nil, &jobdb.GoalFailedError{Failure: failure}
		}
		return []jobdb.DependencyResolution{{Target: d[0].Target, State: jobdb.DependencySatisfied, SatisfiedByJobID: "replacement"}}, nil
	}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).Build()
	state, err := awaitChildGroup(deps, celltest.Context(context.Background()), ChildGroupStepState{Children: []ChildGroupChildRecord{{Status: "started", JobID: "a", GoalID: "failed", Required: false}, {Status: "started", JobID: "b", GoalID: "open", Required: true}}})
	require.NoError(t, err)
	require.Equal(t, []string{"failed", "open"}, seen)
	require.Equal(t, &failure, state.Children[0].GoalFailure)
	require.Equal(t, "replacement", state.Children[1].Result.Job)
}

// These reader fixtures model records retained from the attachment-capable era:
// A and B overlapped, goal G settled on B, and B's primary goal remained H. No
// live association is created, and collection must not reconstruct membership.
func TestHistoricalGoalCrossPrimaryProducerOutputAndArtifacts(t *testing.T) {
	ctx := celltest.Context(context.Background())
	key := jobdb.ArtifactKey{JobId: "B", TaskOrdinal: 9, Name: "result.txt", SizeBytes: 6}
	artifact := jobdb.NewArtifactFromBytes(key.Name, []byte("from B"))
	jobdb.AssignArtifactKey(artifact, key)
	producerGit := contextual.GitCommitContext{PersistHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ParentHash: "cccccccccccccccccccccccccccccccccccccccc"}
	parentGit := contextual.GitCommitContext{PersistHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	evidence := []jobdb.Artifact{artifact}
	expected := map[string]recipeartifacts.Ref{key.Name: recipeartifacts.NewStoredRef(key)}
	for _, name := range []string{gitstate.ThinPackArtifactName, "diff_from_parent.diff", "diff_from_base.diff", gitprofile.StderrArtifactName} {
		body := []byte("B evidence: " + name)
		a := jobdb.NewArtifactFromBytes(name, body)
		k := jobdb.ArtifactKey{JobId: "B", TaskOrdinal: 9, Name: name, SizeBytes: int64(len(body))}
		jobdb.AssignArtifactKey(a, k)
		evidence = append(evidence, a)
		expected[name] = recipeartifacts.NewStoredRef(k)
	}
	jobs := map[string]workflowctl.JobInspection{
		"A": {GoalID: "G", Status: string(jobdb.JobStatusReady)},
		"B": {GoalID: "H", OtherGoalIDs: []string{"G"}, Status: string(jobdb.JobStatusCompleted)},
	}
	goal := jobdb.GoalInfo{GoalKey: jobdb.GoalKey{TenantId: "t", GoalID: "G"}, State: jobdb.GoalSucceeded, SuccessfulJobID: "B"}
	binding := jobdb.DependencyResolution{Target: jobdb.DependencyTarget{GoalID: "G"}, State: jobdb.DependencySatisfied, SatisfiedByJobID: "B"}
	for _, explicit := range []bool{false, true} {
		for _, secondaries := range [][]string{{"G"}, nil} {
			jobs["B"] = workflowctl.JobInspection{GoalID: "H", OtherGoalIDs: secondaries, Status: string(jobdb.JobStatusCompleted)}
			// Both saved snapshots retain B's winning identity even after A later succeeds.
			for _, originalStatus := range []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusCompleted} {
				jobs["A"] = workflowctl.JobInspection{GoalID: "G", Status: string(originalStatus)}
				for _, route := range []string{"hard-wait", "soft-wait", "current-status", "explicit-result"} {
					t.Run(fmt.Sprintf("workspace-%t/secondary-%d/%s/%s", explicit, len(secondaries), originalStatus, route), func(t *testing.T) {
						tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}, call: func(deps ...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
							require.Equal(t, binding.Target, deps[0].Target)
							return []jobdb.DependencyResolution{binding}, nil
						}}
						ctl := &goalStatusControl{goal: goal}
						ctl.inspectFunc = func(_ context.Context, k jobdb.JobKey) (workflowctl.JobInspection, error) { return jobs[k.JobId], nil }
						ctl.jobResultFunc = func(_ context.Context, k jobdb.JobKey) (jobdb.JobData, error) {
							require.Equal(t, jobdb.JobKey{TenantId: "t", JobId: "B"}, k)
							require.Equal(t, "H", jobs[k.JobId].GoalID, "retain the historical producer's primary goal")
							return jobdb.NewTaskDataOrPanic(map[string]any{"output": map[string]any{"producer": k.JobId}, "git": producerGit}, evidence...), nil
						}
						git := ops.GitExecutionContext{CellName: "A", PersistHash: parentGit.PersistHash, ParentHash: parentGit.ParentHash}
						if explicit {
							git.Workspace = &contextual.WorkspaceContext{Cell: "parent", ScopeID: "workspace"}
						}
						deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).WithGitContext(git).Build()
						var result ResultSource
						var outputs map[string]interface{}
						var artifacts map[string]recipeartifacts.Ref
						if route == "hard-wait" || route == "explicit-result" {
							var out SingleRecipeOutput
							var err error
							if route == "hard-wait" {
								out, err = waitAndGetRecipeOutput(deps, ctx, StartedJob{JobID: "A", GoalID: "G"})
							} else {
								out, err = getRecipeOutput(deps, ctx, StartedJob{Target: WaitTarget{Goal: "G"}})
							}
							require.NoError(t, err)
							require.Equal(t, producerGit, out.GitResult)
							result, outputs, artifacts = out.Result, out.Outputs, out.Artifacts
						} else {
							when := "terminal"
							if route == "current-status" {
								when = "current_status"
							}
							out, err := awaitResultSoft(deps, ctx, AwaitResultSoftInput{Target: WaitTarget{Job: "A"}, ReturnWhen: when})
							require.NoError(t, err)
							result, outputs, artifacts = out.Result, out.Outputs, out.Artifacts
						}
						require.Equal(t, ResultSource{Goal: "G", Job: "B"}, result)
						require.Equal(t, "B", outputs["producer"])
						require.Equal(t, expected, artifacts)
						require.Equal(t, git, deps.GitContext(), "historical B must not replace A or workspace provenance")
						forwarded := deps.GetOutputArtifacts()
						require.Len(t, forwarded, 1, "B Git evidence must not replace the awaiting task snapshot")
						bytes, err := forwarded[0].Bytes(ctx)
						require.NoError(t, err)
						require.Equal(t, "from B", string(bytes))
						// Reader replay retains the complete saved record and exact binding.
						replayed, err := ctl.GetGoal(ctx, goal.GoalKey)
						require.NoError(t, err)
						require.Equal(t, goal, replayed)
						resolved, err := tool.AwaitDependencies(jobdb.Dependency{Target: binding.Target, Condition: jobdb.JobPrereqSuccess})
						require.NoError(t, err)
						require.Equal(t, []jobdb.DependencyResolution{binding}, resolved)
					})
				}
			}
		}
	}
}

func TestHistoricalGoalProducerReadErrorsDoNotChooseAnotherJob(t *testing.T) {
	for _, mode := range []string{"missing", "denied", "data", "malformed", "artifacts"} {
		for _, route := range []string{"hard", "soft", "current", "explicit"} {
			t.Run(mode+"/"+route, func(t *testing.T) {
				cause := fmt.Errorf("producer B: %w", jobdb.ErrJobNotFound)
				if mode == "denied" {
					cause = fmt.Errorf("producer B: %w", jobdb.ErrLeasePermissionDenied)
				}
				ctl := &goalStatusControl{goal: jobdb.GoalInfo{GoalKey: jobdb.GoalKey{TenantId: "t", GoalID: "G"}, State: jobdb.GoalSucceeded, SuccessfulJobID: "B"}}
				ctl.inspectFunc = func(_ context.Context, k jobdb.JobKey) (workflowctl.JobInspection, error) {
					return workflowctl.JobInspection{GoalID: "G", Status: string(jobdb.JobStatusCompleted)}, nil
				}
				ctl.jobResultFunc = func(_ context.Context, k jobdb.JobKey) (jobdb.JobData, error) {
					require.Equal(t, "B", k.JobId, "never fall back to the original job A")
					switch mode {
					case "missing", "denied":
						return nil, cause
					case "data":
						return &errorTaskData{dataErr: cause}, nil
					case "malformed":
						return &errorTaskData{data: []byte("not json")}, nil
					default:
						return &errorTaskData{data: []byte(`{"output":{}}`), artifactsErr: cause}, nil
					}
				}
				tool := &bindingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "t"}}, call: func(...jobdb.Dependency) ([]jobdb.DependencyResolution, error) {
					return []jobdb.DependencyResolution{{Target: jobdb.DependencyTarget{GoalID: "G"}, State: jobdb.DependencySatisfied, SatisfiedByJobID: "B"}}, nil
				}}
				deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).Build()
				var err error
				switch route {
				case "hard":
					_, err = waitAndGetRecipeOutput(deps, celltest.Context(context.Background()), StartedJob{JobID: "A", GoalID: "G"})
				case "explicit":
					_, err = getRecipeOutput(deps, celltest.Context(context.Background()), StartedJob{Target: WaitTarget{Goal: "G"}})
				default:
					when := "terminal"
					if route == "current" {
						when = "current_status"
					}
					_, err = awaitResultSoft(deps, celltest.Context(context.Background()), AwaitResultSoftInput{Target: WaitTarget{Goal: "G"}, ReturnWhen: when})
				}
				if mode == "malformed" {
					require.Error(t, err)
				} else {
					require.ErrorIs(t, err, cause)
				}
				require.Empty(t, deps.GetOutputArtifacts())
			})
		}
	}
}
