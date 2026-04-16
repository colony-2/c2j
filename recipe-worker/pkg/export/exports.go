package export

import (
	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	"github.com/colony-2/c2j/recipe-worker/pkg/commandop"
	"github.com/colony-2/c2j/recipe-worker/pkg/sleepop"
)

func GetAll() []ops.RegisterableOp {
	return []ops.RegisterableOp{
		commandop.GetOp(),
		sleepop.GetOp(),
	}
}
