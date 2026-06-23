package task

import (
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type Context struct {
	jobworkflow.TaskContext
	inputArtifacts  []jobdb.Artifact
	outputArtifacts []jobdb.Artifact
}

func (c *Context) InputArtifacts() []jobdb.Artifact {
	return c.inputArtifacts
}

func (c *Context) AddOutputArtifact(a jobdb.Artifact) {
	c.outputArtifacts = append(c.outputArtifacts, a)
}
