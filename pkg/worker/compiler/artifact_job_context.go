package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type thinpackForwarder struct {
	history        taskHistoryReader
	inner          jobworkflow.JobContext
	lastThinpack   jobdb.Artifact
	workspaces     map[string]jobdb.Artifact
	scopedMode     bool
	rootScopeID    string
	rootArtifacts  []jobdb.Artifact
	initialRestore map[string]*jobdb.ArtifactKey
}

func (a *thinpackForwarder) ClientPayload() json.RawMessage {
	return a.inner.ClientPayload()
}

func (a *thinpackForwarder) ClientPayloadRevision() int64 {
	return a.inner.ClientPayloadRevision()
}

func (a *thinpackForwarder) Yield(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	return a.inner.Yield(ctx, req)
}

func (a *thinpackForwarder) AwaitJobs(jobIds ...string) error {
	return a.inner.AwaitJobs(jobIds...)
}

func (a *thinpackForwarder) SubmitJob(ctx context.Context, submit jobdb.SubmitJob) (jobdb.JobKey, error) {
	return a.inner.SubmitJob(ctx, submit)
}

func (a *thinpackForwarder) SubmitRestartJob(ctx context.Context, restart jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return a.inner.SubmitRestartJob(ctx, restart)
}

func newThinPackForwardingJobContext(inner jobworkflow.JobContext) *thinpackForwarder {
	return &thinpackForwarder{inner: inner, workspaces: map[string]jobdb.Artifact{}}
}

func (a *thinpackForwarder) GetJobKey() jobdb.JobKey {
	return a.inner.GetJobKey()
}

func (a *thinpackForwarder) Logger() *slog.Logger {
	return a.inner.Logger()
}

func (a *thinpackForwarder) DoTask(policy jobdb.RunPolicy, taskType string, data jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := a.doTask(policy, taskType, data, func(data jobdb.TaskData) (jobdb.TaskData, error) {
		return a.inner.DoTask(policy, taskType, data)
	})
	return out, err
}

func (a *thinpackForwarder) DoValidationTask(policy jobdb.RunPolicy, taskType string, data jobdb.TaskData) (jobdb.TaskData, bool, error) {
	override, ok := a.inner.(validationTaskOverride)
	if !ok {
		return nil, false, nil
	}
	var handled bool
	var innerOut jobdb.TaskData
	out, err := a.doTask(policy, taskType, data, func(data jobdb.TaskData) (jobdb.TaskData, error) {
		var innerErr error
		innerOut, handled, innerErr = override.DoValidationTask(policy, taskType, data)
		return innerOut, innerErr
	})
	return out, handled, err
}

func (a *thinpackForwarder) doTask(policy jobdb.RunPolicy, taskType string, data jobdb.TaskData, invoke func(jobdb.TaskData) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	options := jobworkflow.TaskOptionsFor(data)
	_ = policy
	if !strings.Contains(taskType, ":") {
		return invoke(data)
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

	if options.Alternate != nil {
		data = jobworkflow.WithTaskOptions(data, options)
	}
	out, err := invoke(data)
	if err != nil {
		recovered, ok, recoveryErr := a.recoverArtifactOrder(taskType, data, err)
		if recoveryErr != nil {
			return nil, fmt.Errorf("restore cached task after artifact-order mismatch: %v: %w", recoveryErr, err)
		}
		if !ok {
			return out, err
		}
		out = recovered
	}
	if out == nil {
		return nil, nil
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
	if last = findThinPack(outputArtifacts); last != nil {
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

func (a *thinpackForwarder) AwaitDuration(waitFor jobdb.Duration) error {
	return a.inner.AwaitDuration(waitFor)
}

func (a *thinpackForwarder) executionTimeoutLimit() time.Duration {
	return activeExecutionTimeoutLimit(a.inner)
}

var _ jobworkflow.JobContext = &thinpackForwarder{}

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
