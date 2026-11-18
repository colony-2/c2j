package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/grouping"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type groupingTool struct {
	fakeJobTool
	requests []jobdb.StartOrAppendJob
	fail     error
}

func (t *groupingTool) StartOrAppendJobRef(_ context.Context, req jobdb.StartOrAppendJob) (jobref.Ref, error) {
	t.requests = append(t.requests, req)
	return jobref.Ref{TenantID: "tenant", JobID: "shared", GoalID: "goal", Cell: "test", CellID: "immutable", CellRepo: "example.test/test"}, t.fail
}
func groupedSpec() grouping.Spec {
	return grouping.Spec{Present: true, Value: map[string]any{"key": " literal ", "content": map[string]any{}}}
}
func TestGroupedOperationRoutingAndWaits(t *testing.T) {
	ctx := celltest.Context(context.Background())
	tool := &groupingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "tenant", JobId: "parent"}}}
	ctl := &fakeWorkflowControl{}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).Build()
	recipe := SingleRecipe{Name: "batch", CellName: "test", Grouping: groupedSpec()}
	one, err := startSingleJob(deps, ctx, SingleRecipeWithRef{SingleRecipe: recipe, GitRef: "main"})
	require.NoError(t, err)
	require.Equal(t, WaitPolicy{On: "job", Condition: "success"}, one.Wait)
	require.Equal(t, "immutable", one.CellID)
	many, err := startMultipleJobs(deps, ctx, MultipleRecipes{GitRef: "main", Recipes: []SingleRecipe{recipe, {Name: "ordinary", CellName: "test"}}})
	require.NoError(t, err)
	require.Equal(t, WaitPolicy{On: "job", Condition: "complete"}, many.Items[0].Wait)
	require.Equal(t, WaitPolicy{On: "goal", Condition: "success"}, many.Items[1].Wait)
	many, err = startMultipleJobs(deps, ctx, MultipleRecipes{GitRef: "main", Wait: WaitPolicy{On: "goal"}, Recipes: []SingleRecipe{recipe}})
	require.NoError(t, err)
	require.Equal(t, "goal", many.Items[0].Wait.On)
	require.Len(t, ctl.startRequests, 1)
	require.Len(t, tool.requests, 3)
	q := tool.requests[0]
	require.Empty(t, q.Job.JobID)
	require.Empty(t, q.Job.GoalID)
	require.Empty(t, q.Job.Dependencies)
	require.Equal(t, " literal ", q.AppendKey)
	require.Equal(t, 50, q.MaxInputs)
	var start workflowctl.StartJob
	require.NoError(t, json.Unmarshal(q.Job.Data.GetDataOrPanic(), &start))
	require.Nil(t, start.Parent)
	var meta map[string]any
	require.NoError(t, json.Unmarshal(q.Job.Metadata, &meta))
	require.NotContains(t, meta, "parent_job_id")
}

func TestMultipleRecipeWaitInheritanceBeforeNormalization(t *testing.T) {
	jobComplete := WaitPolicy{On: "job", Condition: "complete"}
	jobSuccess := WaitPolicy{On: "job", Condition: "success"}
	goalSuccess := WaitPolicy{On: "goal", Condition: "success"}
	grouped := SingleRecipe{Name: "batch", CellName: "test", Grouping: groupedSpec()}
	ordinary := SingleRecipe{Name: "ordinary", CellName: "test"}
	withWait := func(child SingleRecipe, wait WaitPolicy) SingleRecipe {
		child.Wait = wait
		return child
	}
	for _, tc := range []struct {
		name     string
		wait     WaitPolicy
		children []SingleRecipe
		want     []WaitPolicy
		err      string
	}{
		{name: "inherited completion on grouped entries", wait: WaitPolicy{Condition: "complete"}, children: []SingleRecipe{grouped, grouped}, want: []WaitPolicy{jobComplete, jobComplete}},
		{name: "entry completion on grouped entry", children: []SingleRecipe{withWait(grouped, WaitPolicy{Condition: "complete"})}, want: []WaitPolicy{jobComplete}},
		{name: "mixed entries resolve inherited completion", wait: WaitPolicy{Condition: "complete"}, children: []SingleRecipe{grouped, withWait(ordinary, WaitPolicy{On: "job"})}, want: []WaitPolicy{jobComplete, jobComplete}},
		{name: "entry success overrides inherited completion", wait: WaitPolicy{Condition: "complete"}, children: []SingleRecipe{withWait(grouped, WaitPolicy{Condition: "success"})}, want: []WaitPolicy{jobSuccess}},
		{name: "entry goal resets inherited condition", wait: jobComplete, children: []SingleRecipe{withWait(grouped, WaitPolicy{On: "goal"})}, want: []WaitPolicy{goalSuccess}},
		{name: "entry job overrides inherited goal", wait: WaitPolicy{On: "goal"}, children: []SingleRecipe{withWait(grouped, jobComplete)}, want: []WaitPolicy{jobComplete}},
		{name: "mixed defaults stay distinct", children: []SingleRecipe{grouped, ordinary}, want: []WaitPolicy{jobComplete, goalSuccess}},
		{name: "ordinary inherited completion remains invalid", wait: WaitPolicy{Condition: "complete"}, children: []SingleRecipe{grouped, ordinary}, err: "goal and result waits require success"},
		{name: "explicit goal completion remains invalid", wait: WaitPolicy{On: "goal", Condition: "complete"}, children: []SingleRecipe{grouped}, err: "goal and result waits require success"},
		{name: "invalid inherited target", wait: WaitPolicy{On: "invalid"}, children: []SingleRecipe{grouped}, err: "wait.on must be goal or job"},
		{name: "invalid inherited condition", wait: WaitPolicy{Condition: "invalid"}, children: []SingleRecipe{grouped}, err: "wait.condition must be success or complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &groupingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "tenant", JobId: "parent"}}}
			ctl := &fakeWorkflowControl{}
			deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).Build()
			out, err := startMultipleJobs(deps, celltest.Context(context.Background()), MultipleRecipes{GitRef: "main", Wait: tc.wait, Recipes: tc.children})
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				require.Empty(t, tool.requests, "validate every wait before submitting any entry")
				require.Empty(t, ctl.startRequests)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wait, out.Wait, "retain the declared inheritance policy; effective policies belong to items")
			require.Len(t, out.Items, len(tc.want))
			for i, wait := range tc.want {
				require.Equal(t, wait, out.Items[i].Wait)
			}
		})
	}
}

func TestGroupingPresenceSurvivesOperationDecoding(t *testing.T) {
	for _, raw := range []any{nil, 1, []any{}} {
		var input SingleRecipeWithRef
		require.NoError(t, ops.DecodeWithJsonTags(map[string]any{"name": "batch", "grouping": raw, "git_ref": "main"}, &input))
		require.True(t, input.Grouping.Present)
		_, err := input.Grouping.Prepare()
		require.Error(t, err)
	}
	var input SingleRecipeWithRef
	require.NoError(t, ops.DecodeWithJsonTags(map[string]any{"name": "batch", "git_ref": "main"}, &input))
	require.False(t, input.Grouping.Present)
	var many MultipleRecipes
	require.NoError(t, ops.DecodeWithJsonTags(map[string]any{"git_ref": "main", "recipes": []any{map[string]any{"name": "batch", "grouping": nil}}}, &many))
	require.True(t, many.Recipes[0].Grouping.Present)
	var invalid SingleRecipeWithRef
	require.Error(t, ops.DecodeWithJsonTags(map[string]any{"grouping": map[string]any{"key": string([]byte{0xff}), "content": map[string]any{}}}, &invalid))
}
func TestGroupedMissingOrDeniedMethodNeverFallsBack(t *testing.T) {
	ctx := celltest.Context(context.Background())
	ctl := &fakeWorkflowControl{}
	for _, tool := range []ops.JobTool{&fakeJobTool{}, &groupingTool{fail: jobdb.ErrLeasePermissionDenied}} {
		deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(ctl).Build()
		_, err := startSingleJob(deps, ctx, SingleRecipeWithRef{SingleRecipe: SingleRecipe{Name: "batch", CellName: "test", Grouping: groupedSpec()}})
		require.Error(t, err)
		if _, ok := tool.(*groupingTool); ok {
			require.True(t, errors.Is(err, jobdb.ErrLeasePermissionDenied))
		}
	}
	require.Empty(t, ctl.startRequests)
}

func TestGroupedChildRecordsRetainContributionKeys(t *testing.T) {
	tool := &groupingTool{fakeJobTool: fakeJobTool{key: jobdb.JobKey{TenantId: "tenant", JobId: "parent"}}}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithWorkflowControl(&fakeWorkflowControl{}).Build()
	state, err := startChildGroup(deps, context.Background(), ChildGroupStartInput{Children: []ChildGroupChildSpec{
		{Key: "a", Recipe: "batch", CellName: "test", Required: true, Grouping: groupedSpec()},
		{Key: "b", Recipe: "batch", CellName: "test", Required: true, Grouping: groupedSpec()},
		{Key: "skip", Recipe: "batch", Skipped: true, Grouping: grouping.Spec{Present: true}},
	}})
	require.NoError(t, err)
	require.Len(t, tool.requests, 2)
	require.Len(t, state.Children, 3)
	require.Equal(t, state.Children[0].JobID, state.Children[1].JobID)
	require.Equal(t, "a", state.Children[0].Key)
	require.Equal(t, "b", state.Children[1].Key)
	require.Equal(t, WaitPolicy{On: "job", Condition: "success"}, state.Children[0].Wait)
	require.Equal(t, "skipped", state.Children[2].Status)
}

func TestGroupedPartialFanoutRetainsObservedHandles(t *testing.T) {
	ctx := context.Background()
	calls := 0
	access := permissions.NewAccess(ctx, permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}}, nil, nil).WithAppend(func(context.Context, jobdb.StartOrAppendJob) (jobref.Ref, error) {
		calls++
		if calls == 2 {
			return jobref.Ref{}, jobdb.ErrLeasePermissionDenied
		}
		return jobref.Ref{TenantID: "tenant", JobID: "shared", GoalID: "goal", Cell: "test", CellID: "cell", CellRepo: "example.test/test"}, nil
	})
	tool := &ops.TaskBasedJobTool{TaskContext: wf.TaskContext{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "parent"}, InvocationID: "call"}, Restricted: access}
	deps := ops.NewOpDependenciesBuilder().WithJobTool(tool).WithCurrentJobContext(jobcontext.Current{TenantID: "tenant", JobID: "parent"}).Build()
	child := SingleRecipe{Name: "batch", CellName: "test", Grouping: groupedSpec()}
	_, err := startMultipleJobs(deps, ctx, MultipleRecipes{Recipes: []SingleRecipe{child, child}})
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	require.Len(t, tool.StartedJobs().Items, 1)
	require.Equal(t, "shared", tool.StartedJobs().Items[0].JobID)
	require.Empty(t, tool.StartedJobs().Items[0].ParentInvocationHash)
}
