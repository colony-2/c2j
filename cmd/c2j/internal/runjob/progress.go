package runjob

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/colony-2/c2j/pkg/core/contextual"
	"github.com/colony-2/c2j/pkg/core/recipe"
	coreworkflow "github.com/colony-2/c2j/pkg/core/workflow"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/swf-go/pkg/swf"
)

type progressPrinter struct {
	out   io.Writer
	mode  string
	mu    sync.Mutex
	depth int
}

func newProgressPrinter(out io.Writer, mode string) *progressPrinter {
	return &progressPrinter{out: out, mode: mode}
}

func (p *progressPrinter) enter(kind string, label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.out, "%s%s%s %s\n", p.modePrefix(), strings.Repeat("  ", p.depth), kind, label)
	p.depth++
}

func (p *progressPrinter) exit(status string, label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.depth > 0 {
		p.depth--
	}
	fmt.Fprintf(p.out, "%s%s%s %s\n", p.modePrefix(), strings.Repeat("  ", p.depth), status, label)
}

func (p *progressPrinter) event(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.out, "%s%s%s\n", p.modePrefix(), strings.Repeat("  ", p.depth), fmt.Sprintf(format, args...))
}

func (p *progressPrinter) modePrefix() string {
	switch p.mode {
	case "cached":
		return "[cached] "
	case "live":
		return "[live] "
	default:
		return ""
	}
}

func (p *progressPrinter) OnJobStart(event swf.JobStartEvent) {
	p.event("attempt %d started", event.AttemptNumber)
}

func (p *progressPrinter) OnTaskStart(event swf.TaskStartEvent) {
	p.event("task %s #%d attempt %d started", event.TaskType, event.Ordinal, event.AttemptNumber)
}

func (p *progressPrinter) OnTaskEnd(event swf.TaskEndEvent) {
	if event.Err != nil {
		p.event("task %s #%d failed: %v", event.TaskType, event.Ordinal, event.Err)
		return
	}
	p.event("task %s #%d completed", event.TaskType, event.Ordinal)
}

func (p *progressPrinter) OnJobEnd(event swf.JobEndEvent) {
	if event.Err != nil {
		p.event("attempt %d ended: %v", event.AttemptNumber, event.Err)
		return
	}
	p.event("attempt %d ended", event.AttemptNumber)
}

type printingExecutor struct {
	inner   compiler.DefaultRecipeExecutor
	printer *progressPrinter
}

func newPrintingExecutor(printer *progressPrinter) *printingExecutor {
	return &printingExecutor{
		inner:   compiler.DefaultRecipeExecutor{},
		printer: printer,
	}
}

func (e *printingExecutor) ExecuteRecipe(ctx coreworkflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...compiler.ExecutionOptions) (map[string]interface{}, []swf.Artifact, error) {
	label := strings.TrimSpace(r.GetMetadata().ID)
	if label == "" {
		label = "recipe"
	}
	e.printer.enter("recipe", label)
	out, arts, err := e.inner.WithDelegate(e).ExecuteRecipe(ctx, r, rawRecipeInputs, execCtx, commitContext, opts...)
	if err != nil {
		e.printer.exit("failed", label+": "+err.Error())
		return out, arts, err
	}
	e.printer.exit("done", label)
	return out, arts, nil
}

func (e *printingExecutor) ExecuteNode(ctx coreworkflow.Context, parentResCtx *template.ResolutionContext, n *recipe.Node) error {
	return e.inner.WithDelegate(e).ExecuteNode(ctx, parentResCtx, n)
}

func (e *printingExecutor) ExecuteStateMachine(ctx coreworkflow.Context, parentContext *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, stateMap *recipe.StateMap, opts ...compiler.ExecutionOptions) error {
	label := template.ScopeID(metadata, "", template.ScopeStateMachine)
	if label == "" {
		label = "state-machine"
	}
	e.printer.enter("state-machine", label)
	var execOpts compiler.ExecutionOptions
	if len(opts) > 0 {
		execOpts = opts[0]
	}
	execOpts.StateObserver = chainStateObservers(execOpts.StateObserver, printingStateObserver{printer: e.printer})
	err := e.inner.WithDelegate(e).ExecuteStateMachine(ctx, parentContext, metadata, outputTemplate, stateMap, execOpts)
	if err != nil {
		e.printer.exit("failed", label+": "+err.Error())
		return err
	}
	e.printer.exit("done", label)
	return nil
}

func (e *printingExecutor) ExecuteOp(ctx coreworkflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, op string) error {
	label := strings.TrimSpace(op)
	if label == "" {
		label = "op"
	}
	e.printer.enter("op", label)
	err := e.inner.WithDelegate(e).ExecuteOp(ctx, parentResolutionContext, metadata, op)
	if err != nil {
		e.printer.exit("failed", label+": "+err.Error())
		return err
	}
	e.printer.exit("done", label)
	return nil
}

func (e *printingExecutor) ExecuteSequence(ctx coreworkflow.Context, rCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error {
	label := template.ScopeID(metadata, "", template.ScopeSequence)
	if label == "" {
		label = "sequence"
	}
	e.printer.enter("sequence", label)
	err := e.inner.WithDelegate(e).ExecuteSequence(ctx, rCtx, metadata, outputTemplate, sequence)
	if err != nil {
		e.printer.exit("failed", label+": "+err.Error())
		return err
	}
	e.printer.exit("done", label)
	return nil
}

type printingStateObserver struct {
	printer *progressPrinter
}

func (o printingStateObserver) StateEntered(stateName string) {
	o.printer.enter("state", stateName)
}

func (o printingStateObserver) StateExited(stateName string) {
	o.printer.exit("done", stateName)
}

func (o printingStateObserver) TransitionEvalauted(expression string, result bool, nextStateIfExpressionTrue string) {
	if nextStateIfExpressionTrue != "" {
		o.printer.event("transition %q => %t (%s)", expression, result, nextStateIfExpressionTrue)
		return
	}
	o.printer.event("transition %q => %t", expression, result)
}

func chainStateObservers(left compiler.StateObserver, right compiler.StateObserver) compiler.StateObserver {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return stateObserverChain{left: left, right: right}
}

type stateObserverChain struct {
	left  compiler.StateObserver
	right compiler.StateObserver
}

func (c stateObserverChain) StateEntered(stateName string) {
	c.left.StateEntered(stateName)
	c.right.StateEntered(stateName)
}

func (c stateObserverChain) StateExited(stateName string) {
	c.left.StateExited(stateName)
	c.right.StateExited(stateName)
}

func (c stateObserverChain) TransitionEvalauted(expression string, result bool, nextStateIfExpressionTrue string) {
	c.left.TransitionEvalauted(expression, result, nextStateIfExpressionTrue)
	c.right.TransitionEvalauted(expression, result, nextStateIfExpressionTrue)
}
