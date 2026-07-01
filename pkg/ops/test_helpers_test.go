package ops

import (
	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gorm.io/gorm"
)

// testDeps is a minimal OpDependencies test double.
type testDeps struct {
	artifacts []jobdb.Artifact
}

func (d *testDeps) JobTool() JobTool {
	return nil
}

func (d *testDeps) FindArtifact(key jobdb.ArtifactKey) (jobdb.Artifact, error) {
	return nil, nil
}

func (d *testDeps) AddOutputArtifact(artifact jobdb.Artifact) error {
	return nil
}

func (d *testDeps) AddExternalArtifact(name string, url string, expand bool) error {
	_, _, _ = name, url, expand
	return nil
}

func (d *testDeps) GetOutputArtifacts() []jobdb.Artifact {
	return nil
}

func (d *testDeps) GetExternalArtifacts() map[string]recipeartifacts.Ref {
	return nil
}

func (d *testDeps) Database() *gorm.DB { return nil }

func (d *testDeps) AddArtifact(a jobdb.Artifact) error {
	d.artifacts = append(d.artifacts, a)
	return nil
}

func (d *testDeps) GetInputArtifacts() []jobdb.Artifact { return d.artifacts }

func (d *testDeps) WorkflowControl() workflowctl.WorkflowControl { return nil }

func (d *testDeps) WorktreePath() string { return "" }

func (d *testDeps) GitContext() GitExecutionContext { return GitExecutionContext{} }

func (d *testDeps) CurrentJobContext() jobcontext.Current { return jobcontext.Current{} }

func (d *testDeps) SetNextTaskType(taskType string) {
	_ = taskType
}

var _ OpDependencies = &testDeps{}
