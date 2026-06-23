package compiler

import (
	"log/slog"
	"time"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type thinpackForwarder struct {
	inner        jobworkflow.JobContext
	lastThinpack jobdb.Artifact
}

func (a *thinpackForwarder) AwaitJobs(jobIds ...string) error {
	return a.inner.AwaitJobs(jobIds...)
}

func newThinPackForwardingJobContext(inner jobworkflow.JobContext) *thinpackForwarder {
	return &thinpackForwarder{inner: inner}
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
	_ = policy
	_ = taskType
	last := a.lastThinpack

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

	out, err := invoke(data)
	if err != nil {
		return out, err
	}
	if out == nil {
		return nil, nil
	}
	outputArtifacts, err2 := out.GetArtifacts()
	if err2 != nil {
		return out, err
	}
	if last = findThinPack(outputArtifacts); last != nil {
		a.lastThinpack = last
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
