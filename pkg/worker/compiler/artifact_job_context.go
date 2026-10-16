package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type thinpackForwarder struct {
	jobworkflow.ExecutionContext
	lastThinpack   jobdb.Artifact
	workspaces     map[string]jobdb.Artifact
	scopedMode     bool
	rootScopeID    string
	rootArtifacts  []jobdb.Artifact
	initialRestore map[string]*jobdb.ArtifactKey
}

func newThinPackForwardingJobContext(inner jobworkflow.ExecutionContext) *thinpackForwarder {
	return &thinpackForwarder{ExecutionContext: inner, workspaces: map[string]jobdb.Artifact{}}
}

func (a *thinpackForwarder) fork(inner jobworkflow.ExecutionContext) *thinpackForwarder {
	copy := *a
	copy.ExecutionContext = inner
	copy.workspaces = maps.Clone(a.workspaces)
	copy.initialRestore = maps.Clone(a.initialRestore)
	copy.rootArtifacts = append([]jobdb.Artifact(nil), a.rootArtifacts...)
	return &copy
}
func (a *thinpackForwarder) adopt(src *thinpackForwarder) {
	a.lastThinpack = src.lastThinpack
	a.workspaces = src.workspaces
	a.initialRestore = src.initialRestore
	a.rootArtifacts = src.rootArtifacts
}
func (a *thinpackForwarder) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	out, err := a.ExecutionContext.DoTask(call, func(ctx context.Context) (jobdb.TaskData, error) {
		data, err := build(ctx)
		if err != nil {
			return nil, err
		}
		return a.prepareInput(call.TaskType, data)
	})
	if err != nil || out == nil {
		return out, err
	}
	return a.recordOutput(out)
}

func (a *thinpackForwarder) DoValidationTask(policy jobdb.RunPolicy, taskType string, data jobdb.TaskData) (jobdb.TaskData, bool, error) {
	override, ok := a.ExecutionContext.(validationTaskOverride)
	if !ok {
		return nil, false, nil
	}
	data, err := a.prepareInput(taskType, data)
	if err != nil {
		return nil, false, err
	}
	out, handled, err := override.DoValidationTask(policy, taskType, data)
	if err != nil || !handled || out == nil {
		return out, handled, err
	}
	out, err = a.recordOutput(out)
	return out, handled, err
}

func (a *thinpackForwarder) prepareInput(taskType string, data jobdb.TaskData) (jobdb.TaskData, error) {
	if !strings.Contains(taskType, ":") {
		return data, nil
	}
	scope := ""
	payload, err := data.GetData()
	if err != nil {
		return nil, err
	}
	var req workerops.ActivityInvocationRequest
	if err = json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	if req.GitTaskContext.Workspace != nil {
		scope = req.GitTaskContext.Workspace.ScopeID
	}
	last := a.lastThinpack
	if scope != "" {
		last = a.workspaces[scope]
	}
	if a.scopedMode || scope != "" {
		req.WorkspaceManaged = true
		req.RestoreArtifact = a.initialRestore[scope]
		if last != nil {
			key, e := last.ArtifactKey()
			if e != nil {
				return nil, fmt.Errorf("workspace restore artifact: %w", e)
			}
			req.RestoreArtifact = &key
		}
		payload, err = json.Marshal(req)
		if err != nil {
			return nil, err
		}
		artifacts, e := data.GetArtifacts()
		if e != nil {
			return nil, e
		}
		data = &jobdb.SimpleTaskData{Data: payload, Artifacts: artifacts}
	}

	if last != nil {
		payload, err := data.GetData()
		if err != nil {
			return nil, err
		}
		inputArtifacts, err := data.GetArtifacts()
		if err != nil {
			return nil, err
		}

		combined := make([]jobdb.Artifact, 0, len(inputArtifacts)+1)
		combined = append(combined, inputArtifacts...)
		combined = append(combined, last)
		data = &jobdb.SimpleTaskData{
			Data:      payload,
			Artifacts: combined,
		}
	}

	return data, nil
}

func (a *thinpackForwarder) recordOutput(out jobdb.TaskData) (jobdb.TaskData, error) {
	scope := ""
	raw, err := out.GetData()
	if err != nil {
		return nil, err
	}
	if decoded, e := decodeTaskOutput(raw); e == nil {
		scope = decoded.Activity.WorkspaceScopeID
	}
	outputArtifacts, err2 := out.GetArtifacts()
	if err2 != nil {
		return out, err2
	}
	if a.scopedMode || scope != "" {
		count := 0
		for _, art := range outputArtifacts {
			if art.Name() == gitstate.ThinPackArtifactName {
				count++
			}
		}
		if count > 1 {
			return nil, fmt.Errorf("ambiguous workspace snapshot: multiple thin packs in task result")
		}
	}
	if last := findThinPack(outputArtifacts); last != nil {
		if scope == a.rootScopeID {
			a.rootArtifacts = nil
			for _, art := range outputArtifacts {
				if workspaceInternalArtifact(art.Name()) {
					a.rootArtifacts = append(a.rootArtifacts, art)
				}
			}
		}
		if scope == "" {
			a.lastThinpack = last
		} else {
			a.workspaces[scope] = last
		}
	}
	// A successful refresh can replace a snapshot with a repository base.
	if decoded, e := decodeTaskOutput(raw); e == nil && findThinPack(outputArtifacts) == nil {
		git := decoded.Activity.GitResult
		if git.PersistHash != "" && git.PersistHash == git.ParentHash {
			if scope == "" {
				a.lastThinpack = nil
			} else {
				delete(a.workspaces, scope)
			}
			delete(a.initialRestore, scope)
			if scope == a.rootScopeID {
				a.rootArtifacts = nil
			}
		}
	}
	return out, nil
}

func findThinPack(artifacts []jobdb.Artifact) jobdb.Artifact {
	for _, ele := range artifacts {
		if ele.Name() == gitstate.ThinPackArtifactName {
			return ele
		}
	}
	return nil
}

// A foreign workspace's automatic snapshots must never become the root result.
func (a *thinpackForwarder) resultArtifacts(in []jobdb.Artifact) []jobdb.Artifact {
	if !a.scopedMode {
		return in
	}
	out := make([]jobdb.Artifact, 0, len(in)+1)
	for _, art := range in {
		if !workspaceInternalArtifact(art.Name()) {
			out = append(out, art)
		}
	}
	primary := a.lastThinpack
	if a.rootScopeID != "" {
		primary = a.workspaces[a.rootScopeID]
	}
	if len(a.rootArtifacts) > 0 {
		out = append(out, a.rootArtifacts...)
	} else if primary != nil {
		out = append(out, primary)
	}
	return out
}
func workspaceInternalArtifact(name string) bool {
	return name == gitstate.ThinPackArtifactName || name == "diff_from_parent.diff" || name == "diff_from_base.diff"
}
