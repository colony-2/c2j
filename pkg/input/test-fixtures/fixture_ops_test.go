package test_fixtures_test

import (
	"context"
	"sync"

	"github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	childops "github.com/colony-2/c2j/pkg/ops/recipe"
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
