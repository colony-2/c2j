package template

import (
	"encoding/json"
	"maps"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/toolenv"
)

// CloneAttempt isolates the mutable resolver graph while retaining immutable services.
func (rc *ResolutionContext) CloneAttempt() (*ResolutionContext, error) {
	nodes := map[*ResolutionContext]*ResolutionContext{}
	commits := map[*contextual.GitCommitContext]*contextual.GitCommitContext{}
	counters := maps.Clone(rc.tracker.counters)
	workspaceCounters := maps.Clone(rc.workspaceCounters)
	var clone func(*ResolutionContext) (*ResolutionContext, error)
	clone = func(src *ResolutionContext) (*ResolutionContext, error) {
		if src == nil {
			return nil, nil
		}
		if v := nodes[src]; v != nil {
			return v, nil
		}
		dst := *src
		nodes[src] = &dst
		raw, err := json.Marshal(src.TemplateData)
		if err != nil {
			return nil, err
		}
		dst.TemplateData = templateData{}
		if err = json.Unmarshal(raw, &dst.TemplateData); err != nil {
			return nil, err
		}
		dst.Parent, err = clone(src.Parent)
		if err != nil {
			return nil, err
		}
		dst.workspaceCaller, err = clone(src.workspaceCaller)
		if err != nil {
			return nil, err
		}
		if src.commitContext != nil {
			c := commits[src.commitContext]
			if c == nil {
				value := *src.commitContext
				c = &value
				commits[src.commitContext] = c
			}
			dst.commitContext = c
		}
		if src.workspaceBase != nil {
			v := *src.workspaceBase
			dst.workspaceBase = &v
		}
		dst.workspaceCounters = workspaceCounters
		dst.tracker = &invocationTracker{segments: append([]string(nil), src.tracker.segments...), counters: counters}
		dst.ToolScopes = append([]toolenv.Scope(nil), src.ToolScopes...)
		dst.artifactCache = maps.Clone(src.artifactCache)
		dst.lastArtifacts = append(dst.lastArtifacts[:0:0], src.lastArtifacts...)
		dst.lastArtifactRefs = append(dst.lastArtifactRefs[:0:0], src.lastArtifactRefs...)
		dst.lastExecution = cloneAnyMap(src.lastExecution)
		dst.lastJobs.JobIDs = append(src.lastJobs.JobIDs[:0:0], src.lastJobs.JobIDs...)
		dst.lastJobs.GoalIDs = append(src.lastJobs.GoalIDs[:0:0], src.lastJobs.GoalIDs...)
		dst.lastJobs.Items = append(src.lastJobs.Items[:0:0], src.lastJobs.Items...)
		dst.Options.ResolvedSelectors = maps.Clone(src.Options.ResolvedSelectors)
		dst.Options.ResolvedGitRefs = maps.Clone(src.Options.ResolvedGitRefs)
		// These views intentionally share their containing scope's maps.
		if dst.Parent != nil {
			switch src.ScopeType {
			case ScopeOp:
				dst.TemplateData.Sequence = dst.Parent.TemplateData.Sequence
				dst.TemplateData.States = dst.Parent.TemplateData.States
				dst.TemplateData.ContainerInputs = dst.Parent.TemplateData.ContainerInputs
			case ScopeState:
				dst.TemplateData.States = dst.Parent.TemplateData.States
				dst.TemplateData.ContainerInputs = dst.Parent.TemplateData.ContainerInputs
			}
		}
		if _, err = dst.initializeCEL(); err != nil {
			return nil, err
		}
		return &dst, nil
	}
	return clone(rc)
}

// AdoptAttempt publishes a finished attempt on the orchestration goroutine.
// Copy shared maps in place so enclosing resolver views remain connected.
func (rc *ResolutionContext) AdoptAttempt(src *ResolutionContext) {
	if rc == nil || src == nil {
		return
	}
	rc.Parent.AdoptAttempt(src.Parent)
	copyMap := func(dst, source map[string]StepOutput) map[string]StepOutput {
		if dst == nil {
			dst = map[string]StepOutput{}
		}
		clear(dst)
		maps.Copy(dst, source)
		return dst
	}
	seq := copyMap(rc.TemplateData.Sequence, src.TemplateData.Sequence)
	states := copyMap(rc.TemplateData.States, src.TemplateData.States)
	rc.TemplateData = src.TemplateData
	rc.TemplateData.Sequence = seq
	rc.TemplateData.States = states
	if rc.commitContext != nil && src.commitContext != nil {
		*rc.commitContext = *src.commitContext
	}
	clear(rc.tracker.counters)
	maps.Copy(rc.tracker.counters, src.tracker.counters)
	clear(rc.workspaceCounters)
	maps.Copy(rc.workspaceCounters, src.workspaceCounters)
	rc.lastExecution = src.lastExecution
	rc.lastArtifacts = src.lastArtifacts
	rc.lastArtifactRefs = src.lastArtifactRefs
	rc.lastJobs = src.lastJobs
	rc.artifactCache = src.artifactCache
}
