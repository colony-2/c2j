package story

import (
	"errors"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	coreworkflow "github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
)

type recordingExecutor struct {
	inner compiler.DefaultRecipeExecutor
	tree  *treeBuilder
	root  *model.JobRunStoryNode
	rec   *replayStoryRecorder
}

func newRecordingExecutor(inner compiler.DefaultRecipeExecutor, tree *treeBuilder, rec *replayStoryRecorder) *recordingExecutor {
	return &recordingExecutor{inner: inner, tree: tree, rec: rec}
}

func (e *recordingExecutor) Root() *model.JobRunStoryNode { return e.root }

func (e *recordingExecutor) ExecuteRecipe(ctx coreworkflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...compiler.ExecutionOptions) (map[string]interface{}, []swf.Artifact, error) {
	recipeID := strings.TrimSpace(r.GetMetadata().ID)
	root := e.tree.newNode(model.JobRunStoryNodeKindRecipe, "recipe "+recipeID)
	root.RecipeID = recipeID
	root.Invocation = map[string]interface{}{"args": rawRecipeInputs}
	root.Input = rawRecipeInputs
	root.Status = model.JobRunStoryNodeStatusRunning
	e.tree.push("root", root)
	e.root = root
	if e.rec != nil {
		e.rec.SetRecipeMeta(recipeID, strings.TrimSpace(r.GetMetadata().Version))
		e.rec.SetRoot(root)
	}

	out, arts, err := e.inner.WithDelegate(e).ExecuteRecipe(ctx, r, rawRecipeInputs, execCtx, commitContext, opts...)
	if err != nil {
		root.Status = statusFromErr(err, root.Status)
		root.Output = out
		e.tree.pop()
		return out, arts, err
	}
	root.Status = model.JobRunStoryNodeStatusSucceeded
	root.Output = out
	e.tree.pop()
	return out, arts, nil
}

func (e *recordingExecutor) ExecuteNode(ctx coreworkflow.Context, parentResCtx *template.ResolutionContext, n *recipe.Node) error {
	if parentResCtx != nil && parentResCtx.ScopeType == template.ScopeState {
		nodePath := ""
		if tec := parentResCtx.TaskExecutionContext(); strings.TrimSpace(tec.Invocation.NodePath) != "" {
			nodePath = tec.Invocation.NodePath
		}

		title := "state"
		if segs := splitInvocationNodePath(nodePath); len(segs) > 0 {
			seg := strings.TrimSpace(segs[len(segs)-1])
			seg = strings.TrimPrefix(seg, "state:")
			seg = strings.TrimSpace(seg)
			if seg != "" {
				title = "state " + seg
			}
		}

		// Prefer state nodes created by an injected compiler.StateObserver so transition
		// evaluation nodes can be nested under the correct state.
		if cur := e.tree.current(); cur != nil && cur.Kind == model.JobRunStoryNodeKindState && cur.Status == model.JobRunStoryNodeStatusRunning {
			// Populate path/title opportunistically from the invocation context.
			cur.Title = title
			setStoryNodePath(cur, nodePath)
			err := e.inner.WithDelegate(e).ExecuteNode(ctx, parentResCtx, n)
			if err != nil {
				cur.Status = statusFromErr(err, cur.Status)
				return err
			}
			return nil
		}

		// Fallback: create state nodes here (no transition eval capture).
		stateNode := e.tree.newNode(model.JobRunStoryNodeKindState, title)
		stateNode.Status = model.JobRunStoryNodeStatusRunning
		setStoryNodePath(stateNode, nodePath)
		e.tree.push("state", stateNode)

		err := e.inner.WithDelegate(e).ExecuteNode(ctx, parentResCtx, n)
		if err != nil {
			stateNode.Status = statusFromErr(err, stateNode.Status)
			e.tree.pop()
			return err
		}
		stateNode.Status = deriveContainerStatus(stateNode.Children)
		if stateNode.Status == model.JobRunStoryNodeStatusUnknown {
			stateNode.Status = model.JobRunStoryNodeStatusSucceeded
		}
		e.tree.pop()
		return nil
	}

	return e.inner.WithDelegate(e).ExecuteNode(ctx, parentResCtx, n)
}

func (e *recordingExecutor) ExecuteSequence(ctx coreworkflow.Context, rCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error {
	seqID := template.ScopeID(metadata, "", template.ScopeSequence)
	node := e.tree.newNode(model.JobRunStoryNodeKindSequence, "sequence "+seqID)
	node.SequenceID = seqID
	node.Status = model.JobRunStoryNodeStatusRunning
	if rCtx != nil {
		if resolved, err := rCtx.ResolveMap(metadata.Inputs); err == nil {
			node.Input = resolved
		}
	}
	e.tree.push("sequence:"+seqID, node)

	err := e.inner.WithDelegate(e).ExecuteSequence(ctx, rCtx, metadata, outputTemplate, sequence)
	if err != nil {
		node.Status = statusFromErr(err, node.Status)
		e.tree.pop()
		return err
	}
	node.Status = deriveContainerStatus(node.Children)
	if rCtx != nil {
		node.Output = rCtx.GetLastExecution()
	}
	e.tree.pop()
	return nil
}

func (e *recordingExecutor) ExecuteStateMachine(ctx coreworkflow.Context, parentContext *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, stateMap *recipe.StateMap, opts ...compiler.ExecutionOptions) error {
	smID := template.ScopeID(metadata, "", template.ScopeStateMachine)
	node := e.tree.newNode(model.JobRunStoryNodeKindStateMachine, "stateMachine "+smID)
	node.StateMachineID = smID
	node.Status = model.JobRunStoryNodeStatusRunning
	if parentContext != nil {
		nodePath := ""
		if tec := parentContext.TaskExecutionContext(); strings.TrimSpace(tec.Invocation.NodePath) != "" {
			nodePath = tec.Invocation.NodePath
		}
		setStoryNodePath(node, nodePath, "stateMachine:"+smID)
		if resolved, err := parentContext.ResolveMap(metadata.Inputs); err == nil {
			node.Input = resolved
		}
	}
	e.tree.push("stateMachine:"+smID, node)

	stObs := newStoryStateObserver(e.tree)
	defer stObs.Flush()

	var execOpts compiler.ExecutionOptions
	if len(opts) > 0 {
		execOpts = opts[0]
	}
	execOpts.StateObserver = chainStateObservers(execOpts.StateObserver, stObs)

	err := e.inner.WithDelegate(e).ExecuteStateMachine(ctx, parentContext, metadata, outputTemplate, stateMap, execOpts)
	if err != nil {
		node.Status = statusFromErr(err, node.Status)
		e.tree.pop()
		return err
	}
	node.Status = deriveContainerStatus(node.Children)
	if node.Status == model.JobRunStoryNodeStatusUnknown {
		node.Status = model.JobRunStoryNodeStatusSucceeded
	}
	if parentContext != nil {
		node.Output = parentContext.GetLastExecution()
	}
	e.tree.pop()
	return nil
}

func (e *recordingExecutor) ExecuteOp(ctx coreworkflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, opID string) error {
	opID = strings.TrimSpace(opID)
	opNode := e.tree.newNode(model.JobRunStoryNodeKindOp, "op "+opID)
	opNode.OpID = opID
	opNode.OpType = "custom"
	opNode.Status = model.JobRunStoryNodeStatusRunning
	e.tree.push("op:"+opID, opNode)

	if e.rec != nil {
		e.rec.SetCurrentOpNode(opNode)
	}
	err := e.inner.WithDelegate(e).ExecuteOp(ctx, parentResolutionContext, metadata, opID)
	if e.rec != nil {
		e.rec.SetCurrentOpNode(nil)
	}

	if err != nil {
		e.ensureReplayMissStepNode(opNode, opID, err)
		first := findFirstChildStep(opNode.Children)
		last := findLastChildStep(opNode.Children)
		if first != nil {
			opNode.Input = first.Input
		}
		if last != nil {
			opNode.Output = last.Output
		}
		opNode.Status = statusFromErr(err, opNode.Status)
		e.tree.pop()
		return err
	}

	// Attach op-level input/output from steps.
	first := findFirstChildStep(opNode.Children)
	last := findLastChildStep(opNode.Children)
	if first != nil {
		opNode.Input = first.Input
	}
	if last != nil {
		opNode.Output = last.Output
	}

	// Flatten single-step op where the only child step is "<opId>".
	if len(opNode.Children) == 1 {
		ch := opNode.Children[0]
		if ch != nil && ch.Kind == model.JobRunStoryNodeKindOpStep && ch.StepID == opID {
			opNode.Attempt = ch.Attempt
			opNode.PriorAttempts = ch.PriorAttempts
			opNode.Input = ch.Input
			opNode.Output = ch.Output
			opNode.ArtifactKeys = ch.ArtifactKeys
			opNode.ArtifactRefs = ch.ArtifactRefs
			opNode.TaskOrdinal = ch.TaskOrdinal
			opNode.RestartFromOrdinal = ch.RestartFromOrdinal
			opNode.StartedAt = ch.StartedAt
			opNode.FinishedAt = ch.FinishedAt
			opNode.Error = ch.Error
			opNode.Children = make([]*model.JobRunStoryNode, 0)
			opNode.InvokeSeq = ch.InvokeSeq
			opNode.Path = append([]string{}, ch.Path...)
			opNode.Status = ch.Status
			e.tree.pop()
			return nil
		}
	}

	opNode.Status = deriveContainerStatus(opNode.Children)
	if opNode.Status == model.JobRunStoryNodeStatusUnknown {
		opNode.Status = model.JobRunStoryNodeStatusSucceeded
	}

	e.tree.pop()
	return nil
}

func (e *recordingExecutor) ensureReplayMissStepNode(opNode *model.JobRunStoryNode, opID string, err error) {
	if opNode == nil {
		return
	}

	var miss swf.ReplayCacheMissError
	if !errors.As(err, &miss) || miss.Reason != swf.ReplayCacheMissTaskResultMissing {
		return
	}

	stepID := strings.TrimSpace(stepIDFromTaskType(opID, miss.TaskType))
	if stepID == "" {
		stepID = strings.TrimSpace(miss.TaskType)
	}
	if stepID == "" {
		return
	}

	for _, ch := range opNode.Children {
		if ch == nil || ch.Kind != model.JobRunStoryNodeKindOpStep {
			continue
		}
		if ch.TaskOrdinal != nil && *ch.TaskOrdinal == miss.Ordinal {
			applyTaskOutputToNode(ch, "", miss.TaskType, nil, miss)
			return
		}
	}

	stepNode := e.tree.newNode(model.JobRunStoryNodeKindOpStep, "step "+stepID)
	stepNode.StepID = stepID
	stepNode.StepType = "other"
	if miss.Attempt > 0 {
		stepNode.Attempt = miss.Attempt
	}
	ord := miss.Ordinal
	stepNode.TaskOrdinal = &ord
	stepNode.RestartFromOrdinal = &ord
	applyTaskOutputToNode(stepNode, "", miss.TaskType, nil, miss)
	opNode.Children = append(opNode.Children, stepNode)
}
