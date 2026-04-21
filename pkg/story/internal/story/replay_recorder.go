package story

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	coretasks "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/swf-go/pkg/swf"
)

type replayStoryRecorder struct {
	mu sync.Mutex

	jobKey swf.JobKey
	logger *slog.Logger

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

func newReplayStoryRecorder(jobKey swf.JobKey, logger *slog.Logger) *replayStoryRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &replayStoryRecorder{
		jobKey:            jobKey,
		logger:            logger,
		currentJobAttempt: 0,
		attemptTrees:      make(map[int]*treeBuilder),
		rootsByJobAttempt: make(map[int]*model.JobRunStoryNode),
		attemptTiming:     make(map[int]*jobAttemptTiming),
		resolvedSources:   make(map[int]*compiler.ResolvedRecipeSource),
		sourceNodes:       make(map[int]*rootSourceResolutionTracker),
	}
}

func (r *replayStoryRecorder) EnsureAttemptTree() *treeBuilder {
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

func (r *replayStoryRecorder) OnJobStart(event swf.JobStartEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
}

func (r *replayStoryRecorder) OnJobEnd(event swf.JobEndEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
}

func (r *replayStoryRecorder) ensureAttemptTimingLocked(attemptNumber int) *jobAttemptTiming {
	if attemptNumber <= 0 {
		attemptNumber = 1
	}
	if r.attemptTiming == nil {
		r.attemptTiming = make(map[int]*jobAttemptTiming)
	}
	if tm, ok := r.attemptTiming[attemptNumber]; ok && tm != nil {
		return tm
	}
	tm := &jobAttemptTiming{}
	r.attemptTiming[attemptNumber] = tm
	return tm
}

func (r *replayStoryRecorder) OnRecipeLoaded(recipeName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recipeNameFromStart = strings.TrimSpace(recipeName)
}

func (r *replayStoryRecorder) OnRecipeSourceResolved(resolution compiler.RecipeSourceResolution) {
	r.mu.Lock()
	defer r.mu.Unlock()
	att := r.currentJobAttempt
	if att <= 0 {
		att = 1
		r.currentJobAttempt = att
	}
	if existing := r.resolvedSources[att]; existing != nil {
		existing.RecipeSourceResolution = resolution
		return
	}
	resolved := compiler.ResolvedRecipeSource{RecipeSourceResolution: resolution}
	r.resolvedSources[att] = &resolved
}

func (r *replayStoryRecorder) SetRecipeMeta(id, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.TrimSpace(id) != "" {
		r.recipeID = strings.TrimSpace(id)
	}
	if strings.TrimSpace(version) != "" {
		r.recipeVersion = strings.TrimSpace(version)
	}
}

func (r *replayStoryRecorder) SetRoot(root *model.JobRunStoryNode) {
	if root == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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
}

func (r *replayStoryRecorder) SetCurrentOpNode(op *model.JobRunStoryNode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentOpNode = op
	if op == nil {
		r.currentOpSteps = nil
		return
	}
	r.currentOpSteps = make(map[int64]*ordinalStepTracker)
}

func (r *replayStoryRecorder) ensureRootSourceResolutionTrackerLocked(attempt int) *rootSourceResolutionTracker {
	if attempt <= 0 {
		attempt = 1
	}
	if r.sourceNodes == nil {
		r.sourceNodes = make(map[int]*rootSourceResolutionTracker)
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

func (r *replayStoryRecorder) handleRootSourceResolutionTaskStartLocked(event swf.TaskStartEvent) {
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

func (r *replayStoryRecorder) handleRootSourceResolutionTaskEndLocked(event swf.TaskEndEvent) {
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

func (r *replayStoryRecorder) OnTaskStart(event swf.TaskStartEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if event.TaskType == compiler.RootSourceResolutionTaskType {
		r.handleRootSourceResolutionTaskStartLocked(event)
		return
	}

	op := r.currentOpNode
	tree := r.attemptTrees[r.currentJobAttempt]
	if op == nil || tree == nil {
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

	// Create an attempt node; we fold the latest attempt into the step node.
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
}

func (r *replayStoryRecorder) OnTaskEnd(event swf.TaskEndEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if event.TaskType == compiler.RootSourceResolutionTaskType {
		r.handleRootSourceResolutionTaskEndLocked(event)
		return
	}

	if r.currentOpSteps == nil {
		return
	}

	tr := r.currentOpSteps[event.Ordinal]
	if tr == nil || tr.stepNode == nil || tr.byAttempt == nil {
		return
	}

	attNode := tr.byAttempt[event.AttemptNumber]
	if attNode == nil {
		return
	}
	applyTaskOutputToNode(attNode, r.jobKey.JobId, event.TaskType, event.Output, event.Err)
	if !event.At.IsZero() && attNode.FinishedAt == nil && isTerminal(attNode.Status) {
		t := event.At
		attNode.FinishedAt = &t
	}

	// Fold the latest attempt into the step node and populate prior attempts.
	copyAttemptIntoStep(tr.stepNode, attNode)
	prior := make([]*model.JobRunStoryNode, 0, len(tr.attemptNodes))
	for _, n := range tr.attemptNodes {
		if n == nil || n == attNode {
			continue
		}
		prior = append(prior, n)
	}
	tr.stepNode.PriorAttempts = prior
}

func (r *replayStoryRecorder) BuildStory(replayErr error) *model.JobRunStory {
	r.mu.Lock()
	defer r.mu.Unlock()

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
		// Attach past attempts in order.
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

func (r *replayStoryRecorder) buildRootsWithSourceResolutionLocked() map[int]*model.JobRunStoryNode {
	roots := make(map[int]*model.JobRunStoryNode, len(r.rootsByJobAttempt))
	for att, root := range r.rootsByJobAttempt {
		roots[att] = root
	}

	for att := range r.sourceNodes {
		if _, ok := roots[att]; !ok {
			roots[att] = nil
		}
	}

	for att, root := range roots {
		sourceNode := r.resolutionNodeForAttemptLocked(att)
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

func (r *replayStoryRecorder) resolutionNodeForAttemptLocked(attempt int) *model.JobRunStoryNode {
	tracker := r.sourceNodes[attempt]
	if tracker == nil || tracker.node == nil {
		return nil
	}
	return tracker.node
}

func (r *replayStoryRecorder) syntheticRootForAttemptLocked(attempt int, sourceNode *model.JobRunStoryNode) *model.JobRunStoryNode {
	tree := r.attemptTrees[attempt]
	if tree == nil {
		tree = newTreeBuilder()
		r.attemptTrees[attempt] = tree
	}

	recipeID := strings.TrimSpace(r.recipeID)
	recipeName := toRecipeNameFromIDFallback(recipeID, r.recipeNameFromStart)
	if recipeName == "" {
		recipeName = "recipe"
	}

	root := tree.newNode(model.JobRunStoryNodeKindRecipe, "recipe "+recipeName)
	root.RecipeID = recipeID
	root.Status = sourceNode.Status
	root.JobAttempt = attempt
	root.Children = append(root.Children, sourceNode)
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

func parseResolvedRecipeSource(td swf.TaskData) *compiler.ResolvedRecipeSource {
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
}

func applyTaskInputToNode(n *model.JobRunStoryNode, td swf.TaskData) {
	if n == nil || td == nil {
		return
	}
	raw, err := td.GetData()
	if err != nil || len(raw) == 0 {
		return
	}

	// Try to decode the activity invocation request so input and invoke_seq are recipe-centric.
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
		return
	}

	// Some chapters store an OutputEnvelope in the "input" field. For API consumers,
	// unwrap the envelope and return only the payload.
	if payload, ok := unwrapTaskEnvelopePayload(raw); ok {
		n.Input = payload
		return
	}

	var anyRaw any
	if json.Unmarshal(raw, &anyRaw) == nil {
		n.Input = anyRaw
	}
}

func applyTaskOutputToNode(n *model.JobRunStoryNode, jobID string, taskType string, td swf.TaskData, err error) {
	if n == nil {
		return
	}

	// For unexpected chapters (restart context patches), SWF reports a determinism mismatch but
	// still carries a cached output. Use that output as the story output and treat the node as
	// succeeded when the cached payload is a context patch.
	if mismatch, ok := swf.UnexpectedChapter(err); ok && mismatch.CachedTaskDataErr() == nil && mismatch.CachedTaskData() != nil {
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
			keys := make([]swf.ArtifactKey, 0, len(arts))
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
				keys = append(keys, swf.ArtifactKey{
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
