package export

import (
	"github.com/colony-2/c2j/pkg/core/ops"
	"github.com/colony-2/c2j/pkg/git/gitcollector"
	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/git/gitshallow"
	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
)

// GetAll returns all Git activities available in this module
// This provides a single entry point for consumers to discover and register all Git activities
func GetAll() []ops.RegisterableOp {
	return []ops.RegisterableOp{
		gitcollector.GetOp(),
		gitshallow.GetOp(),
		gitcommit.GetPersistOp(),
		gitcommit.GetRestoreOp(),
		thinpackrebase.GetOp(),
		squashrebasemerge.GetOp(),
	}
}
