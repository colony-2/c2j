package grouping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"
)

// DecodeSpecInput avoids mapstructure's null elision and numeric coercion.
func DecodeSpecInput(input, target any) error {
	if err := validStrings(reflect.ValueOf(input)); err != nil {
		return err
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func validStrings(v reflect.Value) error {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return validStrings(v.Elem())
		}
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return fmt.Errorf("recipe submission must contain valid UTF-8")
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			if err := validStrings(it.Key()); err != nil {
				return err
			}
			if err := validStrings(it.Value()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := validStrings(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := validStrings(v.Field(i)); err != nil {
				return err
			}
		}
	}
	return nil
}
