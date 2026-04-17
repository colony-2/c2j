package export

import (
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/sleepop"
	"github.com/colony-2/c2j/pkg/worker/commandop"
)

func GetAll() []ops.RegisterableOp {
	return []ops.RegisterableOp{
		commandop.GetOp(),
		sleepop.GetOp(),
	}
}
