package joblabelop

import (
	"context"

	"github.com/colony-2/c2j/internal/joblabelscope"
	"github.com/colony-2/c2j/pkg/joblabel"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Input struct {
	Action          string          `json:"action" validate:"required"`
	Key             string          `json:"key" validate:"required"`
	ExpectedVersion string          `json:"expected_version" validate:"required"`
	Value           *string         `json:"value,omitempty"`
	Color           *joblabel.Color `json:"color,omitempty"`
}

func GetOp() ops.RegisterableOp {
	return ops.NewActivityMappedOpV2[Input, joblabel.State](ops.OpMetadata{
		Type: joblabelscope.Builtin, Description: "Make one versioned edit to the current job's labels", Version: "1.0.0",
	}, execute)
}

func execute(_ ops.OpDependencies, ctx context.Context, input Input) (joblabel.State, error) {
	edit := joblabelscope.BuiltinEdit(ctx)
	if edit == nil {
		return joblabel.State{}, jobdb.ErrLeasePermissionDenied
	}
	version, err := joblabel.ParseVersion(input.ExpectedVersion)
	if err != nil {
		return joblabel.State{}, err
	}
	return edit(ctx, joblabel.Edit{Action: input.Action, Key: input.Key, ExpectedVersion: &version, Value: input.Value, Color: input.Color})
}
