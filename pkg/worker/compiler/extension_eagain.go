package compiler

import (
	"context"
	"errors"

	"github.com/colony-2/c2j/internal/extensioneagain"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

const extensionEagainWaitType = "c2j-extension-eagain-wait"

type extensionEagainWaitWorker struct{}

func (extensionEagainWaitWorker) Name() string { return extensionEagainWaitType }

func (worker extensionEagainWaitWorker) Run(task wf.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return worker.RunTask(task.Context(), task, input)
}

func (extensionEagainWaitWorker) RunTask(local context.Context, task wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	if err := extensioneagain.Wait(local, task); err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(struct{}{})
}

func continuationInfrastructure(err error) error {
	if err == nil {
		err = errors.New("missing extension EAGAIN state")
	}
	return jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDurableDecode, "c2j.extension_eagain", nil, nil)
}
