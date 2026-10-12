package compiler

import (
	"github.com/colony-2/c2j/pkg/configuration"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"google.golang.org/protobuf/types/known/structpb"
	"reflect"
)

func validateOpInputType(inputType reflect.Type, input map[string]any, allowNulls bool) error {
	if configuration.Contains(input) {
		_, err := configuration.Transform(input, func(r configuration.Reference) (any, error) { return r, nil })
		return err
	}
	return coreops.ValidateOpInputType(inputType, input, allowNulls)
}

func isNullValue(value interface{}) bool {
	switch v := value.(type) {
	case structpb.NullValue:
		return true
	case *structpb.Value:
		if v == nil {
			return true
		}
		if _, ok := v.Kind.(*structpb.Value_NullValue); ok {
			return true
		}
	}
	return false
}
