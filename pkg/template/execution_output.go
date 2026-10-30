package template

import (
	"errors"
	"fmt"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// ExecutionOutput is prepared by a scope body and published by its caller only
// after the execution engine accepts the scope outcome. Values are immutable
// after publication; preparing this record does not copy them or update a map.
type ExecutionOutput struct {
	Outputs      map[string]any
	ArtifactRefs map[string]recipeartifacts.Ref
	Artifacts    []jobdb.Artifact
	Jobs         jobcontext.StartedJobsContext
}

// PublishExecution is called by serial orchestration after DoScope accepts the
// result. Tasks must never receive a resolver or call this publication method.
func (scope *ResolutionContext) PublishExecution(output ExecutionOutput) error {
	if scope.completion.written {
		return fmt.Errorf("%w: %s", ErrAlreadyWritten, scope.TemplateData.Context.Invocation.NodePath)
	}
	scope.completion.written = true
	scope.appendExecution(output.Outputs, output.ArtifactRefs, output.Artifacts, output.Jobs)
	// Workspace wrappers have returned by publication time. Forward their
	// result to the caller while retaining workspace-local Git state.
	for current := scope; current != nil; current = current.Parent {
		if current.workspaceCaller != nil {
			current.workspaceCaller.PublishWorkspaceResult(current)
		}
	}
	return nil
}

var ErrAlreadyWritten = errors.New("context position is already written")

// Views of one invocation share its completion position. A later visit to the
// same logical state gets a fresh position through NewChildContext.
type completionPosition struct{ written bool }
