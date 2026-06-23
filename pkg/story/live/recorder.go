package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/redact"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	coretasks "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/worker/ops"
	coreworkflow "github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type (
	JobRunStory                   = model.JobRunStory
	JobRunStoryRecipe             = model.JobRunStoryRecipe
	JobRunStoryRecipeSource       = model.JobRunStoryRecipeSource
	JobRunStoryNode               = model.JobRunStoryNode
	JobRunStoryNodeKind           = model.JobRunStoryNodeKind
	JobRunStoryNodeStatus         = model.JobRunStoryNodeStatus
	JobRunStoryError              = model.JobRunStoryError
	JobRunStoryTransitionEval     = model.JobRunStoryTransitionEval
	JobRunStoryTransitionDecision = model.JobRunStoryTransitionDecision
	WorkflowStatus                = model.WorkflowStatus
)

const (
	WorkflowStatusRunning    = model.WorkflowStatusRunning
	WorkflowStatusCompleted  = model.WorkflowStatusCompleted
	WorkflowStatusFailed     = model.WorkflowStatusFailed
	WorkflowStatusCanceled   = model.WorkflowStatusCanceled
	WorkflowStatusTerminated = model.WorkflowStatusTerminated
	WorkflowStatusTimedOut   = model.WorkflowStatusTimedOut
	WorkflowStatusUnknown    = model.WorkflowStatusUnknown

	JobRunStoryNodeKindRecipe                 = model.JobRunStoryNodeKindRecipe
	JobRunStoryNodeKindRecipeSourceResolution = model.JobRunStoryNodeKindRecipeSourceResolution
	JobRunStoryNodeKindSequence               = model.JobRunStoryNodeKindSequence
	JobRunStoryNodeKindOp                     = model.JobRunStoryNodeKindOp
	JobRunStoryNodeKindOpStep                 = model.JobRunStoryNodeKindOpStep
	JobRunStoryNodeKindContextPatch           = model.JobRunStoryNodeKindContextPatch
	JobRunStoryNodeKindStateMachine           = model.JobRunStoryNodeKindStateMachine
	JobRunStoryNodeKindState                  = model.JobRunStoryNodeKindState
	JobRunStoryNodeKindTransitionEval         = model.JobRunStoryNodeKindTransitionEval

	JobRunStoryNodeStatusPending   = model.JobRunStoryNodeStatusPending
	JobRunStoryNodeStatusRunning   = model.JobRunStoryNodeStatusRunning
	JobRunStoryNodeStatusSucceeded = model.JobRunStoryNodeStatusSucceeded
	JobRunStoryNodeStatusFailed    = model.JobRunStoryNodeStatusFailed
	JobRunStoryNodeStatusCanceled  = model.JobRunStoryNodeStatusCanceled
	JobRunStoryNodeStatusSkipped   = model.JobRunStoryNodeStatusSkipped
	JobRunStoryNodeStatusUnknown   = model.JobRunStoryNodeStatusUnknown
)

type replayJobRunner interface {
	ReplayJobRun(ctx context.Context, req jobworkflow.ReplayRunRequest) (jobdb.JobData, error)
}

type Options struct {
	JobKey   jobdb.JobKey
	Logger   *slog.Logger
	OnChange func(*JobRunStory)
}

type Recorder struct {
	mu sync.Mutex

	jobKey   jobdb.JobKey
	logger   *slog.Logger
	onChange func(*JobRunStory)

	recipeNameFromStart string
	recipeID            string
	recipeVersion       string

	currentJobAttempt int

	attemptTrees      map[int]*treeBuilder
	rootsByJobAttempt map[int]*model.JobRunStoryNode
	attemptTiming     map[int]*jobAttemptTiming
	resolvedSources   map[int]*compiler.ResolvedRecipeSource
	sourceNodes       map[int]*rootSourceResolutionTracker

	currentOpNode  *model.JobRunStoryNode
	currentOpSteps map[int64]*ordinalStepTracker
}

type jobAttemptTiming struct {
	startedAt  *time.Time
	finishedAt *time.Time
}

type ordinalStepTracker struct {
	taskType string
	stepID   string
	stepNode *model.JobRunStoryNode

	attemptNodes []*model.JobRunStoryNode
	byAttempt    map[int]*model.JobRunStoryNode
}

type rootSourceResolutionTracker struct {
	node         *model.JobRunStoryNode
	attemptNodes []*model.JobRunStoryNode
	byAttempt    map[int]*model.JobRunStoryNode
}

type replayRootSourceResolver struct{}

func NewRecorder(opts Options) *Recorder {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{
		jobKey:            opts.JobKey,
		logger:            logger,
		onChange:          opts.OnChange,
		currentJobAttempt: 0,
		attemptTrees:      make(map[int]*treeBuilder),
		rootsByJobAttempt: make(map[int]*model.JobRunStoryNode),
		attemptTiming:     make(map[int]*jobAttemptTiming),
		resolvedSources:   make(map[int]*compiler.ResolvedRecipeSource),
		sourceNodes:       make(map[int]*rootSourceResolutionTracker),
	}
}

func BuildJobRunStory(ctx context.Context, engine replayJobRunner, jobKey jobdb.JobKey, celProvider template.CELOptionsProvider, logger *slog.Logger, rootResolvers ...compiler.RecipeSourceResolver) (*JobRunStory, error) {
	if engine == nil {
		return nil, fmt.Errorf("engine is required")
	}
	if err := jobKey.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}

	var rootResolver compiler.RecipeSourceResolver
	if len(rootResolvers) > 0 {
		rootResolver = rootResolvers[0]
	}
	if rootResolver == nil {
		rootResolver = replayRootSourceResolver{}
	}

	rec := NewRecorder(Options{JobKey: jobKey, Logger: logger})
	jobWorker := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{
		CELOptionsProvider:     celProvider,
		OnRecipeLoaded:         rec.OnRecipeLoaded,
		OnRecipeSourceResolved: rec.OnRecipeSourceResolved,
		RootSourceResolver:     rootResolver,
		ExecutorFactory:        rec.ExecutorFactory(),
	})

	_, replayErr := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{
		JobKey:    jobKey,
		Observer:  rec.Observer(),
		JobWorker: jobWorker,
	})

	story := rec.Finalize(replayErr)
	if replayErr == nil {
		return story, nil
	}
	if errors.Is(replayErr, context.Canceled) || errors.Is(replayErr, context.DeadlineExceeded) {
		return nil, replayErr
	}
	if errors.Is(replayErr, jobdb.ErrJobNotFound) {
		return nil, replayErr
	}
	if errors.Is(replayErr, jobdb.ErrWorkflowNotDeterministic) {
		return story, replayErr
	}
	if isReplayCacheMissErr(replayErr) {
		return story, nil
	}
	return story, nil
}

func (r *Recorder) Observer() jobworkflow.ReplayObserver {
	return &observer{rec: r}
}

func (r *Recorder) ExecutorFactory() func() compiler.RecipeExecutor {
	return func() compiler.RecipeExecutor {
		tree := r.ensureAttemptTree()
		return newRecordingExecutor(compiler.DefaultRecipeExecutor{}, tree, r)
	}
}

func (r *Recorder) Snapshot() *JobRunStory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buildStoryLocked(nil)
}

func (r *Recorder) Finalize(err error) *JobRunStory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buildStoryLocked(err)
}

func (r *Recorder) OnRecipeLoaded(recipeName string) {
	r.mu.Lock()
	r.recipeNameFromStart = strings.TrimSpace(recipeName)
	r.mu.Unlock()
	r.notify()
}

func (r *Recorder) OnRecipeSourceResolved(resolution compiler.RecipeSourceResolution) {
	r.mu.Lock()
	att := r.currentJobAttempt
	if att <= 0 {
		att = 1
		r.currentJobAttempt = att
	}
	if existing := r.resolvedSources[att]; existing != nil {
		existing.RecipeSourceResolution = resolution
		r.mu.Unlock()
		r.notify()
		return
	}
	resolved := compiler.ResolvedRecipeSource{RecipeSourceResolution: resolution}
	r.resolvedSources[att] = &resolved
	r.mu.Unlock()
	r.notify()
}

func (r *Recorder) OnJobStart(event jobworkflow.JobStartEvent) {
	r.mu.Lock()
	attemptNumber := event.AttemptNumber
	if attemptNumber <= 0 {
		attemptNumber = 1
	}
	r.currentJobAttempt = attemptNumber
	if _, ok := r.attemptTrees[attemptNumber]; !ok {
		r.attemptTrees[attemptNumber] = newTreeBuilder()
	}
	if !event.At.IsZero() {
		t := event.At
		r.ensureAttemptTimingLocked(attemptNumber).startedAt = &t
		if root := r.rootsByJobAttempt[attemptNumber]; root != nil && root.StartedAt == nil {
			root.StartedAt = &t
		}
	}
	r.mu.Unlock()
	r.notify()
}

func (r *Recorder) OnJobEnd(event jobworkflow.JobEndEvent) {
	r.mu.Lock()
	attemptNumber := event.AttemptNumber
	if attemptNumber <= 0 {
		attemptNumber = 1
	}
	if !event.At.IsZero() {
		t := event.At
		r.ensureAttemptTimingLocked(attemptNumber).finishedAt = &t
		if root := r.rootsByJobAttempt[attemptNumber]; root != nil && root.FinishedAt == nil {
			root.FinishedAt = &t
		}
	}
	r.mu.Unlock()
	r.notify()
}

func (r *Recorder) OnTaskStart(event jobworkflow.TaskStartEvent) {
	r.mu.Lock()
	if event.TaskType == compiler.RootSourceResolutionTaskType {
		r.handleRootSourceResolutionTaskStartLocked(event)
		r.mu.Unlock()
		r.notify()
		return
	}

	op := r.currentOpNode
	tree := r.attemptTrees[r.currentJobAttempt]
	if op == nil || tree == nil {
		r.mu.Unlock()
		return
	}

	if r.currentOpSteps == nil {
		r.currentOpSteps = make(map[int64]*ordinalStepTracker)
	}

	tr, ok := r.currentOpSteps[event.Ordinal]
	if !ok || tr == nil {
		stepID := strings.TrimSpace(stepIDFromTaskType(op.OpID, event.TaskType))
		stepNode := tree.newNode(model.JobRunStoryNodeKindOpStep, "step "+stepID)
		stepNode.StepID = stepID
		stepNode.StepType = "other"
		stepNode.Status = model.JobRunStoryNodeStatusRunning
		ord := event.Ordinal
		stepNode.RestartFromOrdinal = &ord
		op.Children = append(op.Children, stepNode)

		tr = &ordinalStepTracker{
			taskType:     strings.TrimSpace(event.TaskType),
			stepID:       stepID,
			stepNode:     stepNode,
			attemptNodes: make([]*model.JobRunStoryNode, 0, 2),
			byAttempt:    make(map[int]*model.JobRunStoryNode),
		}
		r.currentOpSteps[event.Ordinal] = tr
	}

	attNode := tree.newNode(model.JobRunStoryNodeKindOpStep, "step "+strings.TrimSpace(tr.stepID))
	attNode.StepID = strings.TrimSpace(tr.stepID)
	attNode.StepType = "other"
	attNode.Status = model.JobRunStoryNodeStatusRunning
	attNode.Attempt = event.AttemptNumber
	ord := event.Ordinal
	attNode.TaskOrdinal = &ord
	if !event.At.IsZero() {
		t := event.At
		attNode.StartedAt = &t
	}

	applyTaskInputToNode(attNode, event.Input)
	tr.attemptNodes = append(tr.attemptNodes, attNode)
	tr.byAttempt[event.AttemptNumber] = attNode
	r.mu.Unlock()
	r.notify()
}

func (r *Recorder) OnTaskEnd(event jobworkflow.TaskEndEvent) {
	r.mu.Lock()
	if event.TaskType == compiler.RootSourceResolutionTaskType {
		r.handleRootSourceResolutionTaskEndLocked(event)
		r.mu.Unlock()
		r.notify()
		return
	}

	if r.currentOpSteps == nil {
		r.mu.Unlock()
		return
	}

	tr := r.currentOpSteps[event.Ordinal]
	if tr == nil || tr.stepNode == nil || tr.byAttempt == nil {
		r.mu.Unlock()
		return
	}

	attNode := tr.byAttempt[event.AttemptNumber]
	if attNode == nil {
		r.mu.Unlock()
		return
	}
	applyTaskOutputToNode(attNode, r.jobKey.JobId, event.TaskType, event.Output, event.Err)
	if !event.At.IsZero() && attNode.FinishedAt == nil && isTerminal(attNode.Status) {
		t := event.At
		attNode.FinishedAt = &t
	}

	copyAttemptIntoStep(tr.stepNode, attNode)
	prior := make([]*model.JobRunStoryNode, 0, len(tr.attemptNodes))
	for _, n := range tr.attemptNodes {
		if n == nil || n == attNode {
			continue
		}
		prior = append(prior, n)
	}
	tr.stepNode.PriorAttempts = prior
	r.mu.Unlock()
	r.notify()
}

func (r *Recorder) SetRecipeMeta(id, version string) {
	r.mu.Lock()
	if strings.TrimSpace(id) != "" {
		r.recipeID = strings.TrimSpace(id)
	}
	if strings.TrimSpace(version) != "" {
		r.recipeVersion = strings.TrimSpace(version)
	}
	r.mu.Unlock()
}

func (r *Recorder) SetRoot(root *model.JobRunStoryNode) {
	if root == nil {
		return
	}
	r.mu.Lock()
	att := r.currentJobAttempt
	if att <= 0 {
		att = 1
		r.currentJobAttempt = att
	}
	root.JobAttempt = att
	r.rootsByJobAttempt[att] = root
	if tm := r.attemptTiming[att]; tm != nil {
		if root.StartedAt == nil && tm.startedAt != nil {
			t := *tm.startedAt
			root.StartedAt = &t
		}
		if root.FinishedAt == nil && tm.finishedAt != nil {
			t := *tm.finishedAt
			root.FinishedAt = &t
		}
	}
	r.mu.Unlock()
	r.notify()
}

func (r *Recorder) SetCurrentOpNode(op *model.JobRunStoryNode) {
	r.mu.Lock()
	r.currentOpNode = op
	if op == nil {
		r.currentOpSteps = nil
	} else {
		r.currentOpSteps = make(map[int64]*ordinalStepTracker)
	}
	r.mu.Unlock()
}

func (r *Recorder) ensureAttemptTree() *treeBuilder {
	r.mu.Lock()
	defer r.mu.Unlock()
	att := r.currentJobAttempt
	if att <= 0 {
		att = 1
		r.currentJobAttempt = att
	}
	if t, ok := r.attemptTrees[att]; ok && t != nil {
		return t
	}
	t := newTreeBuilder()
	r.attemptTrees[att] = t
	return t
}

func (r *Recorder) ensureAttemptTimingLocked(attemptNumber int) *jobAttemptTiming {
	if attemptNumber <= 0 {
		attemptNumber = 1
	}
	if tm, ok := r.attemptTiming[attemptNumber]; ok && tm != nil {
		return tm
	}
	tm := &jobAttemptTiming{}
	r.attemptTiming[attemptNumber] = tm
	return tm
}

func (r *Recorder) ensureRootSourceResolutionTrackerLocked(attempt int) *rootSourceResolutionTracker {
	if attempt <= 0 {
		attempt = 1
	}
	if tracker, ok := r.sourceNodes[attempt]; ok && tracker != nil {
		return tracker
	}
	tracker := &rootSourceResolutionTracker{
		attemptNodes: make([]*model.JobRunStoryNode, 0, 2),
		byAttempt:    make(map[int]*model.JobRunStoryNode),
	}
	r.sourceNodes[attempt] = tracker
	return tracker
}

func (r *Recorder) handleRootSourceResolutionTaskStartLocked(event jobworkflow.TaskStartEvent) {
	attempt := r.currentJobAttempt
	if attempt <= 0 {
		attempt = 1
		r.currentJobAttempt = attempt
	}

	tree := r.attemptTrees[attempt]
	if tree == nil {
		tree = newTreeBuilder()
		r.attemptTrees[attempt] = tree
	}

	tracker := r.ensureRootSourceResolutionTrackerLocked(attempt)
	if tracker.node == nil {
		node := tree.newNode(model.JobRunStoryNodeKindRecipeSourceResolution, "recipe source resolution")
		node.Status = model.JobRunStoryNodeStatusRunning
		ord := event.Ordinal
		node.RestartFromOrdinal = &ord
		tracker.node = node
	}

	attNode := tree.newNode(model.JobRunStoryNodeKindRecipeSourceResolution, "recipe source resolution")
	attNode.Status = model.JobRunStoryNodeStatusRunning
	attNode.Attempt = event.AttemptNumber
	ord := event.Ordinal
	attNode.TaskOrdinal = &ord
	if !event.At.IsZero() {
		t := event.At
		attNode.StartedAt = &t
	}
	applyTaskInputToNode(attNode, event.Input)
	tracker.attemptNodes = append(tracker.attemptNodes, attNode)
	tracker.byAttempt[event.AttemptNumber] = attNode
}

func (r *Recorder) handleRootSourceResolutionTaskEndLocked(event jobworkflow.TaskEndEvent) {
	attempt := r.currentJobAttempt
	if attempt <= 0 {
		attempt = 1
		r.currentJobAttempt = attempt
	}

	tracker := r.sourceNodes[attempt]
	if tracker == nil || tracker.node == nil || tracker.byAttempt == nil {
		return
	}

	attNode := tracker.byAttempt[event.AttemptNumber]
	if attNode == nil {
		return
	}
	applyTaskOutputToNode(attNode, r.jobKey.JobId, event.TaskType, event.Output, event.Err)
	if !event.At.IsZero() && attNode.FinishedAt == nil && isTerminal(attNode.Status) {
		t := event.At
		attNode.FinishedAt = &t
	}

	copyAttemptIntoStep(tracker.node, attNode)
	prior := make([]*model.JobRunStoryNode, 0, len(tracker.attemptNodes))
	for _, node := range tracker.attemptNodes {
		if node == nil || node == attNode {
			continue
		}
		prior = append(prior, node)
	}
	tracker.node.PriorAttempts = prior

	if event.Err == nil {
		if source := parseResolvedRecipeSource(event.Output); source != nil {
			r.resolvedSources[attempt] = source
		}
	}
}

func (r *Recorder) buildStoryLocked(replayErr error) *JobRunStory {
	earliestStart := time.Time{}
	latestFinish := time.Time{}
	for _, tm := range r.attemptTiming {
		if tm == nil {
			continue
		}
		if tm.startedAt != nil && !tm.startedAt.IsZero() {
			if earliestStart.IsZero() || tm.startedAt.Before(earliestStart) {
				earliestStart = *tm.startedAt
			}
		}
		if tm.finishedAt != nil && !tm.finishedAt.IsZero() {
			if latestFinish.IsZero() || tm.finishedAt.After(latestFinish) {
				latestFinish = *tm.finishedAt
			}
		}
	}

	rootsByAttempt := r.buildRootsWithSourceResolutionLocked()

	latestAttempt := 0
	for att := range rootsByAttempt {
		if att > latestAttempt {
			latestAttempt = att
		}
	}
	root := rootsByAttempt[latestAttempt]
	if root != nil && len(rootsByAttempt) > 1 {
		keys := make([]int, 0, len(rootsByAttempt))
		for k := range rootsByAttempt {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		past := make([]*model.JobRunStoryNode, 0, len(keys)-1)
		for _, k := range keys {
			if k == latestAttempt {
				continue
			}
			if rootsByAttempt[k] != nil {
				past = append(past, rootsByAttempt[k])
			}
		}
		root.PastAttempts = past
	}

	recipeID := strings.TrimSpace(r.recipeID)
	recipeName := toRecipeNameFromIDFallback(recipeID, r.recipeNameFromStart)

	status := mapStoryStatusFromReplayErr(replayErr)
	if latestAttempt > 0 {
		if tm := r.attemptTiming[latestAttempt]; tm != nil && tm.startedAt != nil && tm.finishedAt == nil {
			status = model.WorkflowStatusRunning
		}
	}
	if root != nil && root.Status == model.JobRunStoryNodeStatusRunning {
		status = model.WorkflowStatusRunning
	}
	if status == model.WorkflowStatusRunning && root != nil {
		root.Status = model.JobRunStoryNodeStatusRunning
	}

	sourceKind := "jobStartArtifact"
	sourceArtifact := recipeSourceArtifactName(r.recipeNameFromStart)
	if r.sourceNodes[latestAttempt] != nil || r.resolvedSources[latestAttempt] != nil {
		sourceKind = "jobStartRef"
		sourceArtifact = ""
	}

	source := model.JobRunStoryRecipeSource{
		Kind:         sourceKind,
		ArtifactName: sourceArtifact,
	}
	if resolved := r.resolvedSources[latestAttempt]; resolved != nil {
		source.SubmittedSelector = strings.TrimSpace(resolved.SubmittedSelector)
		source.ResolvedSelector = strings.TrimSpace(resolved.ResolvedSelector)
		source.ResolvedCommit = strings.TrimSpace(resolved.ResolvedCommit)
		source.RecipeYAML = resolved.RecipeYAML
	}
	if sourceNode := r.resolutionNodeForAttemptLocked(latestAttempt); sourceNode != nil && sourceNode.TaskOrdinal != nil {
		ord := *sourceNode.TaskOrdinal
		source.ResolutionTaskOrdinal = &ord
	}

	story := &model.JobRunStory{
		JobID:              r.jobKey.JobId,
		InvocationSequence: 0,
		Recipe: model.JobRunStoryRecipe{
			ID:      recipeID,
			Name:    recipeName,
			Version: strings.TrimSpace(r.recipeVersion),
			Source:  source,
		},
		Status:     status,
		StartedAt:  earliestStart,
		FinishedAt: nil,
		Root:       root,
	}
	if !latestFinish.IsZero() && status != model.WorkflowStatusRunning {
		t := latestFinish
		story.FinishedAt = &t
	}
	if root != nil {
		story.InvocationSequence = root.InvokeSeq
	}

	inferStartedTimes(story.Root)
	inferFinishedTimes(story.Root, story.FinishedAt)

	return story
}

func (r *Recorder) buildRootsWithSourceResolutionLocked() map[int]*model.JobRunStoryNode {
	roots := make(map[int]*model.JobRunStoryNode, len(r.rootsByJobAttempt))
	for att, root := range r.rootsByJobAttempt {
		roots[att] = cloneNode(root)
	}

	for att := range r.sourceNodes {
		if _, ok := roots[att]; !ok {
			roots[att] = nil
		}
	}

	for att, root := range roots {
		sourceNode := cloneNode(r.resolutionNodeForAttemptLocked(att))
		if sourceNode == nil {
			continue
		}
		if root == nil {
			root = r.syntheticRootForAttemptLocked(att, sourceNode)
		} else {
			root.Children = append([]*model.JobRunStoryNode{sourceNode}, root.Children...)
		}
		roots[att] = root
	}

	return roots
}

func (r *Recorder) resolutionNodeForAttemptLocked(attempt int) *model.JobRunStoryNode {
	tracker := r.sourceNodes[attempt]
	if tracker == nil || tracker.node == nil {
		return nil
	}
	return tracker.node
}

func (r *Recorder) syntheticRootForAttemptLocked(attempt int, sourceNode *model.JobRunStoryNode) *model.JobRunStoryNode {
	recipeID := strings.TrimSpace(r.recipeID)
	recipeName := toRecipeNameFromIDFallback(recipeID, r.recipeNameFromStart)
	if recipeName == "" {
		recipeName = "recipe"
	}

	root := &model.JobRunStoryNode{
		ID:            fmt.Sprintf("synthetic_root_%d", attempt),
		Kind:          model.JobRunStoryNodeKindRecipe,
		Title:         "recipe " + recipeName,
		Status:        sourceNode.Status,
		Path:          make([]string, 0, 8),
		Attempt:       1,
		PriorAttempts: make([]*model.JobRunStoryNode, 0),
		ArtifactKeys:  make([]jobdb.ArtifactKey, 0),
		ArtifactRefs:  make([]recipeartifacts.Ref, 0),
		Children:      []*model.JobRunStoryNode{sourceNode},
		RecipeID:      recipeID,
		JobAttempt:    attempt,
	}
	if root.StartedAt == nil && sourceNode.StartedAt != nil {
		t := *sourceNode.StartedAt
		root.StartedAt = &t
	}
	if root.FinishedAt == nil && sourceNode.FinishedAt != nil {
		t := *sourceNode.FinishedAt
		root.FinishedAt = &t
	}
	if tm := r.attemptTiming[attempt]; tm != nil {
		if root.StartedAt == nil && tm.startedAt != nil {
			t := *tm.startedAt
			root.StartedAt = &t
		}
		if root.FinishedAt == nil && tm.finishedAt != nil {
			t := *tm.finishedAt
			root.FinishedAt = &t
		}
	}
	return root
}

func (r *Recorder) notify() {
	if r == nil || r.onChange == nil {
		return
	}
	r.onChange(r.Snapshot())
}

func mapStoryStatusFromReplayErr(err error) model.WorkflowStatus {
	if err == nil {
		return model.WorkflowStatusCompleted
	}
	if isReplayCacheMissErr(err) {
		return model.WorkflowStatusRunning
	}
	var te jobdb.TimeoutError
	if errors.As(err, &te) {
		return model.WorkflowStatusTimedOut
	}
	return model.WorkflowStatusFailed
}

func recipeSourceArtifactName(recipeName string) string {
	recipeName = strings.TrimSpace(recipeName)
	if recipeName == "" {
		return ""
	}
	return recipeName + starter.RecipeArtifactSuffix
}

func toRecipeNameFromIDFallback(recipeID, recipeName string) string {
	recipeID = strings.TrimSpace(recipeID)
	if recipeID != "" {
		return recipeID
	}
	return strings.TrimSpace(recipeName)
}

func (replayRootSourceResolver) Resolve(context.Context, string, string) (compiler.RecipeSourceResolution, error) {
	return compiler.RecipeSourceResolution{}, fmt.Errorf("recipe source resolver unavailable during replay")
}

func (replayRootSourceResolver) Load(context.Context, string, compiler.RecipeSourceResolution) (recipe.Recipe, error) {
	return recipe.Recipe{}, fmt.Errorf("recipe source loader unavailable during replay")
}

type observer struct {
	rec *Recorder
}

func (o *observer) OnJobStart(event jobworkflow.JobStartEvent) {
	if o == nil || o.rec == nil {
		return
	}
	o.rec.OnJobStart(event)
}

func (o *observer) OnTaskStart(event jobworkflow.TaskStartEvent) {
	if o == nil || o.rec == nil {
		return
	}
	o.rec.OnTaskStart(event)
}

func (o *observer) OnTaskEnd(event jobworkflow.TaskEndEvent) {
	if o == nil || o.rec == nil {
		return
	}
	o.rec.OnTaskEnd(event)
}

func (o *observer) OnJobEnd(event jobworkflow.JobEndEvent) {
	if o == nil || o.rec == nil {
		return
	}
	o.rec.OnJobEnd(event)
}

var _ jobworkflow.ReplayObserver = (*observer)(nil)

type recordingExecutor struct {
	inner compiler.DefaultRecipeExecutor
	tree  *treeBuilder
	root  *model.JobRunStoryNode
	rec   *Recorder
}

func newRecordingExecutor(inner compiler.DefaultRecipeExecutor, tree *treeBuilder, rec *Recorder) *recordingExecutor {
	return &recordingExecutor{inner: inner, tree: tree, rec: rec}
}

func (e *recordingExecutor) ExecuteRecipe(ctx coreworkflow.Context, r recipe.Recipe, rawRecipeInputs map[string]interface{}, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, opts ...compiler.ExecutionOptions) (map[string]interface{}, []jobdb.Artifact, error) {
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

	var execOpts compiler.ExecutionOptions
	if len(opts) > 0 {
		execOpts = opts[0]
	}
	execOpts.DiagnosticsObserver = chainDiagnosticsObservers(execOpts.DiagnosticsObserver, e)
	out, arts, err := e.inner.WithDelegate(e).ExecuteRecipe(ctx, r, rawRecipeInputs, execCtx, commitContext, execOpts)
	if err != nil {
		root.Status = statusFromErr(err, root.Status)
		root.Output = out
		e.tree.pop()
		if e.rec != nil {
			e.rec.notify()
		}
		return out, arts, err
	}
	root.Status = model.JobRunStoryNodeStatusSucceeded
	root.Output = out
	e.tree.pop()
	if e.rec != nil {
		e.rec.notify()
	}
	return out, arts, nil
}

func (e *recordingExecutor) VarsResolved(_ template.ScopeType, _ string, vars map[string]interface{}) {
	if e == nil || e.tree == nil {
		return
	}
	current := e.tree.current()
	if current == nil {
		return
	}
	if redacted, ok := redact.Value("vars", vars).(map[string]interface{}); ok {
		current.RenderedVars = redacted
	}
	if e.rec != nil {
		e.rec.notify()
	}
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

		if cur := e.tree.current(); cur != nil && cur.Kind == model.JobRunStoryNodeKindState && cur.Status == model.JobRunStoryNodeStatusRunning {
			cur.Title = title
			setStoryNodePath(cur, nodePath)
			err := e.inner.WithDelegate(e).ExecuteNode(ctx, parentResCtx, n)
			if err != nil {
				cur.Status = statusFromErr(err, cur.Status)
				if e.rec != nil {
					e.rec.notify()
				}
				return err
			}
			return nil
		}

		stateNode := e.tree.newNode(model.JobRunStoryNodeKindState, title)
		stateNode.Status = model.JobRunStoryNodeStatusRunning
		setStoryNodePath(stateNode, nodePath)
		e.tree.push("state", stateNode)
		if e.rec != nil {
			e.rec.notify()
		}

		err := e.inner.WithDelegate(e).ExecuteNode(ctx, parentResCtx, n)
		if err != nil {
			stateNode.Status = statusFromErr(err, stateNode.Status)
			e.tree.pop()
			if e.rec != nil {
				e.rec.notify()
			}
			return err
		}
		stateNode.Status = deriveContainerStatus(stateNode.Children)
		if stateNode.Status == model.JobRunStoryNodeStatusUnknown {
			stateNode.Status = model.JobRunStoryNodeStatusSucceeded
		}
		e.tree.pop()
		if e.rec != nil {
			e.rec.notify()
		}
		return nil
	}

	return e.inner.WithDelegate(e).ExecuteNode(ctx, parentResCtx, n)
}

func (e *recordingExecutor) ExecuteSequence(ctx coreworkflow.Context, rCtx *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, sequence []recipe.Node) error {
	seqID := template.ScopeID(metadata, "", template.ScopeSequence)
	node := e.tree.newNode(model.JobRunStoryNodeKindSequence, "sequence "+seqID)
	node.SequenceID = seqID
	node.Status = model.JobRunStoryNodeStatusRunning
	node.InlineStack = storyInlineStack(rCtx, metadata, seqID)
	if rCtx != nil {
		if resolved, err := rCtx.ResolveMap(metadata.Inputs); err == nil {
			node.Input = resolved
		}
	}
	e.tree.push("sequence:"+seqID, node)
	if e.rec != nil {
		e.rec.notify()
	}

	err := e.inner.WithDelegate(e).ExecuteSequence(ctx, rCtx, metadata, outputTemplate, sequence)
	if err != nil {
		node.Status = statusFromErr(err, node.Status)
		e.tree.pop()
		if e.rec != nil {
			e.rec.notify()
		}
		return err
	}
	node.Status = deriveContainerStatus(node.Children)
	if rCtx != nil {
		node.Output = rCtx.GetLastExecution()
	}
	e.tree.pop()
	if e.rec != nil {
		e.rec.notify()
	}
	return nil
}

func (e *recordingExecutor) ExecuteStateMachine(ctx coreworkflow.Context, parentContext *template.ResolutionContext, metadata recipe.NodeMetadata, outputTemplate map[string]interface{}, stateMap *recipe.StateMap, opts ...compiler.ExecutionOptions) error {
	smID := template.ScopeID(metadata, "", template.ScopeStateMachine)
	node := e.tree.newNode(model.JobRunStoryNodeKindStateMachine, "stateMachine "+smID)
	node.StateMachineID = smID
	node.Status = model.JobRunStoryNodeStatusRunning
	node.InlineStack = storyInlineStack(parentContext, metadata, smID)
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
	if e.rec != nil {
		e.rec.notify()
	}

	stObs := newStoryStateObserver(e.tree, e.rec)
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
		if e.rec != nil {
			e.rec.notify()
		}
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
	if e.rec != nil {
		e.rec.notify()
	}
	return nil
}

func (e *recordingExecutor) ExecuteOp(ctx coreworkflow.Context, parentResolutionContext *template.ResolutionContext, metadata recipe.NodeMetadata, opID string) error {
	opID = strings.TrimSpace(opID)
	opNode := e.tree.newNode(model.JobRunStoryNodeKindOp, "op "+opID)
	opNode.OpID = opID
	opNode.OpType = "custom"
	opNode.Status = model.JobRunStoryNodeStatusRunning
	opNode.InlineStack = storyInlineStack(parentResolutionContext, metadata, opID)
	e.tree.push("op:"+opID, opNode)

	if e.rec != nil {
		e.rec.SetCurrentOpNode(opNode)
		e.rec.notify()
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
		if e.rec != nil {
			e.rec.notify()
		}
		return err
	}

	first := findFirstChildStep(opNode.Children)
	last := findLastChildStep(opNode.Children)
	if first != nil {
		opNode.Input = first.Input
	}
	if last != nil {
		opNode.Output = last.Output
	}

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
			opNode.InlineStack = cloneInlineBoundaryStack(ch.InlineStack)
			opNode.Status = ch.Status
			e.tree.pop()
			if e.rec != nil {
				e.rec.notify()
			}
			return nil
		}
	}

	opNode.Status = deriveContainerStatus(opNode.Children)
	if opNode.Status == model.JobRunStoryNodeStatusUnknown {
		opNode.Status = model.JobRunStoryNodeStatusSucceeded
	}

	e.tree.pop()
	if e.rec != nil {
		e.rec.notify()
	}
	return nil
}

func (e *recordingExecutor) ExecuteChildGroup(ctx coreworkflow.Context, parent *template.ResolutionContext, metadata recipe.NodeMetadata, group recipe.ChildGroupData) error {
	return e.inner.WithDelegate(e).ExecuteChildGroup(ctx, parent, metadata, group)
}

func (e *recordingExecutor) ensureReplayMissStepNode(opNode *model.JobRunStoryNode, opID string, err error) {
	if opNode == nil {
		return
	}

	var miss jobworkflow.ReplayCacheMissError
	if !errors.As(err, &miss) || miss.Reason != jobworkflow.ReplayCacheMissTaskResultMissing {
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

type storyStateObserver struct {
	tree *treeBuilder
	rec  *Recorder

	seenAnyState bool

	currentState   *model.JobRunStoryNode
	currentExited  bool
	stateNeedsPop  bool
	transitionEval *model.JobRunStoryNode
}

func newStoryStateObserver(tree *treeBuilder, rec *Recorder) *storyStateObserver {
	return &storyStateObserver{tree: tree, rec: rec}
}

func (o *storyStateObserver) StateEntered(stateName string) {
	if o == nil || o.tree == nil {
		return
	}

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
	if o.rec != nil {
		o.rec.notify()
	}
}

func (o *storyStateObserver) StateExited(_ string) {
	if o == nil {
		return
	}
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
	if o.rec != nil {
		o.rec.notify()
	}
}

func (o *storyStateObserver) TransitionSelected(fromState string, toState string, payload map[string]interface{}) {
	if o == nil || o.tree == nil || o.currentState == nil {
		return
	}
	te := o.transitionEval
	if te == nil {
		return
	}
	if te.Decision == nil {
		to := strings.TrimSpace(toState)
		te.Decision = &model.JobRunStoryTransitionDecision{
			Kind:      "state",
			ToStateID: &to,
		}
	}
	if te.Decision != nil {
		if redacted, ok := redact.Value("transition.payload", payload).(map[string]interface{}); ok {
			te.Decision.Payload = redacted
		}
	}
	for i := len(te.Evaluations) - 1; i >= 0; i-- {
		if strings.TrimSpace(te.Evaluations[i].ToStateID) == strings.TrimSpace(toState) {
			te.FromStateID = strings.TrimSpace(fromState)
			break
		}
	}
	if o.rec != nil {
		o.rec.notify()
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

	if te := o.transitionEval; te != nil {
		if len(o.currentState.Path) > 0 {
			te.Path = append(append([]string{}, o.currentState.Path...), "transitionEval")
		}
		if te.Decision == nil {
			te.Decision = &model.JobRunStoryTransitionDecision{Kind: "fallthrough"}
		}
	}

	if o.currentExited && o.currentState.Status == model.JobRunStoryNodeStatusRunning {
		o.currentState.Status = deriveContainerStatus(o.currentState.Children)
		if o.currentState.Status == model.JobRunStoryNodeStatusUnknown {
			o.currentState.Status = model.JobRunStoryNodeStatusSucceeded
		}
	}

	if o.stateNeedsPop {
		if cur := o.tree.current(); cur == o.currentState {
			o.tree.pop()
		}
	}

	o.currentState = nil
	o.currentExited = false
	o.stateNeedsPop = false
	o.transitionEval = nil
	if o.rec != nil {
		o.rec.notify()
	}
}

type chainedStateObserver struct {
	a compiler.StateObserver
	b compiler.StateObserver
}

type chainedDiagnosticsObserver struct {
	a template.DiagnosticsObserver
	b template.DiagnosticsObserver
}

func chainDiagnosticsObservers(a template.DiagnosticsObserver, b template.DiagnosticsObserver) template.DiagnosticsObserver {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return &chainedDiagnosticsObserver{a: a, b: b}
}

func (o *chainedDiagnosticsObserver) VarsResolved(scope template.ScopeType, nodePath string, vars map[string]interface{}) {
	if o == nil {
		return
	}
	if o.a != nil {
		o.a.VarsResolved(scope, nodePath, vars)
	}
	if o.b != nil {
		o.b.VarsResolved(scope, nodePath, vars)
	}
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

func (o *chainedStateObserver) TransitionSelected(fromState string, toState string, payload map[string]interface{}) {
	if o == nil {
		return
	}
	if selected, ok := o.a.(compiler.TransitionSelectionObserver); ok {
		selected.TransitionSelected(fromState, toState, payload)
	}
	if selected, ok := o.b.(compiler.TransitionSelectionObserver); ok {
		selected.TransitionSelected(fromState, toState, payload)
	}
}

var _ compiler.StateObserver = (*storyStateObserver)(nil)
var _ compiler.TransitionSelectionObserver = (*storyStateObserver)(nil)
var _ compiler.StateObserver = (*chainedStateObserver)(nil)
var _ compiler.TransitionSelectionObserver = (*chainedStateObserver)(nil)

type treeBuilder struct {
	nextID   int64
	segments []string
	stack    []*model.JobRunStoryNode
}

func newTreeBuilder() *treeBuilder {
	return &treeBuilder{
		nextID:   1,
		segments: make([]string, 0, 16),
		stack:    make([]*model.JobRunStoryNode, 0, 16),
	}
}

func (b *treeBuilder) newNode(kind model.JobRunStoryNodeKind, title string) *model.JobRunStoryNode {
	id := fmt.Sprintf("n_%d", b.nextID)
	b.nextID++
	return &model.JobRunStoryNode{
		ID:            id,
		Kind:          kind,
		Title:         title,
		Status:        model.JobRunStoryNodeStatusUnknown,
		Path:          make([]string, 0, 8),
		Attempt:       1,
		PriorAttempts: make([]*model.JobRunStoryNode, 0),
		ArtifactKeys:  make([]jobdb.ArtifactKey, 0),
		ArtifactRefs:  make([]recipeartifacts.Ref, 0),
		Children:      make([]*model.JobRunStoryNode, 0),
	}
}

func (b *treeBuilder) push(segment string, n *model.JobRunStoryNode) {
	if len(b.stack) > 0 {
		parent := b.stack[len(b.stack)-1]
		parent.Children = append(parent.Children, n)
	}
	b.segments = append(b.segments, segment)
	b.stack = append(b.stack, n)
}

func (b *treeBuilder) pop() *model.JobRunStoryNode {
	if len(b.stack) == 0 {
		return nil
	}
	n := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	if len(b.segments) > 0 {
		b.segments = b.segments[:len(b.segments)-1]
	}
	return n
}

func (b *treeBuilder) current() *model.JobRunStoryNode {
	if len(b.stack) == 0 {
		return nil
	}
	return b.stack[len(b.stack)-1]
}

func inferFinishedTimes(root *model.JobRunStoryNode, jobFinishedAt *time.Time) {
	if root == nil {
		return
	}
	inferFinishedTimesRec(root, jobFinishedAt)
}

func inferStartedTimes(root *model.JobRunStoryNode) {
	if root == nil {
		return
	}
	inferStartedTimesRec(root)
}

func inferStartedTimesRec(n *model.JobRunStoryNode) {
	if n == nil {
		return
	}
	for _, ch := range n.Children {
		inferStartedTimesRec(ch)
	}

	if n.StartedAt != nil {
		return
	}

	earliest := (*time.Time)(nil)
	for _, ch := range n.Children {
		if ch == nil || ch.StartedAt == nil || ch.StartedAt.IsZero() {
			continue
		}
		if earliest == nil || ch.StartedAt.Before(*earliest) {
			t := *ch.StartedAt
			earliest = &t
		}
	}
	if earliest != nil {
		t := *earliest
		n.StartedAt = &t
	}
}

func inferFinishedTimesRec(n *model.JobRunStoryNode, jobFinishedAt *time.Time) {
	if n == nil {
		return
	}
	for _, ch := range n.Children {
		inferFinishedTimesRec(ch, jobFinishedAt)
	}

	for i := 0; i < len(n.Children)-1; i++ {
		cur := n.Children[i]
		next := n.Children[i+1]
		if cur == nil || next == nil || cur.FinishedAt != nil || !isTerminal(cur.Status) {
			continue
		}
		if next.StartedAt != nil {
			t := *next.StartedAt
			cur.FinishedAt = &t
		}
	}

	if len(n.Children) > 0 {
		last := n.Children[len(n.Children)-1]
		if last != nil && last.FinishedAt == nil && isTerminal(last.Status) {
			if n.FinishedAt != nil {
				t := *n.FinishedAt
				last.FinishedAt = &t
			} else if jobFinishedAt != nil {
				t := *jobFinishedAt
				last.FinishedAt = &t
			}
		}
	}

	if n.FinishedAt == nil && isTerminal(n.Status) {
		if len(n.Children) > 0 {
			last := n.Children[len(n.Children)-1]
			if last != nil {
				if last.FinishedAt != nil {
					t := *last.FinishedAt
					n.FinishedAt = &t
				} else if last.StartedAt != nil {
					t := *last.StartedAt
					n.FinishedAt = &t
				}
			}
		} else if jobFinishedAt != nil {
			t := *jobFinishedAt
			n.FinishedAt = &t
		}
	}
}

func isTerminal(st model.JobRunStoryNodeStatus) bool {
	switch st {
	case model.JobRunStoryNodeStatusSucceeded, model.JobRunStoryNodeStatusFailed, model.JobRunStoryNodeStatusCanceled, model.JobRunStoryNodeStatusSkipped:
		return true
	default:
		return false
	}
}

func splitInvocationNodePath(nodePath string) []string {
	nodePath = strings.TrimSpace(nodePath)
	if nodePath == "" {
		return make([]string, 0)
	}
	parts := strings.Split(nodePath, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func setStoryNodePath(n *model.JobRunStoryNode, invocationNodePath string, extra ...string) {
	if n == nil {
		return
	}
	path := splitInvocationNodePath(invocationNodePath)
	for _, seg := range extra {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		path = append(path, seg)
	}
	n.Path = path
}

func storyInlineStack(parent *template.ResolutionContext, metadata recipe.NodeMetadata, boundaryID string) []contextual.InlineBoundaryFrame {
	var out []contextual.InlineBoundaryFrame
	parentPath := ""
	if parent != nil {
		taskCtx := parent.TaskExecutionContext()
		out = cloneInlineBoundaryStack(taskCtx.InlineStack)
		parentPath = strings.TrimSpace(taskCtx.Invocation.NodePath)
	}
	if metadata.Internal == nil || metadata.Internal.Inline == nil {
		return out
	}
	inline := metadata.Internal.Inline
	boundaryPath := strings.TrimSpace(boundaryID)
	if parentPath != "" {
		if boundaryPath != "" {
			boundaryPath = parentPath + "/" + boundaryPath
		} else {
			boundaryPath = parentPath
		}
	}
	out = append(out, contextual.InlineBoundaryFrame{
		CallsitePath:      inline.CallsitePath,
		BoundaryNodePath:  boundaryPath,
		RecipeID:          inline.RecipeID,
		RecipeVersion:     inline.RecipeVersion,
		SourceKind:        inline.Source.SourceKind,
		SubmittedSelector: inline.Source.SubmittedSelector,
		ResolvedSelector:  inline.Source.ResolvedSelector,
		ResolvedCommit:    inline.Source.ResolvedCommit,
		ContentSHA256:     inline.ContentSHA256,
	})
	return out
}

func cloneInlineBoundaryStack(in []contextual.InlineBoundaryFrame) []contextual.InlineBoundaryFrame {
	if len(in) == 0 {
		return nil
	}
	out := make([]contextual.InlineBoundaryFrame, len(in))
	copy(out, in)
	return out
}

func statusFromErr(err error, fallback model.JobRunStoryNodeStatus) model.JobRunStoryNodeStatus {
	if err == nil {
		return fallback
	}
	if isReplayCacheMissErr(err) {
		return model.JobRunStoryNodeStatusRunning
	}
	return model.JobRunStoryNodeStatusFailed
}

func isReplayCacheMissErr(err error) bool {
	if err == nil {
		return false
	}
	var miss jobworkflow.ReplayCacheMissError
	if errors.As(err, &miss) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "replay cache miss:")
}

func deriveContainerStatus(children []*model.JobRunStoryNode) model.JobRunStoryNodeStatus {
	if len(children) == 0 {
		return model.JobRunStoryNodeStatusUnknown
	}
	last := children[len(children)-1]
	if last != nil && last.Status == model.JobRunStoryNodeStatusRunning {
		return model.JobRunStoryNodeStatusRunning
	}
	for _, ch := range children {
		if ch != nil && ch.Status == model.JobRunStoryNodeStatusFailed {
			return model.JobRunStoryNodeStatusFailed
		}
	}
	return model.JobRunStoryNodeStatusSucceeded
}

func stepIDFromTaskType(opID, taskType string) string {
	taskType = strings.TrimSpace(taskType)
	if taskType == "" {
		return ""
	}
	parts := strings.SplitN(taskType, ":", 2)
	if len(parts) != 2 {
		return taskType
	}
	if strings.TrimSpace(parts[0]) != strings.TrimSpace(opID) {
		return parts[1]
	}
	return parts[1]
}

func unwrapTaskEnvelopePayload(raw []byte) (any, bool) {
	var env coretasks.OutputEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false
	}
	if env.Version != coretasks.OutputEnvelopeVersion || strings.TrimSpace(string(env.Kind)) == "" {
		return nil, false
	}
	if len(env.Payload) == 0 {
		return nil, true
	}
	var payload any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func outputKindFromRaw(raw []byte) (coretasks.OutputKind, bool) {
	var env coretasks.OutputEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return "", false
	}
	if env.Version != coretasks.OutputEnvelopeVersion {
		return "", false
	}
	return env.Kind, true
}

func findFirstChildStep(children []*model.JobRunStoryNode) *model.JobRunStoryNode {
	for _, ch := range children {
		if ch != nil && ch.Kind == model.JobRunStoryNodeKindOpStep {
			return ch
		}
	}
	return nil
}

func findLastChildStep(children []*model.JobRunStoryNode) *model.JobRunStoryNode {
	for i := len(children) - 1; i >= 0; i-- {
		ch := children[i]
		if ch != nil && ch.Kind == model.JobRunStoryNodeKindOpStep {
			return ch
		}
	}
	return nil
}

func parseResolvedRecipeSource(td jobdb.TaskData) *compiler.ResolvedRecipeSource {
	if td == nil {
		return nil
	}
	source, err := compiler.ParseResolvedRecipeSourceTaskData(td)
	if err != nil {
		return nil
	}
	return source
}

func copyAttemptIntoStep(dst, src *model.JobRunStoryNode) {
	if dst == nil || src == nil {
		return
	}
	dst.Kind = src.Kind
	dst.Title = src.Title
	dst.Status = src.Status
	dst.StartedAt = src.StartedAt
	dst.FinishedAt = src.FinishedAt
	dst.Attempt = src.Attempt
	dst.Input = src.Input
	dst.Output = src.Output
	dst.ArtifactKeys = src.ArtifactKeys
	dst.ArtifactRefs = src.ArtifactRefs
	dst.TaskOrdinal = src.TaskOrdinal
	dst.Error = src.Error
	dst.InvokeSeq = src.InvokeSeq
	dst.Path = append([]string{}, src.Path...)
	dst.InlineStack = cloneInlineBoundaryStack(src.InlineStack)
}

func applyTaskInputToNode(n *model.JobRunStoryNode, td jobdb.TaskData) {
	if n == nil || td == nil {
		return
	}
	raw, err := td.GetData()
	if err != nil || len(raw) == 0 {
		return
	}

	var req ops.ActivityInvocationRequest
	if json.Unmarshal(raw, &req) == nil {
		n.Input = req.Input
		n.InvokeSeq = req.GitTaskContext.InvokeSeq
		if strings.TrimSpace(req.GitTaskContext.NodePath) != "" {
			if n.Kind == model.JobRunStoryNodeKindOpStep && strings.TrimSpace(n.StepID) != "" {
				setStoryNodePath(n, req.GitTaskContext.NodePath, "step:"+strings.TrimSpace(n.StepID))
			} else {
				setStoryNodePath(n, req.GitTaskContext.NodePath)
			}
		}
		n.InlineStack = cloneInlineBoundaryStack(req.GitTaskContext.InlineStack)
		return
	}

	if payload, ok := unwrapTaskEnvelopePayload(raw); ok {
		n.Input = payload
		return
	}

	var anyRaw any
	if json.Unmarshal(raw, &anyRaw) == nil {
		n.Input = anyRaw
	}
}

func applyTaskOutputToNode(n *model.JobRunStoryNode, jobID string, taskType string, td jobdb.TaskData, err error) {
	if n == nil {
		return
	}

	if mismatch, ok := jobworkflow.UnexpectedChapter(err); ok && mismatch.CachedTaskDataErr() == nil && mismatch.CachedTaskData() != nil {
		cached := mismatch.CachedTaskData()
		raw, rerr := cached.GetData()
		if rerr == nil && len(raw) > 0 {
			kind, ok := outputKindFromRaw(raw)
			if ok && kind == coretasks.OutputKindContextPatch {
				td = cached
				err = nil
			}
		}
	}

	if err != nil {
		if isReplayCacheMissErr(err) {
			n.Status = model.JobRunStoryNodeStatusRunning
			n.Error = &model.JobRunStoryError{Message: err.Error()}
		} else {
			n.Status = model.JobRunStoryNodeStatusFailed
			n.Error = &model.JobRunStoryError{Message: err.Error()}
		}
	} else {
		n.Status = model.JobRunStoryNodeStatusSucceeded
	}

	if td != nil {
		raw, derr := td.GetData()
		if derr == nil && len(raw) > 0 {
			var outEnv coretasks.OutputEnvelope
			if json.Unmarshal(raw, &outEnv) == nil && outEnv.Version == coretasks.OutputEnvelopeVersion {
				switch outEnv.Kind {
				case coretasks.OutputKindActivityInvocationOutput:
					var env ops.ActivityInvocationOutput
					if outEnv.DecodePayload(&env) == nil {
						n.Output = env.OpOutput
						if len(env.ArtifactRefs) > 0 {
							refs := make([]recipeartifacts.Ref, 0, len(env.ArtifactRefs))
							for _, artifactRef := range env.ArtifactRefs {
								refs = append(refs, artifactRef)
							}
							n.ArtifactRefs = refs
						}
					}
				case coretasks.OutputKindContextPatch:
					var patch coretasks.ContextPatch
					if outEnv.DecodePayload(&patch) == nil {
						n.Kind = model.JobRunStoryNodeKindContextPatch
						n.Title = "context patch"
						n.StepID = ""
						n.StepType = ""
						n.Output = patch
					}
				default:
					n.Output = map[string]any{"kind": string(outEnv.Kind)}
				}
			} else {
				var anyRaw any
				if json.Unmarshal(raw, &anyRaw) == nil {
					n.Output = anyRaw
				}
			}
		}

		arts, aerr := td.GetArtifacts()
		if aerr == nil && len(arts) > 0 {
			keys := make([]jobdb.ArtifactKey, 0, len(arts))
			for _, art := range arts {
				if art == nil {
					continue
				}
				if k, kerr := art.ArtifactKey(); kerr == nil {
					keys = append(keys, k)
					continue
				}
				ord := int64(0)
				if n.TaskOrdinal != nil {
					ord = *n.TaskOrdinal
				}
				keys = append(keys, jobdb.ArtifactKey{
					JobId:       jobID,
					TaskOrdinal: ord,
					Name:        art.Name(),
					SizeBytes:   art.Size(),
				})
			}
			n.ArtifactKeys = keys
		}
	}

	_ = taskType
}

func cloneNode(src *model.JobRunStoryNode) *model.JobRunStoryNode {
	if src == nil {
		return nil
	}
	dst := *src
	dst.StartedAt = cloneTimePtr(src.StartedAt)
	dst.FinishedAt = cloneTimePtr(src.FinishedAt)
	dst.Path = append([]string{}, src.Path...)
	dst.InlineStack = cloneInlineBoundaryStack(src.InlineStack)
	dst.PriorAttempts = cloneNodes(src.PriorAttempts)
	dst.RenderedVars = cloneMap(src.RenderedVars)
	dst.ArtifactKeys = append([]jobdb.ArtifactKey{}, src.ArtifactKeys...)
	dst.ArtifactRefs = append([]recipeartifacts.Ref{}, src.ArtifactRefs...)
	dst.TaskOrdinal = cloneInt64Ptr(src.TaskOrdinal)
	dst.RestartFromOrdinal = cloneInt64Ptr(src.RestartFromOrdinal)
	dst.Children = cloneNodes(src.Children)
	dst.Invocation = cloneInvocation(src.Invocation)
	dst.Error = cloneError(src.Error)
	dst.IsInitial = cloneBoolPtr(src.IsInitial)
	dst.Evaluations = cloneEvaluations(src.Evaluations)
	dst.Decision = cloneDecision(src.Decision)
	dst.PastAttempts = cloneNodes(src.PastAttempts)
	return &dst
}

func cloneNodes(src []*model.JobRunStoryNode) []*model.JobRunStoryNode {
	if src == nil {
		return nil
	}
	dst := make([]*model.JobRunStoryNode, 0, len(src))
	for _, n := range src {
		dst = append(dst, cloneNode(n))
	}
	return dst
}

func cloneInvocation(src map[string]interface{}) map[string]interface{} {
	return cloneMap(src)
}

func cloneMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = cloneAny(v)
	}
	return dst
}

func cloneAny(src interface{}) interface{} {
	switch typed := src.(type) {
	case map[string]interface{}:
		return cloneMap(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return typed
	}
}

func cloneError(src *model.JobRunStoryError) *model.JobRunStoryError {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func cloneEvaluations(src []model.JobRunStoryTransitionEval) []model.JobRunStoryTransitionEval {
	if src == nil {
		return nil
	}
	dst := make([]model.JobRunStoryTransitionEval, 0, len(src))
	for _, ev := range src {
		out := ev
		if ev.Reason != nil {
			reason := *ev.Reason
			out.Reason = &reason
		}
		dst = append(dst, out)
	}
	return dst
}

func cloneDecision(src *model.JobRunStoryTransitionDecision) *model.JobRunStoryTransitionDecision {
	if src == nil {
		return nil
	}
	dst := *src
	if src.ToStateID != nil {
		to := *src.ToStateID
		dst.ToStateID = &to
	}
	dst.Payload = cloneMap(src.Payload)
	return &dst
}

func cloneTimePtr(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	t := *src
	return &t
}

func cloneInt64Ptr(src *int64) *int64 {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

func cloneBoolPtr(src *bool) *bool {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
