package ops

import (
	recipeartifacts "github.com/colony-2/c2j/pkg/core/artifacts"
	"github.com/colony-2/c2j/pkg/core/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	"gorm.io/gorm"
)

// testDeps is a minimal OpDependencies test double.
type testDeps struct {
	artifacts []swf.Artifact
}

func (d *testDeps) JobTool() JobTool {
	return nil
}

func (d *testDeps) FindArtifact(key swf.ArtifactKey) (swf.Artifact, error) {
	return nil, nil
}

func (d *testDeps) AddOutputArtifact(artifact swf.Artifact) error {
	return nil
}

func (d *testDeps) AddExternalArtifact(name string, url string, expand bool) error {
	_, _, _ = name, url, expand
	return nil
}

func (d *testDeps) GetOutputArtifacts() []swf.Artifact {
	return nil
}

func (d *testDeps) GetExternalArtifacts() map[string]recipeartifacts.Ref {
	return nil
}

func (d *testDeps) Database() *gorm.DB { return nil }

func (d *testDeps) AddArtifact(a swf.Artifact) error {
	d.artifacts = append(d.artifacts, a)
	return nil
}

func (d *testDeps) GetInputArtifacts() []swf.Artifact { return d.artifacts }

func (d *testDeps) WorkflowControl() workflowctl.WorkflowControl { return nil }

func (d *testDeps) WorktreePath() string { return "" }

func (d *testDeps) GitContext() GitExecutionContext { return GitExecutionContext{} }

func (d *testDeps) SetNextTaskType(taskType string) {
	_ = taskType
}

var _ OpDependencies = &testDeps{}
