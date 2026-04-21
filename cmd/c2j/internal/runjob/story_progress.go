package runjob

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	storylive "github.com/colony-2/c2j/pkg/story/live"
)

type storyProgressRenderer interface {
	SetMode(mode string)
	Render(story *storylive.JobRunStory)
	Flush()
}

func newStoryProgressRenderer(out io.Writer, mode string, interactive bool) storyProgressRenderer {
	if interactive {
		return newTreeStoryProgressRenderer(out, mode)
	}
	return newLineStoryProgressRenderer(out, mode)
}

type lineStoryProgressRenderer struct {
	out   io.Writer
	mu    sync.Mutex
	mode  string
	nodes map[string]renderedNodeState
}

type renderedNodeState struct {
	status storylive.JobRunStoryNodeStatus
}

func newLineStoryProgressRenderer(out io.Writer, mode string) *lineStoryProgressRenderer {
	return &lineStoryProgressRenderer{
		out:   out,
		mode:  mode,
		nodes: make(map[string]renderedNodeState),
	}
}

func (r *lineStoryProgressRenderer) SetMode(mode string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = mode
}

func (r *lineStoryProgressRenderer) Render(story *storylive.JobRunStory) {
	if r == nil || story == nil || story.Root == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[string]struct{})
	r.walk(story.Root, 0, seen)
	for id := range r.nodes {
		if _, ok := seen[id]; ok {
			continue
		}
		delete(r.nodes, id)
	}
}

func (r *lineStoryProgressRenderer) Flush() {}

func (r *lineStoryProgressRenderer) walk(node *storylive.JobRunStoryNode, depth int, seen map[string]struct{}) {
	if node == nil {
		return
	}

	seen[node.ID] = struct{}{}
	prev, hadPrev := r.nodes[node.ID]
	kind, label := renderNodeKindAndLabel(node)

	if !hadPrev {
		r.printLine(depth, kind, label)
	}

	for _, child := range node.Children {
		r.walk(child, depth+1, seen)
	}

	if isTerminalNodeStatus(node.Status) && (!hadPrev || prev.status != node.Status) {
		r.printLine(depth, renderStatusLabel(node), renderStatusValue(node, label))
	}

	r.nodes[node.ID] = renderedNodeState{status: node.Status}
}

func (r *lineStoryProgressRenderer) printLine(depth int, kind string, label string) {
	if strings.TrimSpace(kind) == "" {
		kind = "node"
	}
	if strings.TrimSpace(label) == "" {
		label = kind
	}
	fmt.Fprintf(r.out, "%s%s%s %s\n", modePrefix(r.mode), strings.Repeat("  ", depth), kind, label)
}

type treeStoryProgressRenderer struct {
	out           io.Writer
	mu            sync.Mutex
	mode          string
	debounce      time.Duration
	renderedLines int
	lastLines     []string
	lastFlushAt   time.Time
	pendingLines  []string
	timer         *time.Timer
}

func newTreeStoryProgressRenderer(out io.Writer, mode string) *treeStoryProgressRenderer {
	return &treeStoryProgressRenderer{
		out:       out,
		mode:      mode,
		debounce:  75 * time.Millisecond,
		lastLines: make([]string, 0, 32),
	}
}

func (r *treeStoryProgressRenderer) SetMode(mode string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.flushPendingLocked()
	defer r.mu.Unlock()
	r.mode = mode
}

func (r *treeStoryProgressRenderer) Render(story *storylive.JobRunStory) {
	if r == nil || story == nil || story.Root == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	lines := buildTreeRenderLines(r.mode, story)
	if stringSlicesEqual(r.lastLines, lines) && len(r.pendingLines) == 0 {
		return
	}

	immediate := r.lastFlushAt.IsZero() || isTerminalWorkflowStatus(story.Status)
	if !immediate && r.debounce > 0 {
		elapsed := time.Since(r.lastFlushAt)
		if elapsed < r.debounce {
			r.pendingLines = append(r.pendingLines[:0], lines...)
			r.scheduleFlushLocked(r.debounce - elapsed)
			return
		}
	}

	r.drawLocked(lines)
}

func (r *treeStoryProgressRenderer) Flush() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushPendingLocked()
}

func (r *treeStoryProgressRenderer) scheduleFlushLocked(delay time.Duration) {
	if delay <= 0 {
		r.flushPendingLocked()
		return
	}
	if r.timer != nil {
		return
	}
	r.timer = time.AfterFunc(delay, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.flushPendingLocked()
	})
}

func (r *treeStoryProgressRenderer) flushPendingLocked() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if len(r.pendingLines) == 0 {
		return
	}
	lines := append([]string{}, r.pendingLines...)
	r.pendingLines = nil
	r.drawLocked(lines)
}

func (r *treeStoryProgressRenderer) drawLocked(lines []string) {
	if stringSlicesEqual(r.lastLines, lines) {
		return
	}
	if r.renderedLines > 0 {
		fmt.Fprintf(r.out, "\x1b[%dA\x1b[J", r.renderedLines)
	}
	if len(lines) > 0 {
		fmt.Fprint(r.out, strings.Join(lines, "\n"))
		fmt.Fprint(r.out, "\n")
	}
	r.renderedLines = len(lines)
	r.lastLines = append(r.lastLines[:0], lines...)
	r.lastFlushAt = time.Now()
}

func buildTreeRenderLines(mode string, story *storylive.JobRunStory) []string {
	if story == nil || story.Root == nil {
		return nil
	}

	lines := make([]string, 0, 32)
	jobID := strings.TrimSpace(story.JobID)
	if jobID == "" {
		jobID = "job"
	}
	lines = append(lines, fmt.Sprintf("%sjob %s [%s]", treeModePrefix(mode), jobID, strings.TrimSpace(string(story.Status))))
	appendTreeNodeLines(&lines, story.Root, 0)
	return lines
}

func appendTreeNodeLines(lines *[]string, node *storylive.JobRunStoryNode, depth int) {
	if node == nil {
		return
	}

	title := renderTreeNodeTitle(node)
	suffix := renderTreeNodeSuffix(node)
	if suffix != "" {
		title += " " + suffix
	}
	if shouldCollapseTreeNode(node, depth) {
		title += " " + collapseSummary(node)
		*lines = append(*lines, fmt.Sprintf("%s%s [%s]", strings.Repeat("  ", depth), title, strings.TrimSpace(string(node.Status))))
		return
	}

	*lines = append(*lines, fmt.Sprintf("%s%s [%s]", strings.Repeat("  ", depth), title, strings.TrimSpace(string(node.Status))))
	for _, child := range node.Children {
		appendTreeNodeLines(lines, child, depth+1)
	}
}

func renderTreeNodeTitle(node *storylive.JobRunStoryNode) string {
	if node == nil {
		return "node"
	}
	kind, label := renderNodeKindAndLabel(node)
	title := strings.TrimSpace(strings.TrimSpace(kind + " " + label))
	if title == "" {
		return "node"
	}
	return title
}

func renderTreeNodeSuffix(node *storylive.JobRunStoryNode) string {
	if node == nil {
		return ""
	}

	parts := make([]string, 0, 3)
	if node.JobAttempt > 1 || len(node.PastAttempts) > 0 {
		if len(node.PastAttempts) > 0 {
			parts = append(parts, fmt.Sprintf("job attempt %d, %d prior", maxInt(node.JobAttempt, 1), len(node.PastAttempts)))
		} else {
			parts = append(parts, fmt.Sprintf("job attempt %d", maxInt(node.JobAttempt, 1)))
		}
	}
	if node.Attempt > 1 || len(node.PriorAttempts) > 0 {
		parts = append(parts, fmt.Sprintf("attempt %d", maxInt(node.Attempt, len(node.PriorAttempts)+1)))
	}
	if node.Kind == storylive.JobRunStoryNodeKindState && node.IsInitial != nil && *node.IsInitial {
		parts = append(parts, "initial")
	}
	if node.Kind == storylive.JobRunStoryNodeKindTransitionEval && node.Decision != nil {
		switch node.Decision.Kind {
		case "state":
			if node.Decision.ToStateID != nil && strings.TrimSpace(*node.Decision.ToStateID) != "" {
				parts = append(parts, "to "+strings.TrimSpace(*node.Decision.ToStateID))
			}
		case "fallthrough":
			parts = append(parts, "fallthrough")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func shouldCollapseTreeNode(node *storylive.JobRunStoryNode, depth int) bool {
	if node == nil || depth == 0 || len(node.Children) == 0 {
		return false
	}
	if node.Status != storylive.JobRunStoryNodeStatusSucceeded && node.Status != storylive.JobRunStoryNodeStatusSkipped {
		return false
	}
	return !subtreeHasNonTerminalOrError(node)
}

func subtreeHasNonTerminalOrError(node *storylive.JobRunStoryNode) bool {
	if node == nil {
		return false
	}
	switch node.Status {
	case storylive.JobRunStoryNodeStatusRunning, storylive.JobRunStoryNodeStatusPending, storylive.JobRunStoryNodeStatusFailed, storylive.JobRunStoryNodeStatusCanceled, storylive.JobRunStoryNodeStatusUnknown:
		return true
	}
	for _, child := range node.Children {
		if subtreeHasNonTerminalOrError(child) {
			return true
		}
	}
	return false
}

func collapseSummary(node *storylive.JobRunStoryNode) string {
	count := countDescendants(node)
	switch count {
	case 0:
		return ""
	case 1:
		return "(1 child collapsed)"
	default:
		return fmt.Sprintf("(%d children collapsed)", count)
	}
}

func countDescendants(node *storylive.JobRunStoryNode) int {
	if node == nil {
		return 0
	}
	total := 0
	for _, child := range node.Children {
		if child == nil {
			continue
		}
		total++
		total += countDescendants(child)
	}
	return total
}

func modePrefix(mode string) string {
	switch mode {
	case "cached":
		return "[cached] "
	case "live":
		return "[live] "
	default:
		return ""
	}
}

func treeModePrefix(mode string) string {
	switch mode {
	case "cached":
		return "cached "
	case "live":
		return "live "
	default:
		return ""
	}
}

func renderNodeKindAndLabel(node *storylive.JobRunStoryNode) (string, string) {
	switch node.Kind {
	case storylive.JobRunStoryNodeKindRecipe:
		return "recipe", firstNonEmpty(node.RecipeID, trimNodeTitle(node.Title, "recipe"))
	case storylive.JobRunStoryNodeKindRecipeSourceResolution:
		return "source", firstNonEmpty(trimNodeTitle(node.Title, "recipe source resolution"), "recipe source resolution")
	case storylive.JobRunStoryNodeKindSequence:
		return "sequence", firstNonEmpty(node.SequenceID, trimNodeTitle(node.Title, "sequence"))
	case storylive.JobRunStoryNodeKindOp:
		return "op", firstNonEmpty(node.OpID, trimNodeTitle(node.Title, "op"))
	case storylive.JobRunStoryNodeKindOpStep:
		return "step", firstNonEmpty(node.StepID, trimNodeTitle(node.Title, "step"))
	case storylive.JobRunStoryNodeKindContextPatch:
		return "context-patch", firstNonEmpty(trimNodeTitle(node.Title, "context patch"), "context patch")
	case storylive.JobRunStoryNodeKindStateMachine:
		return "state-machine", firstNonEmpty(node.StateMachineID, trimNodeTitle(node.Title, "stateMachine"))
	case storylive.JobRunStoryNodeKindState:
		return "state", firstNonEmpty(node.StateID, trimNodeTitle(node.Title, "state"))
	case storylive.JobRunStoryNodeKindTransitionEval:
		if node.Decision != nil && node.Decision.Kind == "state" && node.Decision.ToStateID != nil && strings.TrimSpace(*node.Decision.ToStateID) != "" {
			return "transition", "to " + strings.TrimSpace(*node.Decision.ToStateID)
		}
		if node.Decision != nil && node.Decision.Kind == "fallthrough" {
			return "transition", "fallthrough"
		}
		return "transition", firstNonEmpty(trimNodeTitle(node.Title, "evaluate transitions"), "evaluate transitions")
	default:
		return "node", strings.TrimSpace(node.Title)
	}
}

func renderStatusLabel(node *storylive.JobRunStoryNode) string {
	switch node.Status {
	case storylive.JobRunStoryNodeStatusSucceeded:
		return "done"
	case storylive.JobRunStoryNodeStatusFailed:
		return "failed"
	case storylive.JobRunStoryNodeStatusCanceled:
		return "canceled"
	case storylive.JobRunStoryNodeStatusSkipped:
		return "skipped"
	default:
		return "done"
	}
}

func renderStatusValue(node *storylive.JobRunStoryNode, label string) string {
	if node != nil && node.Status == storylive.JobRunStoryNodeStatusFailed && node.Error != nil && strings.TrimSpace(node.Error.Message) != "" {
		return label + ": " + strings.TrimSpace(node.Error.Message)
	}
	return label
}

func isTerminalNodeStatus(status storylive.JobRunStoryNodeStatus) bool {
	switch status {
	case storylive.JobRunStoryNodeStatusSucceeded, storylive.JobRunStoryNodeStatusFailed, storylive.JobRunStoryNodeStatusCanceled, storylive.JobRunStoryNodeStatusSkipped:
		return true
	default:
		return false
	}
}

func trimNodeTitle(title string, prefix string) string {
	title = strings.TrimSpace(title)
	prefix = strings.TrimSpace(prefix)
	if title == "" {
		return ""
	}
	if prefix == "" {
		return title
	}
	return strings.TrimSpace(strings.TrimPrefix(title, prefix))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func isTerminalWorkflowStatus(status storylive.WorkflowStatus) bool {
	switch status {
	case storylive.WorkflowStatusCompleted, storylive.WorkflowStatusFailed, storylive.WorkflowStatusCanceled, storylive.WorkflowStatusTerminated, storylive.WorkflowStatusTimedOut:
		return true
	default:
		return false
	}
}
