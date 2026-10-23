package live

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// scopeReplay adapts JobDB's versioned scope identities to chapter ordinals.
// It observes interpreter calls; it never infers boundaries from display paths,
// task ordinals, or timestamps. Identity encoding follows execution protocol v1/2.
type scopeReplay struct {
	mu      sync.Mutex
	attempt int
	root    string
	entries map[string]jobdb.ExecutionHistoryEntry
	observe func(jobdb.ExecutionHistoryEntry, *template.ResolutionContext) error
}

func newScopeReplay(history []jobdb.ExecutionHistoryEntry, observe func(jobdb.ExecutionHistoryEntry, *template.ResolutionContext) error) *scopeReplay {
	r := &scopeReplay{entries: map[string]jobdb.ExecutionHistoryEntry{}, observe: observe}
	for _, entry := range history {
		if entry.Record.Kind != "scope" {
			continue
		}
		if r.root == "" {
			r.root = entry.Record.ID
		}
		if restartableScope(entry) {
			r.entries[entry.Record.ID] = entry
		}
	}
	return r
}

func restartableScope(entry jobdb.ExecutionHistoryEntry) bool {
	var policy struct {
		Restart bool `json:"restart"`
	}
	return entry.Record.Kind == "scope" && json.Unmarshal(entry.Record.Descriptor, &policy) == nil && policy.Restart
}

type scopeReplayKey struct{}
type scopeReplayContext struct {
	jobworkflow.ExecutionContext
	id     string
	mu     sync.Mutex
	counts map[string]int
}

func wrapScopeContext(exec jobworkflow.ExecutionContext, id string) *scopeReplayContext {
	return &scopeReplayContext{ExecutionContext: exec, id: id, counts: map[string]int{}}
}
func (c *scopeReplayContext) Context() context.Context {
	return context.WithValue(c.ExecutionContext.Context(), scopeReplayKey{}, c)
}
func (c *scopeReplayContext) nextScopeID(site string, advance bool) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.counts[site] + 1
	if advance {
		c.counts[site] = n
	}
	encoded, _ := json.Marshal(site)
	return fmt.Sprintf("%s/scope:%s#%d", c.id, encoded, n)
}
func (c *scopeReplayContext) DoScope(site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	id := c.nextScopeID(site, true)
	var mu sync.Mutex
	attempt := 0
	opts.Catch = append([]jobworkflow.CatchRule(nil), opts.Catch...)
	for i := range opts.Catch {
		rule := opts.Catch[i]
		opts.Catch[i].Handle = func(exec jobworkflow.ExecutionContext, err error) (jobworkflow.CatchResult, error) {
			mu.Lock()
			n := attempt
			mu.Unlock()
			return rule.Handle(wrapScopeContext(exec, fmt.Sprintf("%s/attempt:%d/catch:%s", id, n, rule.ID)), err)
		}
	}
	return c.ExecutionContext.DoScope(site, opts, func(exec jobworkflow.ExecutionContext) (jobdb.JobData, error) {
		mu.Lock()
		attempt++
		n := attempt
		mu.Unlock()
		return body(wrapScopeContext(exec, fmt.Sprintf("%s/attempt:%d", id, n)))
	})
}

func (r *scopeReplay) beforeScope(ctx context.Context, site string, state *template.ResolutionContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, ok := ctx.Value(scopeReplayKey{}).(*scopeReplayContext)
	if !ok {
		return fmt.Errorf("restart scope instrumentation unavailable")
	}
	entry, ok := r.entries[c.nextScopeID(site, false)]
	if !ok {
		return nil
	} // An unrecorded live edge has no restart target.
	return r.observe(entry, state)
}

func (r *scopeReplay) worker(worker jobworkflow.ExecutionWorker) jobworkflow.ExecutionWorker {
	execute := worker.Execute
	worker.Execute = func(exec jobworkflow.ExecutionContext, data jobdb.JobData) (jobdb.JobData, error) {
		r.mu.Lock()
		attempt := r.attempt
		r.mu.Unlock()
		return execute(wrapScopeContext(exec, fmt.Sprintf("%s/attempt:%d", r.root, attempt)), data)
	}
	return worker
}

type scopeReplayObserver struct {
	jobworkflow.ReplayObserver
	scopes *scopeReplay
}

func (o scopeReplayObserver) OnJobStart(event jobworkflow.JobStartEvent) {
	o.scopes.mu.Lock()
	o.scopes.attempt = event.AttemptNumber
	o.scopes.mu.Unlock()
	o.ReplayObserver.OnJobStart(event)
}

func (r *Recorder) recordRestartScope(entry jobdb.ExecutionHistoryEntry, _ *template.ResolutionContext) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tree := r.attemptTrees[r.currentJobAttempt]; tree != nil {
		if node := tree.current(); node != nil && node.RestartChapterOrdinal == nil {
			ordinal := entry.Ordinal
			node.RestartChapterOrdinal = &ordinal
			node.RestartScopeID = entry.Record.ID
			node.RestartRelationship = "exact"
		}
	}
	return nil
}

// Tasks restart through their enclosing recipe scope. Source-loading nodes have
// no such boundary. A recipe root uses its outer executable body's boundary.
func associateRestartScopes(node *model.JobRunStoryNode, parent *model.JobRunStoryNode) {
	if node == nil {
		return
	}
	if node.Kind == model.JobRunStoryNodeKindRecipe && node.RestartChapterOrdinal == nil {
		for _, child := range node.Children {
			if child.RestartChapterOrdinal != nil {
				node.RestartChapterOrdinal = child.RestartChapterOrdinal
				node.RestartScopeID = child.RestartScopeID
				node.RestartRelationship = "body"
				break
			}
		}
	}
	if node.RestartChapterOrdinal == nil && parent != nil && node.Kind != model.JobRunStoryNodeKindRecipeSourceResolution {
		node.RestartChapterOrdinal = parent.RestartChapterOrdinal
		node.RestartScopeID = parent.RestartScopeID
		node.RestartRelationship = "containing"
	}
	if node.RestartChapterOrdinal == nil {
		node.RestartUnavailableReason = "No recorded restartable recipe scope"
	}
	for _, child := range node.Children {
		associateRestartScopes(child, node)
	}
	for _, prior := range node.PriorAttempts {
		associateRestartScopes(prior, node)
	}
	for _, prior := range node.PastAttempts {
		associateRestartScopes(prior, nil)
	}
}

func labelRestartScopes(story *JobRunStory) {
	scopes := map[string]*model.RestartScope{}
	for i := range story.RestartScopes {
		scopes[story.RestartScopes[i].ScopeID] = &story.RestartScopes[i]
	}
	var visit func(*model.JobRunStoryNode)
	visit = func(node *model.JobRunStoryNode) {
		if node == nil {
			return
		}
		if node.RestartChapterOrdinal != nil && node.RestartRelationship == "exact" {
			if scope := scopes[node.RestartScopeID]; scope != nil {
				scope.Title = node.Title
				scope.Path = append([]string(nil), node.Path...)
			}
		}
		for _, children := range [][]*model.JobRunStoryNode{node.Children, node.PastAttempts, node.PriorAttempts} {
			for _, child := range children {
				visit(child)
			}
		}
	}
	visit(story.Root)
}
