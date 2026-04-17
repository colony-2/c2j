package test_fixtures_test

import (
	"context"
	"sync"

	childops "github.com/colony-2/c2j/pkg/child/recipe"
	coreops "github.com/colony-2/c2j/pkg/core/ops"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/worker/commandop"
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
