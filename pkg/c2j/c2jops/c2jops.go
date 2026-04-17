package c2jops

import (
	"github.com/colony-2/c2j/pkg/child/recipe"
	coreops "github.com/colony-2/c2j/pkg/core/ops"
	gitexport "github.com/colony-2/c2j/pkg/git/export"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	workerexport "github.com/colony-2/c2j/pkg/worker/export"
)

// Register resets the global op registry and installs the c2j-safe op set.
func Register() []coreops.RegisterableOp {
	impls := Ops()
	coreops.Replace(impls...)
	return impls
}

// Ops returns the ops that are safe to register in the c2j runtime.
func Ops() []coreops.RegisterableOp {
	impls := []coreops.RegisterableOp{extensions.GetExecutionOp()}
	impls = append(impls, workerexport.GetAll()...)
	impls = append(impls, input.GetOp())
	impls = append(impls, input.GetAutoFillOp())
	impls = append(impls, recipe.GetOps()...)
	impls = append(impls, gitexport.GetAll()...)
	return impls
}
