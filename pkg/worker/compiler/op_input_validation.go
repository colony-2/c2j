package compiler

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	coreops "github.com/colony-2/c2j/pkg/core/ops"
	"github.com/go-playground/validator/v10"
	"github.com/mitchellh/mapstructure"
	"google.golang.org/protobuf/types/known/structpb"
)

var (
	opInputValidatorOnce sync.Once
	opInputValidator     *validator.Validate
	opInputValidatorErr  error
)

func getOpInputValidator() (*validator.Validate, error) {
	opInputValidatorOnce.Do(func() {
		v := validator.New()
		if err := v.RegisterValidation("dir", validateDirTag); err != nil {
			opInputValidatorErr = err
			return
		}
		opInputValidator = v
	})
	return opInputValidator, opInputValidatorErr
}

func validateDirTag(fl validator.FieldLevel) bool {
	field := fl.Field()
	if field.Kind() == reflect.Pointer {
		if field.IsNil() {
			return true
		}
		field = field.Elem()
	}
	if field.Kind() != reflect.String {
		return false
	}
	return strings.TrimSpace(field.String()) != ""
}

func validateOpInputType(inputType reflect.Type, input map[string]interface{}, allowNulls bool) error {
	if inputType == nil {
		return nil
	}
	if inputType.Kind() == reflect.Pointer {
		inputType = inputType.Elem()
	}
	if inputType.Kind() != reflect.Struct {
		return nil
	}

	if allowNulls {
		input = stripNullValues(input)
	}

	target := reflect.New(inputType).Interface()
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName:     "json",
		Result:      target,
		ErrorUnused: true,
		DecodeHook:  coreops.DecodeHookMapDecoder,
	})
	if err != nil {
		return err
	}
	if err := decoder.Decode(input); err != nil {
		return fmt.Errorf("decode op input: %w", err)
	}

	v, err := getOpInputValidator()
	if err != nil {
		return err
	}
	if err := v.Struct(target); err != nil {
		if allowNulls {
			if filtered := filterRequiredValidationErrors(err); filtered == nil {
				return nil
			} else {
				err = filtered
			}
		}
		return fmt.Errorf("validate op input: %w", err)
	}
	return nil
}

func stripNullValues(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	cleaned := make(map[string]interface{}, len(input))
	for key, value := range input {
		if isNullValue(value) {
			continue
		}
		cleaned[key] = stripNullValue(value)
	}
	return cleaned
}

func stripNullValue(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		return stripNullValues(v)
	case []interface{}:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			if isNullValue(item) {
				continue
			}
			out = append(out, stripNullValue(item))
		}
		return out
	default:
		return value
	}
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

func filterRequiredValidationErrors(err error) error {
	verrs, ok := err.(validator.ValidationErrors)
	if !ok {
		return err
	}
	remaining := make(validator.ValidationErrors, 0, len(verrs))
	for _, verr := range verrs {
		switch verr.Tag() {
		case "required", "required_if", "required_without", "required_with", "required_without_all", "required_with_all":
			continue
		default:
			remaining = append(remaining, verr)
		}
	}
	if len(remaining) == 0 {
		return nil
	}
	return remaining
}
