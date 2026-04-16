package task

import "github.com/colony-2/swf-go/pkg/swf"

type Context struct {
	swf.TaskContext
	inputArtifacts  []swf.Artifact
	outputArtifacts []swf.Artifact
}

func (c *Context) InputArtifacts() []swf.Artifact {
	return c.inputArtifacts
}

func (c *Context) AddOutputArtifact(a swf.Artifact) {
	c.outputArtifacts = append(c.outputArtifacts, a)
}
