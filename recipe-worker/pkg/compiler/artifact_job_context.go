package compiler

import (
	"log/slog"

	"github.com/colony-2/c2j/git/pkg/gitstate"
	"github.com/colony-2/swf-go/pkg/swf"
)

type thinpackForwarder struct {
	inner        swf.JobContext
	lastThinpack swf.Artifact
}

func (a *thinpackForwarder) AwaitJobs(jobIds ...string) error {
	return a.inner.AwaitJobs(jobIds...)
}

func newThinPackForwardingJobContext(inner swf.JobContext) *thinpackForwarder {
	return &thinpackForwarder{inner: inner}
}

func (a *thinpackForwarder) GetJobKey() swf.JobKey {
	return a.inner.GetJobKey()
}

func (a *thinpackForwarder) Logger() *slog.Logger {
	return a.inner.Logger()
}

func (a *thinpackForwarder) DoTask(policy swf.RunPolicy, taskType string, data swf.TaskData) (swf.TaskData, error) {
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

		combined := make([]swf.Artifact, 0, len(inputArtifacts)+1)
		combined = append(combined, inputArtifacts...)
		combined = append(combined, last)
		data = &swf.SimpleTaskData{
			Data:      payload,
			Artifacts: combined,
		}
	}

	out, err := a.inner.DoTask(policy, taskType, data)
	if err != nil {
		return out, err
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

func findThinPack(artifacts []swf.Artifact) swf.Artifact {
	for _, ele := range artifacts {
		if ele.Name() == gitstate.ThinPackArtifactName {
			return ele
		}
	}
	return nil
}

func (a *thinpackForwarder) AwaitDuration(waitFor swf.Duration) error {
	return a.inner.AwaitDuration(waitFor)
}

var _ swf.JobContext = &thinpackForwarder{}
