package recipetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/colony-2/c2j/internal/dispatchtest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestNativeDispatchBeforeAuthorization(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "permission denied retained", true: "strict denial before callback"}[strict], func(t *testing.T) {
			c := controlCase()
			c.Runtime.Controls.Dispatch = &DispatchPolicy{Strict: strict}
			f := &runtimeFixture{c: c}
			f.initControls()
			ctx := dispatchtest.WithObserver(context.Background(), f.beginDispatch)
			ctx = dispatchtest.WithIdentity(ctx, dispatchtest.Attempt{JobID: "native-job", InvocationID: "native-invocation", Attempt: 2, ResultOrdinal: 7, NodePath: "controls/child"})
			called := false
			access := permissions.NewAccess(ctx, permissions.JobDB{}, nil, nil, func(context.Context, jobdb.CreateCellRequest) (jobdb.Cell, error) {
				called = true
				return jobdb.Cell{}, nil
			})
			_, err := access.CreateCell(ctx, jobdb.CreateCellRequest{Name: "forbidden-child"})
			require.Error(t, err)
			require.False(t, called)
			require.Len(t, f.report.Controls.Dispatch, 1)
			record := f.report.Controls.Dispatch[0]
			require.Equal(t, "create_cell", record.Name)
			require.Equal(t, "native-invocation", record.InvocationID)
			require.Equal(t, 2, record.Attempt.Attempt)
			require.True(t, record.Denied)
			require.True(t, record.Finished)
			require.Equal(t, !strict, record.Allowed)
		})
	}
}

func TestDispatchEvidenceLimitFailsClosed(t *testing.T) {
	c := controlCase()
	c.Runtime.Controls.Dispatch = &DispatchPolicy{MaxAttempts: 1}
	f := &runtimeFixture{c: c}
	f.initControls()
	finish, err := f.beginDispatch(context.Background(), dispatchtest.Attempt{Kind: "task", Name: "one"})
	require.NoError(t, err)
	finish(nil)
	_, err = f.beginDispatch(context.Background(), dispatchtest.Attempt{Kind: "task", Name: "two"})
	require.ErrorContains(t, err, "incomplete")
	result := CaseRunResult{Status: "passed"}
	f.finishControls(&result)
	require.Equal(t, "failed", result.Status)
	require.Equal(t, 1, f.report.Controls.DispatchDropped)
	require.False(t, f.report.Controls.DispatchComplete)
}

func TestRuntimeForbiddenTaskBeforeBody(t *testing.T) {
	withRegisteredCoreOps(t)
	c := controlCase()
	c.Runtime.Controls.Dispatch = &DispatchPolicy{Strict: true}
	target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: controls\nsequence:\n- id: answer\n  op: ./echo\n"}
	result := RunCase(context.Background(), HarnessOptions{WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "forbidden-task", target, c, ExecutionOptions{Timeout: "5s"})
	require.Equal(t, "failed", result.Status)
	require.NotNil(t, result.Runtime.Controls)
	require.NotEmpty(t, result.Runtime.Controls.Dispatch)
	require.True(t, result.Runtime.Controls.Dispatch[0].Denied)
	require.Empty(t, result.Runtime.Calls)
}

func TestRuntimeDeniedNativeSubmission(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			submit := coreops.NewActivityMappedOpV2[neutralContinueInput, neutralContinueInput](coreops.OpMetadata{Type: "neutral_submit"}, func(deps coreops.OpDependencies, ctx context.Context, in neutralContinueInput) (neutralContinueInput, error) {
				tool, ok := deps.JobTool().(*coreops.TaskBasedJobTool)
				if !ok {
					return in, fmt.Errorf("native task tool unavailable")
				}
				_, err := tool.SubmitJobRef(ctx, jobdb.SubmitJob{Cell: "forbidden-child"})
				return in, err
			})
			withRegisteredCoreOps(t, extensions.GetExecutionOp(), submit)
			target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: controls\nsequence:\n- id: answer\n  op: ./echo\n- id: child\n  op: neutral_submit\n  perms: {jobdb: {}}\noutputs: {}\n"}
			c := controlCase()
			c.Runtime.Controls.Dispatch.Allow = append(c.Runtime.Controls.Dispatch.Allow, DispatchMatcher{Kind: "task", Name: "neutral_submit:neutral_submit", NodePath: "controls/child"})
			if !strict {
				c.Runtime.Controls.Dispatch.Allow = append(c.Runtime.Controls.Dispatch.Allow, DispatchMatcher{Kind: "native", Name: "submit_job", Cell: "forbidden-child", NodePath: "controls/child"})
			}
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: t.TempDir()}, "native-denied", target, c, ExecutionOptions{Timeout: "5s"})
			require.Equal(t, "failed", result.Status)
			records := result.Runtime.Controls.Dispatch
			require.NotEmpty(t, records)
			last := records[len(records)-1]
			require.Equal(t, "submit_job", last.Name)
			require.Equal(t, !strict, last.Allowed)
			require.Equal(t, "controls/child", last.NodePath)
			require.True(t, last.Denied)
			require.True(t, last.Finished)
			require.NotEmpty(t, last.JobID)
			require.NotEmpty(t, last.InvocationID)
			require.Positive(t, last.Attempt.Attempt)
			require.Positive(t, last.ResultOrdinal)
			require.Empty(t, result.Runtime.Controls.FinalSnapshot.Children)
		})
	}
}
