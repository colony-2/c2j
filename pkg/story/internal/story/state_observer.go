package story

import (
	"strings"

	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/c2j/pkg/worker/compiler"
)

type storyStateObserver struct {
	tree *treeBuilder

	seenAnyState bool

	currentState   *model.JobRunStoryNode
	currentExited  bool
	stateNeedsPop  bool
	transitionEval *model.JobRunStoryNode
}

func newStoryStateObserver(tree *treeBuilder) *storyStateObserver {
	return &storyStateObserver{tree: tree}
}

func (o *storyStateObserver) StateEntered(stateName string) {
	if o == nil || o.tree == nil {
		return
	}

	// Close prior state (transition evals may arrive after StateExited).
	o.closeCurrentState()

	stateName = strings.TrimSpace(stateName)
	title := "state"
	if stateName != "" {
		title = "state " + stateName
	}
	n := o.tree.newNode(model.JobRunStoryNodeKindState, title)
	n.Status = model.JobRunStoryNodeStatusRunning
	n.StateID = stateName
	if !o.seenAnyState {
		b := true
		n.IsInitial = &b
		o.seenAnyState = true
	} else {
		b := false
		n.IsInitial = &b
	}

	o.tree.push("state:"+stateName, n)
	o.currentState = n
	o.currentExited = false
	o.stateNeedsPop = true
	o.transitionEval = nil
}

func (o *storyStateObserver) StateExited(_ string) {
	if o == nil {
		return
	}
	// Intentionally do not pop here: some compiler implementations emit transition
	// evaluation callbacks after StateExited. We keep the state node on the stack
	// until the next StateEntered or Flush().
	o.currentExited = true
}

func (o *storyStateObserver) TransitionEvalauted(expression string, result bool, nextStateIfExpressionTrue string) {
	if o == nil || o.tree == nil || o.currentState == nil {
		return
	}
	expression = strings.TrimSpace(expression)
	nextStateIfExpressionTrue = strings.TrimSpace(nextStateIfExpressionTrue)

	te := o.transitionEval
	if te == nil {
		te = o.tree.newNode(model.JobRunStoryNodeKindTransitionEval, "evaluate transitions")
		te.Status = model.JobRunStoryNodeStatusSucceeded
		te.FromStateID = strings.TrimSpace(o.currentState.StateID)
		te.Evaluations = make([]model.JobRunStoryTransitionEval, 0, 4)

		// Best-effort path: append "transitionEval" to the current state node path.
		if len(o.currentState.Path) > 0 {
			te.Path = append(append([]string{}, o.currentState.Path...), "transitionEval")
		} else {
			te.Path = make([]string, 0, 8)
		}

		o.currentState.Children = append(o.currentState.Children, te)
		o.transitionEval = te
	}

	te.Evaluations = append(te.Evaluations, model.JobRunStoryTransitionEval{
		ToStateID:  nextStateIfExpressionTrue,
		Expression: expression,
		Result:     result,
		Reason:     nil,
	})

	if result && te.Decision == nil {
		to := nextStateIfExpressionTrue
		te.Decision = &model.JobRunStoryTransitionDecision{
			Kind:      "state",
			ToStateID: &to,
		}
	}
}

func (o *storyStateObserver) Flush() {
	if o == nil {
		return
	}
	o.closeCurrentState()
}

func (o *storyStateObserver) closeCurrentState() {
	if o == nil || o.tree == nil || o.currentState == nil {
		return
	}

	// Ensure transition eval decision is always set when present.
	if te := o.transitionEval; te != nil {
		// Rebase path now that ExecuteNode likely populated the state path.
		if len(o.currentState.Path) > 0 {
			te.Path = append(append([]string{}, o.currentState.Path...), "transitionEval")
		}
		if te.Decision == nil {
			te.Decision = &model.JobRunStoryTransitionDecision{Kind: "fallthrough"}
		}
	}

	// Finalize state status if still running.
	if o.currentExited && o.currentState.Status == model.JobRunStoryNodeStatusRunning {
		o.currentState.Status = deriveContainerStatus(o.currentState.Children)
		if o.currentState.Status == model.JobRunStoryNodeStatusUnknown {
			o.currentState.Status = model.JobRunStoryNodeStatusSucceeded
		}
	}

	// Pop if this observer pushed it and it is currently on the stack.
	if o.stateNeedsPop {
		if cur := o.tree.current(); cur == o.currentState {
			o.tree.pop()
		}
	}

	o.currentState = nil
	o.currentExited = false
	o.stateNeedsPop = false
	o.transitionEval = nil
}

type chainedStateObserver struct {
	a compiler.StateObserver
	b compiler.StateObserver
}

func chainStateObservers(a compiler.StateObserver, b compiler.StateObserver) compiler.StateObserver {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &chainedStateObserver{a: a, b: b}
}

func (o *chainedStateObserver) StateEntered(stateName string) {
	if o == nil {
		return
	}
	if o.a != nil {
		o.a.StateEntered(stateName)
	}
	if o.b != nil {
		o.b.StateEntered(stateName)
	}
}

func (o *chainedStateObserver) StateExited(stateName string) {
	if o == nil {
		return
	}
	if o.a != nil {
		o.a.StateExited(stateName)
	}
	if o.b != nil {
		o.b.StateExited(stateName)
	}
}

func (o *chainedStateObserver) TransitionEvalauted(expression string, result bool, nextStateIfExpressionTrue string) {
	if o == nil {
		return
	}
	if o.a != nil {
		o.a.TransitionEvalauted(expression, result, nextStateIfExpressionTrue)
	}
	if o.b != nil {
		o.b.TransitionEvalauted(expression, result, nextStateIfExpressionTrue)
	}
}

var _ compiler.StateObserver = (*storyStateObserver)(nil)
var _ compiler.StateObserver = (*chainedStateObserver)(nil)
