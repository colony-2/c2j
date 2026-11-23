// Package includetest carries case-local include replacements across the native
// compiler and recipe test worker boundary.
package includetest

import (
	"context"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const TaskType = "c2j_recipe_test_include"

type Invocation struct {
	NodePath     string                   `json:"node_path"`
	Cell         string                   `json:"cell"`
	Inputs       map[string]any           `json:"inputs"`
	ArtifactRefs map[string]artifacts.Ref `json:"artifact_refs,omitempty"`
}

type Result struct {
	Outputs map[string]any         `json:"outputs,omitempty"`
	Failure *jobdb.AppErrorPayload `json:"failure,omitempty"`
}

type Control struct {
	Matches func(jobdb.JobKey, string) bool
	Reject  func(string, error) error
}

type controlKey struct{}

func WithControl(ctx context.Context, control Control) context.Context {
	return context.WithValue(ctx, controlKey{}, control)
}

func FromContext(ctx context.Context) (Control, bool) {
	control, ok := ctx.Value(controlKey{}).(Control)
	return control, ok
}
