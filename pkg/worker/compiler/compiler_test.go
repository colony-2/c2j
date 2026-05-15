package compiler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	ops2 "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	toyruntime "github.com/colony-2/swf-go/pkg/swf/runtime/toy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type CompilerTestSuite struct {
	suite.Suite
	eng  swf.SWFEngine
	deps ops2.OpDependencies
	//eng *impl.EmbeddedEngine
}

func newToyEngine(t *testing.T, gen func(string) (swf.JobKey, error)) swf.SWFEngine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := make([]toyruntime.Option, 0, 1)
	if gen != nil {
		opts = append(opts, toyruntime.WithJobIDGenerator(gen))
	}
	engine, err := swf.NewEngineBuilder().
		WithRuntime(toyruntime.New(opts...)).
		BuildEngine()
	require.NoError(t, err)
	go engine.Run(ctx)
	return engine
}

func newToyEngineWithWorkSet(t *testing.T, ws *swf.WorkSet, gen func(string) (swf.JobKey, error)) swf.SWFEngine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := make([]toyruntime.Option, 0, 1)
	if gen != nil {
		opts = append(opts, toyruntime.WithJobIDGenerator(gen))
	}
	builder := swf.NewEngineBuilder().WithRuntime(toyruntime.New(opts...))
	if ws != nil {
		taskWorkers := make([]swf.TaskWorker, 0, len(ws.TaskWorkers))
		for _, tw := range ws.TaskWorkers {
			taskWorkers = append(taskWorkers, tw)
		}
		builder.PlusWorkers(ws.JobWorker, taskWorkers...)
	}
	engine, err := builder.BuildEngine()
	require.NoError(t, err)
	go engine.Run(ctx)
	return engine
}

func (s *CompilerTestSuite) SetupTest() {
	s.eng = newToyEngine(s.T(), nil)

	s.deps = ops2.NewOpDependenciesBuilder().Build()
	//eng, err := impl.StartEmbeddedEngine(context.Background(), nil)
}

func (s *CompilerTestSuite) AfterTest(suiteName, testName string) {
	//s.eng.Shutdown()
}

func TestCompilerTestSuite(t *testing.T) {
	suite.Run(t, new(CompilerTestSuite))
}

type simpleFunc func(context.Context, map[string]interface{}) (map[string]interface{}, error)

func registerActivity(registry *ops.ActivityRegistry, name string, fn simpleFunc) error {
	adapter := func(ctx context.Context, input ops.GenericInput) (ops.GenericOutput, error) {
		// Pass only the extras map through to keep the dynamic shape while using a struct for schema generation.
		out, err := fn(ctx, input.Extra)
		return ops.GenericOutput{Result: out}, err
	}

	op := ops2.NewActivityMappedOpV2[ops.GenericInput, ops.GenericOutput](
		ops2.OpMetadata{Type: name},
		func(inv ops2.OpDependencies, aCtx context.Context, in ops.GenericInput) (ops.GenericOutput, error) {
			return adapter(aCtx, in)
		},
	)
	// Ensure both the local activity registry and the global recipe-core registry know about the op
	if err := ops.Register(registry, op); err != nil {
		return err
	}
	ops2.Register(op) // recipe parser consults the global registry
	return nil
}

func (s *CompilerTestSuite) testRecipe(recipeYaml string, input map[string]interface{}, expectedOutputs map[string]interface{}) {

	registry, err := ops.NewActivityRegistry()
	require.NoError(s.T(), err)
	testRecipe, err := recipe.LoadRecipeFromString([]byte(recipeYaml))
	require.NoError(s.T(), err)
	workSet, err := NewRecipeWorker(ops2.NewServiceDepsBuilder().Build(), registry, nil)
	require.NoError(s.T(), err)
	err = s.eng.RegisterWorkers(workSet)
	require.NoError(s.T(), err)

	jobCtx, gitCtx := GenerateTestContext()

	job := workflowctl.StartJob{
		TenantId:   "test-tenant",
		RecipeName: "test-recipe",
		Inputs:     input,
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	jobKey, err := starter.StartRecipeJob(context.Background(), job, s.eng, *testRecipe)
	require.NoError(s.T(), err)
	require.NoError(s.T(), swf.WaitForJobToComplete(context.Background(), 30*time.Second, jobKey, s.eng))
	r, err := swfutil.JobResult(context.Background(), s.eng, jobKey)
	require.NoError(s.T(), err)
	d, err := r.GetData()
	require.NoError(s.T(), err)
	out := make(map[string]interface{})
	require.NoError(s.T(), json.Unmarshal(d, &out))
	assert.Equal(s.T(), expectedOutputs, out)
}

func (s *CompilerTestSuite) TestCompileSimpleRecipe() {

	recipeYaml := `
---
id: test-recipe
input_schema:
  param1:
    type: string
inputs:
  iParam1: "{{ inputs.param1 }}"
sequence:
  - id: echo
    op: echo_activity
    inputs:
      Message: "{{ inputs.iParam1 }}"
outputs:
  result: "{{ sequence.echo.outputs.output }}"
`

	input := map[string]interface{}{
		"param1": "Hello, World!",
	}

	expectedOutputs := map[string]interface{}{"result": "Hello, World!"}
	s.testRecipe(recipeYaml, input, expectedOutputs)
}

func (s *CompilerTestSuite) TestRecipeVarsResolveAcrossRecipeAndOpScopes() {
	recipeYaml := `
---
id: test-recipe
input_schema:
  title:
    type: string
vars:
  root_message: "${{ inputs.title + '-root' }}"
inputs:
  sequence_message: "${{ vars.root_message }}"
sequence:
  - id: echo
    op: echo_activity
    vars:
      op_message: "${{ vars.root_message + '-op' }}"
    inputs:
      Message: "${{ vars.op_message }}"
outputs:
  result: "${{ sequence.echo.outputs.output }}"
  root: "${{ vars.root_message }}"
`

	input := map[string]interface{}{
		"title": "demo",
	}

	expectedOutputs := map[string]interface{}{
		"result": "demo-root-op",
		"root":   "demo-root",
	}
	s.testRecipe(recipeYaml, input, expectedOutputs)
}

func (s *CompilerTestSuite) TestStateVarsReevaluateOnEachStateInvocation() {
	recipeYaml := `
---
id: test-recipe
state:
  initial: review
  states:
    review:
      op: echo_activity
      vars:
        message: "${{ state_exists('review') ? 'again' : 'first' }}"
      inputs:
        Message: "${{ vars.message }}"
      transitions:
        - to: review
          when: outputs.output == "first"
        - to: done
          when: true
    done:
      op: echo_activity
      inputs:
        Message: "${{ state_output('review', 'output', 'missing') }}"
outputs:
  result: "${{ states.done.outputs.output }}"
`

	expectedOutputs := map[string]interface{}{
		"result": "again",
	}
	s.testRecipe(recipeYaml, map[string]interface{}{}, expectedOutputs)
}

func (s *CompilerTestSuite) TestSequenceRecipeCompilation() {
	// Test sequence compilation instead of parallel
	node := &recipe.Node{
		NodeImpl: &recipe.NodeSequence{
			NodeMetadata: recipe.NodeMetadata{
				ID: "sequence-recipe",
			},
			SequenceData: recipe.SequenceData{
				Sequence: []recipe.Node{
					{
						NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								ID: "task_a",
							},
							OpData: recipe.OpData{
								Op: "activity_a",
							},
						},
					},
					{
						NodeImpl: &recipe.NodeOp{
							NodeMetadata: recipe.NodeMetadata{
								ID: "task_b",
							},
							OpData: recipe.OpData{
								Op: "activity_b",
							},
						},
					},
				},
			},
		},
	}

	// Create activity registry
	registry, err := ops.NewActivityRegistry()
	require.NoError(s.T(), err)

	// Register test activity functions
	activityAFunc := func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"result": "output_a"}, nil
	}
	activityBFunc := func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"result": "output_b"}, nil
	}
	if _, exists := registry.Get("activity_a"); !exists {
		require.NoError(s.T(), registerActivity(registry, "activity_a", activityAFunc))
	}
	if _, exists := registry.Get("activity_b"); !exists {
		require.NoError(s.T(), registerActivity(registry, "activity_b", activityBFunc))
	}

	testRecipe := &recipe.Recipe{
		RecipeImpl: &recipe.RecipeSequence{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: node.NodeImpl.(*recipe.NodeSequence).NodeMetadata,
			},
			SequenceData: node.NodeImpl.(*recipe.NodeSequence).SequenceData,
		},
	}

	workSet, err := NewRecipeWorker(ops2.NewServiceDepsBuilder().Build(), registry, nil)
	err = s.eng.RegisterWorkers(workSet)

	require.NoError(s.T(), err)
	jobCtx, gitCtx := GenerateTestContext()
	in := map[string]interface{}{}

	job := workflowctl.StartJob{
		TenantId:   "test-tenant",
		RecipeName: testRecipe.GetMetadata().ID,
		Inputs:     in,
		JobContext: jobCtx,
		GitRef:     gitCtx.ParentRef,
	}

	jobId, err := starter.StartRecipeJob(context.Background(), job, s.eng, *testRecipe)
	require.NoError(s.T(), err)
	require.NoError(s.T(), swf.WaitForJobToComplete(context.Background(), 30*time.Second, jobId, s.eng))
	r, err := swfutil.JobResult(context.Background(), s.eng, jobId)
	require.NoError(s.T(), err)
	d, err := r.GetData()
	require.NoError(s.T(), err)
	out := make(map[string]interface{})
	require.NoError(s.T(), json.Unmarshal(d, &out))
}

func TestActivityRegistry(t *testing.T) {
	// Activity registry is now in ops package
	registry, _ := ops.NewActivityRegistry()

	// Test that registry exists and can be created
	assert.NotNil(t, registry)

	// List activities (should be empty or have defaults)
	activities := registry.List()
	assert.NotNil(t, activities)
}

func (s *CompilerTestSuite) TestRecipeWithSharedActivities() {
	recipeYaml := `
id: shared-recipe
version: 1.0
defs:
  my_llm:
    op: llm
    inputs:
      model: gpt-4
      type: ai_prompt

sequence:
  - shared: my_llm
`

	recipeDef, err := recipe.LoadRecipeFromString([]byte(recipeYaml))
	require.NoError(s.T(), err)

	// ensure the sequence node is of type llm.
	assert.NotNil(s.T(), recipeDef)
	recipeSeq := recipeDef.RecipeImpl.(*recipe.RecipeSequence)
	assert.Equal(s.T(), "1.0", recipeSeq.RecipeMetadata.Version)
	item := recipeSeq.Sequence[0]
	assert.NotNil(s.T(), recipeSeq.Sequence[0])
	op := item.NodeImpl.(*recipe.NodeOp)
	assert.Equal(s.T(), "llm", op.Op)
	assert.Len(s.T(), recipeSeq.Sequence, 1)
}
