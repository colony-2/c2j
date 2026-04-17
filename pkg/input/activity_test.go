package input

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/core/ops"
	"github.com/colony-2/c2j/pkg/core/recipe"
	"github.com/colony-2/c2j/pkg/core/starter"
	"github.com/colony-2/c2j/pkg/core/swfutil"
	"github.com/colony-2/c2j/pkg/core/workflowctl"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
	directruntime "github.com/colony-2/swf-go/pkg/swf/runtime/direct"
	directtestsupport "github.com/colony-2/swf-go/pkg/swf/runtime/direct/testsupport"
	toyruntime "github.com/colony-2/swf-go/pkg/swf/runtime/toy"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type gen struct {
	count int
	max   int
}

func (g *gen) Generate(tenantId string) (swf.JobKey, error) {
	g.count++
	if g.count > g.max {
		return swf.JobKey{}, fmt.Errorf("too many jobs")
	}
	return swf.JobKey{TenantId: tenantId, JobId: fmt.Sprintf("job-%d", g.count)}, nil
}

func newToyEngine(t *testing.T, gen func(string) (swf.JobKey, error)) swf.SWFEngine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := make([]toyruntime.Option, 0, 1)
	if gen != nil {
		opts = append(opts, toyruntime.WithJobIDGenerator(gen))
	}
	eng, err := swf.NewEngineBuilder().
		WithRuntime(toyruntime.New(opts...)).
		BuildEngine()
	require.NoError(t, err)
	go eng.Run(ctx)
	return eng
}

type EchoIn struct {
	Message string `json:"message"`
}

type EchoOut struct {
	Output string `json:"output"`
}

func TestSimpleInput(t *testing.T) {
	coreops.Register(coreops.NewActivityMappedOpV2[EchoIn, EchoOut](
		coreops.OpMetadata{Type: "echo"},
		func(_ coreops.OpDependencies, _ context.Context, input EchoIn) (EchoOut, error) {
			message := input.Message
			return EchoOut{
				Output: message,
			}, nil
		},
	))

	op := GetOp()
	opR := op.GetManagementService().(*inputManagementService)
	coreops.Register(op)
	recipeYaml := `
---
id: testrecipe

input_schema:
  prompt:
    type: string
    default_value: Hello
inputs: 
  p1: "{{ inputs.prompt }}"
sequence:
  - op: input
    id: q1
    inputs: 
      form:
        question: "{{ inputs.p1 }}"
  - op: echo
    inputs: 
      message: q1.outputs.response
outputs:
  r2: "{{ sequence.q1.outputs.response }}"
`

	testRecipe, err := recipe.LoadRecipeFromString([]byte(recipeYaml))
	require.NoError(t, err)

	registry, err := ops.NewActivityRegistry()
	require.NoError(t, err)
	g := gen{max: 1}
	eng := newToyEngine(t, g.Generate)

	wf := workflow.SWFWorkflowControl{
		Engine: eng,
	}
	deps := coreops.NewServiceDepsBuilder().
		WithWorkflowControl(&wf).
		WithSSEManager(NewSimpleSSEManager()).
		Build()
	require.NoError(t, op.GetManagementService().Initialize(deps))

	workSet, err := compiler.NewRecipeWorker(deps, registry, nil)
	require.NoError(t, eng.RegisterWorkers(workSet))
	jobCtx, gitCtx := compiler.GenerateTestContext()
	in := map[string]interface{}{
		"prompt": "how old are you",
	}

	job := workflowctl.StartJob{
		TenantId:   "test-tenant",
		RecipeName: testRecipe.GetMetadata().ID,
		Inputs:     in,
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	errCh := make(chan error)
	go func() {
		_, err := starter.StartRecipeJob(context.Background(), job, eng, *testRecipe)
		errCh <- err
	}()

	// Wait for job to reach Ready status
	var inputs []PendingInput
	for i := 0; i < 2000; i++ {
		time.Sleep(100 * time.Millisecond)
		inputs, err = opR.collectPendingInputs(context.Background(), "test-tenant")
		require.NoError(t, err)
		if len(inputs) > 0 {
			break
		}
	}
	require.Equal(t, 1, len(inputs), "Expected 1 pending input after waiting")
	pending := inputs[0]

	result := opR.getDetails(context.Background(), "test-tenant", pending.Id)
	if result.hasError() {
		t.Fatalf("failed to get result: %v", result.err)
	}
	details := result.value
	require.NotNil(t, details.Form.Question)
	require.Equal(t, "how old are you", *details.Form.Question)
	resp := any("foolish")
	hash := "abc123"
	res2 := opR.submitResponse(context.Background(), "test-tenant", pending.Id, FormResponse{
		Fields:   map[string]interface{}{},
		Response: &resp,
		Hash:     &hash,
	})
	if res2.hasError() {
		t.Fatalf("failed to submit response: %v", res2.err)
	}
	err = <-errCh
	require.NoError(t, err)
	require.NoError(t, swf.WaitForJobToComplete(context.Background(), 5*time.Second, swf.JobKey{
		TenantId: "test-tenant",
		JobId:    "job-1",
	}, eng))

	res3, err := swfutil.JobResult(context.Background(), eng, swf.JobKey{TenantId: "test-tenant", JobId: "job-1"})
	require.NoError(t, err)

	res4, err := res3.GetData()
	require.NoError(t, err)

	air := make(map[string]interface{})
	require.NoError(t, json.Unmarshal(res4, &air))

	require.Equal(t, "foolish", air["r2"])

}

func TestSimpleInputRealEngineWaitRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	op := GetOp()
	opR := op.GetManagementService().(*inputManagementService)
	coreops.Register(op)
	recipeYaml := `
---
id: test-recipe

input_schema:
  prompt:
    type: string
    default_value: Hello
op: input
inputs:
  form:
    question: "{{ inputs.prompt }}"
`

	testRecipe, err := recipe.LoadRecipeFromString([]byte(recipeYaml))
	require.NoError(t, err)

	registry, err := ops.NewActivityRegistry()
	require.NoError(t, err)
	workerDeps := coreops.NewServiceDepsBuilder().Build()
	workSet, err := compiler.NewRecipeWorker(workerDeps, registry, nil)
	require.NoError(t, err)

	dsn, stopPG, err := directtestsupport.StartEmbeddedPostgres()
	require.NoError(t, err)
	defer stopPG()

	sqlDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, directtestsupport.InstallPGWF(ctx, sqlDB))

	strata, err := directtestsupport.StartEmbeddedStrata()
	require.NoError(t, err)
	defer strata.Shutdown()

	taskWorkers := make([]swf.TaskWorker, 0, len(workSet.TaskWorkers))
	for _, tw := range workSet.TaskWorkers {
		taskWorkers = append(taskWorkers, tw)
	}
	swfRuntime, err := directruntime.NewFromConfig(dsn, strata.BaseURL, strata.APIKey)
	require.NoError(t, err)
	engine, err := swf.NewEngineBuilder().
		WithRuntime(swfRuntime).
		WithAwaitRecycleThreshold(5*time.Second).
		WithLogger(slog.Default()).
		WithMaxActive(100).
		PlusWorkers(workSet.JobWorker, taskWorkers...).
		BuildEngine()
	require.NoError(t, err)
	go engine.Run(ctx)

	wf := workflow.SWFWorkflowControl{
		Engine: engine,
	}
	deps := coreops.NewServiceDepsBuilder().
		WithWorkflowControl(&wf).
		WithSSEManager(NewSimpleSSEManager()).
		Build()
	require.NoError(t, opR.Initialize(deps))

	jobCtx, gitCtx := compiler.GenerateTestContext()
	in := map[string]interface{}{
		"prompt": "how old are you",
	}

	job := workflowctl.StartJob{
		TenantId:   "test-tenant",
		RecipeName: testRecipe.GetMetadata().ID,
		Inputs:     in,
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	jobKey, err := starter.StartRecipeJob(ctx, job, engine, *testRecipe)
	require.NoError(t, err)

	// Wait for job to reach Ready status
	var inputs []PendingInput
	for i := 0; i < 200; i++ {
		time.Sleep(100 * time.Millisecond)
		inputs, err = opR.collectPendingInputs(ctx, "test-tenant")
		require.NoError(t, err)
		if len(inputs) > 0 {
			break
		}
	}
	require.Equal(t, 1, len(inputs), "Expected 1 pending input after waiting")
	pending := inputs[0]

	time.Sleep(6 * time.Second)

	afterWait, err := opR.collectPendingInputs(ctx, "test-tenant")
	require.NoError(t, err)
	require.Equal(t, 1, len(afterWait), "Expected pending input after restart wait")

	result := opR.getDetails(ctx, "test-tenant", pending.Id)
	if result.hasError() {
		t.Fatalf("failed to get result: %v", result.err)
	}
	details := result.value
	require.NotNil(t, details.Form.Question)
	require.Equal(t, "how old are you", *details.Form.Question)
	resp := any("foolish")
	hash := "abc123"
	res2 := opR.submitResponse(ctx, "test-tenant", pending.Id, FormResponse{
		Fields:   map[string]interface{}{},
		Response: &resp,
		Hash:     &hash,
	})
	if res2.hasError() {
		t.Fatalf("failed to submit response: %v", res2.err)
	}

	require.NoError(t, swf.WaitForJobToComplete(ctx, 30*time.Second, jobKey, engine))

	res3, err := swfutil.JobResult(ctx, engine, jobKey)
	require.NoError(t, err)

	res4, err := res3.GetData()
	require.NoError(t, err)

	air := Output{}
	require.NoError(t, json.Unmarshal(res4, &air))

	require.Equal(t, "foolish", air.Response)
}
