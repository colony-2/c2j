package export

import (
	"github.com/colony-2/c2j/pkg/core/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/sleepop"
)

func GetAll() []ops.RegisterableOp {
	return []ops.RegisterableOp{
		commandop.GetOp(),
		sleepop.GetOp(),
	}
}
