package jobdbschema

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type submissionNoReads struct {
	jobdb.WorkflowRuntime
	jobdb.JobSchemaRegistry
	reads int
}

func (r *submissionNoReads) GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error) {
	r.reads++
	panic("submission must preserve the response without identity lookup")
}
func TestSubmitReferencePreservesIdentityWithoutRead(t *testing.T) {
	raw := toy.New()
	rt := &submissionNoReads{WorkflowRuntime: raw, JobSchemaRegistry: raw}
	sdk, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	engine := WorkflowEngine{Engine: sdk, Registry: rt}
	request := jobdb.SubmitJob{TenantId: "t", JobID: "stable-child", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(map[string]any{"cell": "test", "tenantId": "t", "recipe": "test", "context": map[string]any{}}), Cell: "test", CellRepo: "example.test/test"}
	celltest.Create(t, engine, request.TenantId, request.Cell, request.CellRepo)
	first, err := engine.SubmitJobRef(celltest.Context(context.Background()), request)
	require.NoError(t, err)
	require.NotEmpty(t, first.GoalID)
	require.Equal(t, "stable-child", first.JobID)
	require.Equal(t, "t", first.TenantID)
	celltest.Create(t, engine, request.TenantId, request.Cell, request.CellRepo)
	retry, err := engine.SubmitJobRef(celltest.Context(context.Background()), request)
	require.NoError(t, err)
	require.Equal(t, first, retry)
	require.Zero(t, rt.reads)
}

// Only the schema contract is embedded. In particular this base does not expose
// WorkflowRuntime (which would silently add attachment and lifecycle methods).
type goalSchemaOnly struct{ jobdb.JobSchemaRegistry }

type onlyGetGoal struct {
	goalSchemaOnly
	call func(context.Context, jobdb.GoalKey) (jobdb.GoalInfo, error)
}

func (r onlyGetGoal) GetGoal(ctx context.Context, req jobdb.GoalKey) (jobdb.GoalInfo, error) {
	return r.call(ctx, req)
}

type onlyGetDependencyWait struct {
	goalSchemaOnly
	call func(context.Context, jobdb.DependencyWaitKey) (jobdb.DependencyWaitInfo, error)
}

func (r onlyGetDependencyWait) GetDependencyWait(ctx context.Context, req jobdb.DependencyWaitKey) (jobdb.DependencyWaitInfo, error) {
	return r.call(ctx, req)
}

type onlyFailGoal struct {
	goalSchemaOnly
	call func(context.Context, jobdb.FailGoalRequest) (jobdb.GoalInfo, error)
}

func (r onlyFailGoal) FailGoal(ctx context.Context, req jobdb.FailGoalRequest) (jobdb.GoalInfo, error) {
	return r.call(ctx, req)
}

type onlyListGoals struct {
	goalSchemaOnly
	call func(context.Context, jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error)
}

func (r onlyListGoals) ListGoals(ctx context.Context, req jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error) {
	return r.call(ctx, req)
}

type onlyListDependencyWaits struct {
	goalSchemaOnly
	call func(context.Context, jobdb.ListDependencyWaitsRequest) (jobdb.ListDependencyWaitsResponse, error)
}

func (r onlyListDependencyWaits) ListDependencyWaits(ctx context.Context, req jobdb.ListDependencyWaitsRequest) (jobdb.ListDependencyWaitsResponse, error) {
	return r.call(ctx, req)
}

func TestGoalForwardingIndividualCapabilities(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "preserve context")
	base := goalSchemaOnly{}
	_, broad := any(base).(jobdb.GoalRuntime)
	require.False(t, broad)
	// Reflection catches accidental addition of a removed forwarding method.
	require.NotContains(t, methodNames(WorkflowEngine{}), "AddGoalCandidate")
	backendError := errors.New("backend failed")

	t.Run("GetGoal", func(t *testing.T) {
		req := jobdb.GoalKey{TenantId: "t", GoalID: "g"}
		want := jobdb.GoalInfo{GoalKey: jobdb.GoalKey{TenantId: "t", GoalID: "g"}, State: jobdb.GoalOpen}
		var failure error
		calls := 0
		registry := onlyGetGoal{call: func(gotCtx context.Context, got jobdb.GoalKey) (jobdb.GoalInfo, error) {
			calls++
			require.Equal(t, ctx, gotCtx)
			require.Equal(t, req, got)
			return want, failure
		}}
		_, broad := any(registry).(jobdb.GoalRuntime)
		require.False(t, broad)
		require.NotContains(t, methodNames(registry), "AddGoalCandidate")
		engine := WorkflowEngine{Registry: registry}
		got, err := engine.GetGoal(ctx, req)
		require.NoError(t, err)
		require.Equal(t, want, got)
		failure = backendError
		_, err = engine.GetGoal(ctx, req)
		require.Same(t, backendError, err)
		require.Equal(t, 2, calls)
		_, err = (WorkflowEngine{Registry: base}).GetGoal(ctx, req)
		require.EqualError(t, err, "goal runtime unavailable")
	})

	t.Run("GetDependencyWait", func(t *testing.T) {
		req := jobdb.DependencyWaitKey{JobKey: jobdb.JobKey{TenantId: "t", JobId: "consumer"}, WaitID: "w"}
		want := jobdb.DependencyWaitInfo{WaitID: "w", State: jobdb.DependencyPending}
		var failure error
		calls := 0
		registry := onlyGetDependencyWait{call: func(gotCtx context.Context, got jobdb.DependencyWaitKey) (jobdb.DependencyWaitInfo, error) {
			calls++
			require.Equal(t, ctx, gotCtx)
			require.Equal(t, req, got)
			return want, failure
		}}
		_, broad := any(registry).(jobdb.GoalRuntime)
		require.False(t, broad)
		require.NotContains(t, methodNames(registry), "AddGoalCandidate")
		engine := WorkflowEngine{Registry: registry}
		got, err := engine.GetDependencyWait(ctx, req)
		require.NoError(t, err)
		require.Equal(t, want, got)
		failure = backendError
		_, err = engine.GetDependencyWait(ctx, req)
		require.Same(t, backendError, err)
		require.Equal(t, 2, calls)
		_, err = (WorkflowEngine{Registry: base}).GetDependencyWait(ctx, req)
		require.EqualError(t, err, "goal runtime unavailable")
	})

	t.Run("FailGoal", func(t *testing.T) {
		req := jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "t", GoalID: "g"}, Message: " café\n"}
		want := jobdb.GoalInfo{GoalKey: jobdb.GoalKey{TenantId: "t", GoalID: "g"}, State: jobdb.GoalFailed}
		var failure error
		calls := 0
		registry := onlyFailGoal{call: func(gotCtx context.Context, got jobdb.FailGoalRequest) (jobdb.GoalInfo, error) {
			calls++
			require.Equal(t, ctx, gotCtx)
			require.Equal(t, req, got)
			return want, failure
		}}
		_, broad := any(registry).(jobdb.GoalRuntime)
		require.False(t, broad)
		require.NotContains(t, methodNames(registry), "AddGoalCandidate")
		engine := WorkflowEngine{Registry: registry}
		got, err := engine.FailGoal(ctx, req)
		require.NoError(t, err)
		require.Equal(t, want, got)
		failure = backendError
		_, err = engine.FailGoal(ctx, req)
		require.Same(t, backendError, err)
		require.Equal(t, 2, calls)
		_, err = (WorkflowEngine{Registry: base}).FailGoal(ctx, req)
		require.EqualError(t, err, "goal runtime unavailable")
	})

	t.Run("ListGoals", func(t *testing.T) {
		req := jobdb.ListGoalsRequest{TenantId: "t", PageToken: "cursor"}
		want := jobdb.ListGoalsResponse{NextPageToken: "next"}
		var failure error
		calls := 0
		registry := onlyListGoals{call: func(gotCtx context.Context, got jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error) {
			calls++
			require.Equal(t, ctx, gotCtx)
			require.Equal(t, req, got)
			return want, failure
		}}
		_, broad := any(registry).(jobdb.GoalRuntime)
		require.False(t, broad)
		require.NotContains(t, methodNames(registry), "AddGoalCandidate")
		engine := WorkflowEngine{Registry: registry}
		got, err := engine.ListGoals(ctx, req)
		require.NoError(t, err)
		require.Equal(t, want, got)
		failure = backendError
		_, err = engine.ListGoals(ctx, req)
		require.Same(t, backendError, err)
		require.Equal(t, 2, calls)
		_, err = (WorkflowEngine{Registry: base}).ListGoals(ctx, req)
		require.EqualError(t, err, "goal runtime unavailable")
	})

	t.Run("ListDependencyWaits", func(t *testing.T) {
		req := jobdb.ListDependencyWaitsRequest{TenantId: "t", ConsumerJobID: "consumer"}
		want := jobdb.ListDependencyWaitsResponse{NextPageToken: "next"}
		var failure error
		calls := 0
		registry := onlyListDependencyWaits{call: func(gotCtx context.Context, got jobdb.ListDependencyWaitsRequest) (jobdb.ListDependencyWaitsResponse, error) {
			calls++
			require.Equal(t, ctx, gotCtx)
			require.Equal(t, req, got)
			return want, failure
		}}
		_, broad := any(registry).(jobdb.GoalRuntime)
		require.False(t, broad)
		require.NotContains(t, methodNames(registry), "AddGoalCandidate")
		engine := WorkflowEngine{Registry: registry}
		got, err := engine.ListDependencyWaits(ctx, req)
		require.NoError(t, err)
		require.Equal(t, want, got)
		failure = backendError
		_, err = engine.ListDependencyWaits(ctx, req)
		require.Same(t, backendError, err)
		require.Equal(t, 2, calls)
		_, err = (WorkflowEngine{Registry: base}).ListDependencyWaits(ctx, req)
		require.EqualError(t, err, "goal runtime unavailable")
	})
}

func methodNames(v any) []string {
	var names []string
	typ := reflect.TypeOf(v)
	for i := 0; i < typ.NumMethod(); i++ {
		names = append(names, typ.Method(i).Name)
	}
	return names
}

type onlyCrashConcern struct {
	goalSchemaOnly
	call func(context.Context, jobdb.ClearCrashConcernRequest) error
}

func (r onlyCrashConcern) ClearCrashConcern(ctx context.Context, req jobdb.ClearCrashConcernRequest) error {
	return r.call(ctx, req)
}
func TestGoalCleanupPreservesCrashConcernForwarding(t *testing.T) {
	ctx := context.Background()
	// Use the request's validation to establish a valid administrative request.
	req := jobdb.ClearCrashConcernRequest{JobKey: jobdb.JobKey{TenantId: "t", JobId: "j"}}
	want := errors.New("backend crash concern")
	engine := WorkflowEngine{Registry: onlyCrashConcern{call: func(got context.Context, request jobdb.ClearCrashConcernRequest) error {
		require.Equal(t, ctx, got)
		require.Equal(t, req, request)
		return want
	}}}
	require.Same(t, want, engine.ClearCrashConcern(ctx, req))
	require.EqualError(t, (WorkflowEngine{Registry: goalSchemaOnly{}}).ClearCrashConcern(ctx, req), "crash concern administration unavailable")
}
