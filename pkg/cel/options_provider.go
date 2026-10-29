package cel

import (
	googlecel "github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

// OptionsProvider supplies CEL type options and adapter-dependent functions.
type OptionsProvider interface {
	TypeOptions() []googlecel.EnvOption
	FunctionOptions(adapter types.Adapter) ([]googlecel.EnvOption, error)
}
