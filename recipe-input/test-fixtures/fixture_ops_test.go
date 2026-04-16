package test_fixtures_test

import (
	"context"
	"sync"

	coreops "github.com/colony-2/c2j/recipe-core/pkg/ops"
	childops "github.com/colony-2/c2j/recipe-child/pkg/recipe"
	"github.com/colony-2/c2j/recipe-input/pkg/input"
	"github.com/colony-2/c2j/recipe-worker/pkg/commandop"
)

type fixtureOps struct {
	inputOp coreops.RegisterableOp
}

var (
	fixtureOpsOnce sync.Once
	fixtureOpsInst fixtureOps
)

func ensureFixtureOps() fixtureOps {
	fixtureOpsOnce.Do(func() {
		coreops.Register(childops.GetOps()...)

		fixtureOpsInst.inputOp = input.GetOp()
		coreops.Register(fixtureOpsInst.inputOp)
		coreops.Register(input.GetAutoFillOp())
		coreops.Register(commandop.GetOp())

		coreops.Register(coreops.NewActivityMappedOpV2[echoInput, echoOutput](
			coreops.OpMetadata{Type: "echo"},
			func(_ coreops.OpDependencies, _ context.Context, in echoInput) (echoOutput, error) {
				return echoOutput{Output: in.Message}, nil
			},
		))
	})
	return fixtureOpsInst
}

