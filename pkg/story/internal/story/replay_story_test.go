package story

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	coretasks "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

type fakeReplayEngine struct {
	jobInput    swf.JobData
	taskScripts []fakeTaskScript
	jobStartAt  time.Time
	jobEndAt    time.Time

	skipTaskStartOnMiss bool
}

func (e *fakeReplayEngine) ReplayJobRun(ctx context.Context, req swf.ReplayRunRequest) (swf.JobData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.JobWorker == nil {
		return nil, swf.ReplayCacheMissError{JobKey: req.JobKey, Ordinal: 0, Attempt: 1, Reason: swf.ReplayCacheMissJobResultMissing}
	}
	obs := req.Observer
	if obs != nil {
		obs.OnJobStart(swf.JobStartEvent{JobKey: req.JobKey, AttemptNumber: 1, Input: e.jobInput, At: e.jobStartAt})
	}
	jc := &fakeReplayJobContext{
		jobKey:              req.JobKey,
		observer:            obs,
		nextOrdinal:         1,
		scripts:             append([]fakeTaskScript{}, e.taskScripts...),
		skipTaskStartOnMiss: e.skipTaskStartOnMiss,
	}
	out, err := req.JobWorker.Run(jc, e.jobInput)
	if obs != nil {
		obs.OnJobEnd(swf.JobEndEvent{JobKey: req.JobKey, AttemptNumber: 1, Output: out, Err: err, At: e.jobEndAt})
	}
	return out, err
}

type fakeReplayJobContext struct {
	jobKey   swf.JobKey
	observer swf.ReplayObserver

	nextOrdinal int64
	scripts     []fakeTaskScript

	skipTaskStartOnMiss bool
}

func (c *fakeReplayJobContext) GetJobKey() swf.JobKey { return c.jobKey }
func (c *fakeReplayJobContext) Logger() *slog.Logger  { return nil }

func (c *fakeReplayJobContext) AwaitDuration(_ swf.Duration) error { return nil }
func (c *fakeReplayJobContext) AwaitJobs(_ ...string) error        { return nil }

type fakeTaskAttempt struct {
	Attempt    int
	Output     swf.TaskData
	Err        error
	StartedAt  time.Time
	FinishedAt time.Time
}

type fakeTaskScript struct {
	Ordinal  int64
	Attempts []fakeTaskAttempt
}

func (c *fakeReplayJobContext) DoTask(_ swf.RunPolicy, taskType string, input swf.TaskData) (swf.TaskData, error) {
	ord := c.nextOrdinal
	script := fakeTaskScript{}
	if len(c.scripts) > 0 {
		script = c.scripts[0]
		c.scripts = c.scripts[1:]
	}
	if script.Ordinal != 0 {
		ord = script.Ordinal
	} else {
		c.nextOrdinal++
	}

	if len(script.Attempts) == 0 {
		script.Attempts = []fakeTaskAttempt{{
			Attempt: 1,
			Output:  nil,
			Err: swf.ReplayCacheMissError{
				JobKey:   c.jobKey,
				TaskType: taskType,
				Ordinal:  ord,
				Attempt:  1,
				Reason:   swf.ReplayCacheMissTaskResultMissing,
			},
		}}
	}

	for _, att := range script.Attempts {
		attemptNum := att.Attempt
		if attemptNum <= 0 {
			attemptNum = 1
		}
		var miss swf.ReplayCacheMissError
		if c.observer != nil && !(c.skipTaskStartOnMiss && errors.As(att.Err, &miss)) {
			c.observer.OnTaskStart(swf.TaskStartEvent{
				JobKey:        c.jobKey,
				TaskType:      taskType,
				Ordinal:       ord,
				AttemptNumber: attemptNum,
				Input:         input,
				At:            att.StartedAt,
			})
		}
		if c.observer != nil {
			c.observer.OnTaskEnd(swf.TaskEndEvent{
				JobKey:        c.jobKey,
				TaskType:      taskType,
				Ordinal:       ord,
				AttemptNumber: attemptNum,
				Output:        att.Output,
				Err:           att.Err,
				At:            att.FinishedAt,
			})
		}
	}

	last := script.Attempts[len(script.Attempts)-1]
	return last.Output, last.Err
}

var _ swf.JobContext = (*fakeReplayJobContext)(nil)

func TestJobRunStoryReplay_BuildsTreeAndNormalizesMissingOutputKeys(t *testing.T) {
	type in struct{}
	type out struct {
		Present string `json:"present"`
		Missing string `json:"missing,omitempty"`
	}
	coreops.Register(coreops.NewActivityMappedOpV2[in, out](coreops.OpMetadata{Type: "test_story_normalize"}, func(_ coreops.OpDependencies, _ context.Context, _ in) (out, error) {
		return out{Present: "ok"}, nil
	}))

	recipeYAML := `
id: test
version: "1.0.0"
sequence:
  - id: run
    op: test_story_normalize
outputs:
  present: "{{ sequence.run.outputs.present }}"
  missing: "{{ sequence.run.outputs.missing }}"
`
	recipeArt := swf.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{
		RecipeName: "test",
		GitRef:     "main",
		Inputs:     map[string]any{},
		JobContext: contextual.JobContext{
			GitBase: contextual.GitBaseContext{BaseRepo: "/src", BaseRef: "main"},
			Workflow: contextual.WorkflowContext{
				CellName: "test",
			},
		},
	}
	jobInput, err := swf.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	env, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git":          map[string]any{},
		"nextTaskType": "",
		"output":       map[string]any{"present": "ok"},
	})
	if err != nil {
		t.Fatalf("NewOutputEnvelope: %v", err)
	}
	outBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	taskOut := &swf.SimpleTaskData{Data: json.RawMessage(outBytes)}

	engine := &fakeReplayEngine{
		jobInput:    swf.JobData(jobInput),
		taskScripts: []fakeTaskScript{{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: taskOut, Err: nil}}}},
	}

	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}
	if st == nil || st.Root == nil || st.Root.Kind != model.JobRunStoryNodeKindRecipe {
		t.Fatalf("expected recipe root, got %#v", st)
	}
	if st.Status != model.WorkflowStatusCompleted {
		t.Fatalf("expected story status completed, got %q", st.Status)
	}
	outMap, ok := st.Root.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected root output map, got %T", st.Root.Output)
	}
	if outMap["present"] != "ok" {
		t.Fatalf("expected present=%q, got %#v", "ok", outMap["present"])
	}
	if outMap["missing"] != "" {
		t.Fatalf("expected missing=%q, got %#v", "", outMap["missing"])
	}
}

func TestJobRunStoryReplay_RecordsRootSourceResolutionNode(t *testing.T) {
	type in struct{}
	type out struct {
		Value string `json:"value"`
	}
	const opType = "test_story_root_source"
	coreops.Register(coreops.NewActivityMappedOpV2[in, out](coreops.OpMetadata{Type: opType}, func(_ coreops.OpDependencies, _ context.Context, _ in) (out, error) {
		return out{Value: "ok"}, nil
	}))

	resolvedRecipeYAML := strings.TrimSpace(`
id: story_ref_recipe
version: "1.0.0"
sequence:
  - id: run
    op: test_story_root_source
outputs:
  result: "{{ sequence.run.outputs.value }}"
`) + "\n"

	start := workflowctl.StartJob{
		RecipeName: "story-ref",
		GitRef:     "main",
		Inputs:     map[string]any{},
		JobContext: contextual.JobContext{
			GitBase: contextual.GitBaseContext{BaseRepo: "/src", BaseRef: "main"},
			Workflow: contextual.WorkflowContext{
				CellName: "test",
			},
		},
	}
	jobInput, err := swf.NewTaskData(start)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	resolutionOut, err := swf.NewTaskData(compiler.ResolvedRecipeSource{
		RecipeSourceResolution: compiler.RecipeSourceResolution{
			SourceKind:        compiler.RecipeSourceKindServerRef,
			SubmittedSelector: "story-ref",
			ResolvedSelector:  "story-ref@v1",
			ResolvedCommit:    "v1",
			WasAlreadyPinned:  false,
		},
		RecipeYAML: resolvedRecipeYAML,
	})
	if err != nil {
		t.Fatalf("NewTaskData(resolution): %v", err)
	}

	env, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git":          map[string]any{},
		"nextTaskType": "",
		"output":       map[string]any{"value": "ok"},
	})
	if err != nil {
		t.Fatalf("NewOutputEnvelope: %v", err)
	}
	outBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal output: %v", err)
	}
	taskOut := &swf.SimpleTaskData{Data: json.RawMessage(outBytes)}

	engine := &fakeReplayEngine{
		jobInput: swf.JobData(jobInput),
		taskScripts: []fakeTaskScript{
			{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: resolutionOut, Err: nil}}},
			{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: taskOut, Err: nil}}},
		},
	}

	st, err := BuildJobRunStory(context.Background(), engine, swf.JobKey{TenantId: "tenant", JobId: "job"}, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}
	if st.Recipe.Source.Kind != "jobStartRef" {
		t.Fatalf("expected jobStartRef source kind, got %q", st.Recipe.Source.Kind)
	}
	if st.Recipe.Source.SubmittedSelector != "story-ref" {
		t.Fatalf("expected submitted selector story-ref, got %q", st.Recipe.Source.SubmittedSelector)
	}
	if st.Recipe.Source.ResolvedSelector != "story-ref@v1" {
		t.Fatalf("expected resolved selector story-ref@v1, got %q", st.Recipe.Source.ResolvedSelector)
	}
	if st.Recipe.Source.RecipeYAML != resolvedRecipeYAML {
		t.Fatalf("expected recipe YAML to be preserved, got %q", st.Recipe.Source.RecipeYAML)
	}
	if st.Recipe.Source.ResolutionTaskOrdinal == nil || *st.Recipe.Source.ResolutionTaskOrdinal != 1 {
		t.Fatalf("expected resolution task ordinal 1, got %#v", st.Recipe.Source.ResolutionTaskOrdinal)
	}
	if st.Root == nil || len(st.Root.Children) == 0 {
		t.Fatalf("expected root story with children, got %#v", st.Root)
	}
	if st.Root.Children[0].Kind != model.JobRunStoryNodeKindRecipeSourceResolution {
		t.Fatalf("expected first child to be recipe source resolution, got %q", st.Root.Children[0].Kind)
	}
	outMap, ok := st.Root.Children[0].Output.(map[string]any)
	if !ok {
		t.Fatalf("expected resolution output map, got %T", st.Root.Children[0].Output)
	}
	if got := outMap["resolved_selector"]; got != "story-ref@v1" {
		t.Fatalf("expected resolved selector story-ref@v1, got %#v", got)
	}
	if got := outMap["recipe_yaml"]; got != resolvedRecipeYAML {
		t.Fatalf("expected recipe_yaml to be preserved, got %#v", got)
	}
}

func TestJobRunStoryReplay_BuildsStepNodesFromTaskEvents(t *testing.T) {
	type in struct{}
	type mid struct {
		Value string `json:"value"`
	}
	type out struct {
		Done bool `json:"done"`
	}

	opType := "test_story_steps"
	op, err := coreops.NewOp().
		WithType(opType).
		AddStep("first", coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, _ in) (mid, error) {
			return mid{Value: "one"}, nil
		})).
		AddStep("second", coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, _ mid) (out, error) {
			return out{Done: true}, nil
		})).
		Build()
	if err != nil {
		t.Fatalf("build op: %v", err)
	}
	coreops.Register(op)

	recipeYAML := `
id: test
version: "1.0.0"
sequence:
  - id: run
    op: test_story_steps
outputs:
  done: "{{ sequence.run.outputs.done }}"
`
	recipeArt := swf.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{
		RecipeName: "test",
		GitRef:     "main",
		Inputs:     map[string]any{},
		JobContext: contextual.JobContext{},
	}
	jobInput, err := swf.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	env1, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git":          map[string]any{},
		"nextTaskType": opType + ":second",
		"output":       map[string]any{"value": "one"},
	})
	if err != nil {
		t.Fatalf("NewOutputEnvelope step1: %v", err)
	}
	raw1, _ := json.Marshal(env1)
	taskOut1 := &swf.SimpleTaskData{Data: json.RawMessage(raw1)}

	env2, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git":          map[string]any{},
		"nextTaskType": "",
		"output":       map[string]any{"done": true},
	})
	if err != nil {
		t.Fatalf("NewOutputEnvelope step2: %v", err)
	}
	raw2, _ := json.Marshal(env2)
	taskOut2 := &swf.SimpleTaskData{Data: json.RawMessage(raw2)}

	engine := &fakeReplayEngine{
		jobInput: swf.JobData(jobInput),
		taskScripts: []fakeTaskScript{
			{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: taskOut1, Err: nil}}},
			{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: taskOut2, Err: nil}}},
		},
	}

	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}
	if st == nil || st.Root == nil {
		t.Fatalf("expected root story")
	}

	seq := findFirstKind(st.Root.Children, model.JobRunStoryNodeKindSequence)
	if seq == nil {
		t.Fatalf("expected sequence child")
	}
	opNode := findFirstKind(seq.Children, model.JobRunStoryNodeKindOp)
	if opNode == nil || opNode.OpID != opType {
		t.Fatalf("expected op node %q, got %#v", opType, opNode)
	}
	if len(opNode.Children) != 2 {
		t.Fatalf("expected 2 step children, got %d", len(opNode.Children))
	}
	if opNode.Children[0].StepID != "first" || opNode.Children[0].Status != model.JobRunStoryNodeStatusSucceeded {
		t.Fatalf("unexpected first step: %#v", opNode.Children[0])
	}
	if opNode.Children[1].StepID != "second" || opNode.Children[1].Status != model.JobRunStoryNodeStatusSucceeded {
		t.Fatalf("unexpected second step: %#v", opNode.Children[1])
	}
}

func TestJobRunStoryReplay_PriorAttemptsFromObserverRetries(t *testing.T) {
	type in struct{}
	type out struct {
		Value string `json:"value"`
	}

	opType := "test_story_retry_attempts"
	coreops.Register(coreops.NewActivityMappedOpV2[in, out](coreops.OpMetadata{Type: opType}, func(_ coreops.OpDependencies, _ context.Context, _ in) (out, error) {
		return out{Value: "ok"}, nil
	}))

	recipeYAML := `
id: test
version: "1.0.0"
sequence:
  - id: run
    op: test_story_retry_attempts
outputs:
  value: "{{ sequence.run.outputs.value }}"
`
	recipeArt := swf.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{RecipeName: "test", GitRef: "main", Inputs: map[string]any{}, JobContext: contextual.JobContext{}}
	jobInput, err := swf.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	env, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git":          map[string]any{},
		"nextTaskType": "",
		"output":       map[string]any{"value": "ok"},
	})
	if err != nil {
		t.Fatalf("NewOutputEnvelope: %v", err)
	}
	raw, _ := json.Marshal(env)
	taskOut := &swf.SimpleTaskData{Data: json.RawMessage(raw)}

	engine := &fakeReplayEngine{
		jobInput: swf.JobData(jobInput),
		taskScripts: []fakeTaskScript{{
			Attempts: []fakeTaskAttempt{
				{Attempt: 1, Output: nil, Err: errors.New("boom")},
				{Attempt: 2, Output: taskOut, Err: nil},
			},
		}},
	}

	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}

	seq := findFirstKind(st.Root.Children, model.JobRunStoryNodeKindSequence)
	opNode := findFirstKind(seq.Children, model.JobRunStoryNodeKindOp)
	if opNode == nil {
		t.Fatalf("expected op node")
	}
	// This op is single-step; recording flattens the child step into the op node.
	if opNode.Attempt != 2 {
		t.Fatalf("expected op attempt 2, got %d", opNode.Attempt)
	}
	if len(opNode.PriorAttempts) != 1 || opNode.PriorAttempts[0].Status != model.JobRunStoryNodeStatusFailed {
		t.Fatalf("expected 1 failed prior attempt, got %#v", opNode.PriorAttempts)
	}
}

func TestJobRunStoryReplay_CacheMissMarksStoryRunning(t *testing.T) {
	type in struct{}
	type out struct {
		Value string `json:"value"`
	}

	opType := "test_story_cache_miss"
	coreops.Register(coreops.NewActivityMappedOpV2[in, out](coreops.OpMetadata{Type: opType}, func(_ coreops.OpDependencies, _ context.Context, _ in) (out, error) {
		return out{Value: "ok"}, nil
	}))

	recipeYAML := `
id: test
version: "1.0.0"
sequence:
  - id: run
    op: test_story_cache_miss
outputs:
  value: "{{ sequence.run.outputs.value }}"
`
	recipeArt := swf.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{RecipeName: "test", GitRef: "main", Inputs: map[string]any{}, JobContext: contextual.JobContext{}}
	jobInput, err := swf.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	engine := &fakeReplayEngine{
		jobInput: swf.JobData(jobInput),
		taskScripts: []fakeTaskScript{{
			Attempts: []fakeTaskAttempt{{
				Attempt: 1,
				Output:  nil,
				Err: swf.ReplayCacheMissError{
					JobKey:   swf.JobKey{TenantId: "tenant", JobId: "job"},
					TaskType: opType + ":" + opType,
					Ordinal:  1,
					Attempt:  1,
					Reason:   swf.ReplayCacheMissTaskResultMissing,
				},
			}},
		}},
	}

	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}
	if st.Status != model.WorkflowStatusRunning {
		t.Fatalf("expected story status running, got %q", st.Status)
	}

	seq := findFirstKind(st.Root.Children, model.JobRunStoryNodeKindSequence)
	opNode := findFirstKind(seq.Children, model.JobRunStoryNodeKindOp)
	if opNode == nil || opNode.Status != model.JobRunStoryNodeStatusRunning {
		t.Fatalf("expected op node running, got %#v", opNode)
	}

	step := findFirstKind(opNode.Children, model.JobRunStoryNodeKindOpStep)
	if step == nil || step.Status != model.JobRunStoryNodeStatusRunning {
		t.Fatalf("expected step running on cache miss, got %#v", step)
	}
}

func TestJobRunStoryReplay_CacheMissWithoutTaskStartSynthesizesRunningStep(t *testing.T) {
	type in struct{}
	type out struct {
		Value string `json:"value"`
	}

	opType := "test_story_cache_miss_without_start"
	coreops.Register(coreops.NewActivityMappedOpV2[in, out](coreops.OpMetadata{Type: opType}, func(_ coreops.OpDependencies, _ context.Context, _ in) (out, error) {
		return out{Value: "ok"}, nil
	}))

	recipeYAML := `
id: test
version: "1.0.0"
sequence:
  - id: run
    op: test_story_cache_miss_without_start
outputs:
  value: "{{ sequence.run.outputs.value }}"
`
	recipeArt := swf.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{RecipeName: "test", GitRef: "main", Inputs: map[string]any{}, JobContext: contextual.JobContext{}}
	jobInput, err := swf.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	engine := &fakeReplayEngine{
		jobInput:            swf.JobData(jobInput),
		skipTaskStartOnMiss: true,
		taskScripts: []fakeTaskScript{{
			Attempts: []fakeTaskAttempt{{
				Attempt: 1,
				Output:  nil,
				Err: swf.ReplayCacheMissError{
					JobKey:   swf.JobKey{TenantId: "tenant", JobId: "job"},
					TaskType: opType + ":" + opType,
					Ordinal:  1,
					Attempt:  1,
					Reason:   swf.ReplayCacheMissTaskResultMissing,
				},
			}},
		}},
	}

	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}
	if st.Status != model.WorkflowStatusRunning {
		t.Fatalf("expected story status running, got %q", st.Status)
	}

	seq := findFirstKind(st.Root.Children, model.JobRunStoryNodeKindSequence)
	opNode := findFirstKind(seq.Children, model.JobRunStoryNodeKindOp)
	if opNode == nil || opNode.Status != model.JobRunStoryNodeStatusRunning {
		t.Fatalf("expected op node running, got %#v", opNode)
	}

	step := findFirstKind(opNode.Children, model.JobRunStoryNodeKindOpStep)
	if step == nil || step.Status != model.JobRunStoryNodeStatusRunning {
		t.Fatalf("expected synthesized running step on cache miss, got %#v", step)
	}
	if step.StepID != opType {
		t.Fatalf("expected synthesized step %q, got %#v", opType, step)
	}
}

func findFirstKind(nodes []*model.JobRunStoryNode, kind model.JobRunStoryNodeKind) *model.JobRunStoryNode {
	for _, n := range nodes {
		if n != nil && n.Kind == kind {
			return n
		}
	}
	return nil
}

func TestJobRunStoryReplay_UsesInjectedCELOptionsProvider(t *testing.T) {
	builder := funcregistry.NewBuilder().WithDefaults().WithBuiltin("hello", func(_ types.Adapter, _ funcregistry.ContextProvider) cel.EnvOption {
		return cel.Function(
			"hello",
			cel.Overload(
				"hello_0",
				[]*cel.Type{},
				cel.StringType,
				cel.FunctionBinding(func(...ref.Val) ref.Val { return types.String("hi") }),
			),
		)
	})

	recipeYAML := `
id: test
version: "1.0.0"
sequence: []
outputs:
  greet: "${{ hello() }}"
`
	recipeArt := swf.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{
		RecipeName: "test",
		GitRef:     "main",
		Inputs:     map[string]any{},
		JobContext: contextual.JobContext{},
	}
	jobInput, err := swf.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	engine := &fakeReplayEngine{jobInput: swf.JobData(jobInput)}
	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, builder, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}
	if st == nil || st.Root == nil {
		t.Fatalf("expected root")
	}
	outMap, ok := st.Root.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected root output map, got %T", st.Root.Output)
	}
	if outMap["greet"] != "hi" {
		t.Fatalf("expected greet=%q, got %#v", "hi", outMap["greet"])
	}
}

func TestJobRunStoryReplay_TaskTimestampsPopulateNodes(t *testing.T) {
	type in struct{}
	type out struct {
		Value string `json:"value"`
	}

	opType := "test_story_timestamps"
	coreops.Register(coreops.NewActivityMappedOpV2[in, out](coreops.OpMetadata{Type: opType}, func(_ coreops.OpDependencies, _ context.Context, _ in) (out, error) {
		return out{Value: "ok"}, nil
	}))

	recipeYAML := `
id: test
version: "1.0.0"
sequence:
  - id: run
    op: test_story_timestamps
outputs: {}
`
	recipeArt := swf.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{RecipeName: "test", GitRef: "main", Inputs: map[string]any{}, JobContext: contextual.JobContext{}}
	jobInput, err := swf.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	env, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git":          map[string]any{},
		"nextTaskType": "",
		"output":       map[string]any{"value": "ok"},
	})
	if err != nil {
		t.Fatalf("NewOutputEnvelope: %v", err)
	}
	raw, _ := json.Marshal(env)
	taskOut := &swf.SimpleTaskData{Data: json.RawMessage(raw)}

	t0 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Second)
	t2 := t1.Add(5 * time.Second)
	tEnd := t0.Add(1 * time.Minute)

	engine := &fakeReplayEngine{
		jobInput:   swf.JobData(jobInput),
		jobStartAt: t0,
		jobEndAt:   tEnd,
		taskScripts: []fakeTaskScript{{
			Attempts: []fakeTaskAttempt{{
				Attempt:    1,
				Output:     taskOut,
				Err:        nil,
				StartedAt:  t1,
				FinishedAt: t2,
			}},
		}},
	}

	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}
	if st.StartedAt.IsZero() || !st.StartedAt.Equal(t0) {
		t.Fatalf("expected story started_at %v, got %v", t0, st.StartedAt)
	}
	if st.FinishedAt == nil || !st.FinishedAt.Equal(tEnd) {
		t.Fatalf("expected story finished_at %v, got %#v", tEnd, st.FinishedAt)
	}

	seq := findFirstKind(st.Root.Children, model.JobRunStoryNodeKindSequence)
	opNode := findFirstKind(seq.Children, model.JobRunStoryNodeKindOp)
	if opNode == nil {
		t.Fatalf("expected op node")
	}
	if opNode.StartedAt == nil || !opNode.StartedAt.Equal(t1) {
		t.Fatalf("expected op started_at %v, got %#v", t1, opNode.StartedAt)
	}
	if opNode.FinishedAt == nil || !opNode.FinishedAt.Equal(t2) {
		t.Fatalf("expected op finished_at %v, got %#v", t2, opNode.FinishedAt)
	}
}

func TestJobRunStoryReplay_StateObserverCapturesTransitions(t *testing.T) {
	type in struct{}
	type out struct {
		OK bool `json:"ok"`
	}

	opType := "test_story_state_observer"
	coreops.Register(coreops.NewActivityMappedOpV2[in, out](coreops.OpMetadata{Type: opType}, func(_ coreops.OpDependencies, _ context.Context, _ in) (out, error) {
		return out{OK: true}, nil
	}))

	recipeYAML := `
id: test
version: "1.0.0"
sequence:
  - id: sm
    state:
      initial: s1
      states:
        s1:
          op: test_story_state_observer
          inputs: {}
          transitions:
            - to: s2
              when: "false"
            - to: s2
              when: "true"
        s2:
          op: test_story_state_observer
          inputs: {}
outputs: {}
`
	recipeArt := swf.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{RecipeName: "test", GitRef: "main", Inputs: map[string]any{}, JobContext: contextual.JobContext{}}
	jobInput, err := swf.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	env, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git":          map[string]any{},
		"nextTaskType": "",
		"output":       map[string]any{"ok": true},
	})
	if err != nil {
		t.Fatalf("NewOutputEnvelope: %v", err)
	}
	raw, _ := json.Marshal(env)
	taskOut := &swf.SimpleTaskData{Data: json.RawMessage(raw)}

	engine := &fakeReplayEngine{
		jobInput: swf.JobData(jobInput),
		taskScripts: []fakeTaskScript{
			{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: taskOut, Err: nil}}},
			{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: taskOut, Err: nil}}},
		},
	}

	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}
	if st == nil || st.Root == nil {
		t.Fatalf("expected root")
	}

	seq := findFirstKind(st.Root.Children, model.JobRunStoryNodeKindSequence)
	if seq == nil {
		t.Fatalf("expected sequence")
	}
	sm := findFirstKind(seq.Children, model.JobRunStoryNodeKindStateMachine)
	if sm == nil {
		t.Fatalf("expected stateMachine node")
	}
	s1 := findStateByID(sm.Children, "s1")
	if s1 == nil {
		t.Fatalf("expected state s1, got %#v", sm.Children)
	}
	if s1.IsInitial == nil || *s1.IsInitial != true {
		t.Fatalf("expected s1 isInitial=true, got %#v", s1.IsInitial)
	}
	te := findFirstKind(s1.Children, model.JobRunStoryNodeKindTransitionEval)
	if te == nil {
		t.Fatalf("expected transitionEval under state s1")
	}
	if te.FromStateID != "s1" {
		t.Fatalf("expected from_state_id=s1, got %q", te.FromStateID)
	}
	if len(te.Evaluations) != 2 {
		t.Fatalf("expected 2 evaluations, got %#v", te.Evaluations)
	}
	if te.Evaluations[0].Expression != "false" || te.Evaluations[0].Result != false || te.Evaluations[0].ToStateID != "s2" {
		t.Fatalf("unexpected evaluation[0]: %#v", te.Evaluations[0])
	}
	if te.Evaluations[1].Expression != "true" || te.Evaluations[1].Result != true || te.Evaluations[1].ToStateID != "s2" {
		t.Fatalf("unexpected evaluation[1]: %#v", te.Evaluations[1])
	}
	if te.Decision == nil || te.Decision.Kind != "state" || te.Decision.ToStateID == nil || *te.Decision.ToStateID != "s2" {
		t.Fatalf("unexpected decision: %#v", te.Decision)
	}
}

func findStateByID(nodes []*model.JobRunStoryNode, stateID string) *model.JobRunStoryNode {
	for _, n := range nodes {
		if n == nil || n.Kind != model.JobRunStoryNodeKindState {
			continue
		}
		if n.StateID == stateID {
			return n
		}
	}
	return nil
}
