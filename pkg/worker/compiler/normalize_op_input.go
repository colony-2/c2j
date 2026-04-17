package compiler

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/core/artifacts"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/mitchellh/mapstructure"
)

var (
	artifactKeyType = reflect.TypeOf(swf.ArtifactKey{})
	artifactRefType = reflect.TypeOf(recipeartifacts.Ref{})
)

type NormalizedOpInput struct {
	Data               map[string]interface{}
	StoredArtifactKeys []swf.ArtifactKey
}

type artifactKeyAccumulator struct {
	seen map[string]swf.ArtifactKey
}

func newArtifactKeyAccumulator() *artifactKeyAccumulator {
	return &artifactKeyAccumulator{
		seen: make(map[string]swf.ArtifactKey),
	}
}

func (a *artifactKeyAccumulator) Add(key swf.ArtifactKey) error {
	if err := key.Validate(); err != nil {
		return err
	}
	a.seen[artifactKeyIdentity(key)] = key
	return nil
}

func (a *artifactKeyAccumulator) AddRef(ref recipeartifacts.Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	key, ok := ref.StoredKey()
	if !ok {
		return nil
	}
	return a.Add(key)
}

func (a *artifactKeyAccumulator) Keys() []swf.ArtifactKey {
	out := make([]swf.ArtifactKey, 0, len(a.seen))
	for _, key := range a.seen {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		return artifactKeyIdentity(out[i]) < artifactKeyIdentity(out[j])
	})
	return out
}

func NormalizeOpInput(inputType reflect.Type, raw map[string]interface{}) (NormalizedOpInput, error) {
	if raw == nil {
		return NormalizedOpInput{}, nil
	}

	acc := newArtifactKeyAccumulator()
	targetType := derefType(inputType)
	if targetType == nil || targetType.Kind() != reflect.Struct {
		normalized, err := normalizeDynamicMap(raw, acc, "")
		if err != nil {
			return NormalizedOpInput{}, err
		}
		return NormalizedOpInput{
			Data:               normalized,
			StoredArtifactKeys: acc.Keys(),
		}, nil
	}

	out := cloneStringMap(raw)
	if err := normalizeStructMap(targetType, raw, out, acc, ""); err != nil {
		return NormalizedOpInput{}, err
	}
	return NormalizedOpInput{
		Data:               out,
		StoredArtifactKeys: acc.Keys(),
	}, nil
}

func normalizeStructMap(targetType reflect.Type, raw map[string]interface{}, out map[string]interface{}, acc *artifactKeyAccumulator, path string) error {
	matched := make(map[string]struct{}, len(raw))
	remainElemType, err := normalizeStructFields(targetType, raw, out, acc, path, matched)
	if err != nil {
		return err
	}
	if remainElemType == nil {
		return nil
	}

	for key, value := range raw {
		if _, ok := matched[key]; ok {
			continue
		}
		normalized, err := normalizeValueForType(remainElemType, value, acc, joinFieldPath(path, key))
		if err != nil {
			return err
		}
		out[key] = normalized
	}
	return nil
}

func normalizeStructFields(targetType reflect.Type, raw map[string]interface{}, out map[string]interface{}, acc *artifactKeyAccumulator, path string, matched map[string]struct{}) (reflect.Type, error) {
	var remainElemType reflect.Type

	for i := 0; i < targetType.NumField(); i++ {
		field := targetType.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}

		if isRemainField(field) {
			if remainElemType == nil {
				remainElemType = remainMapElementType(field.Type)
			}
			continue
		}

		if shouldInlineAnonymousField(field) {
			nestedRemainElemType, err := normalizeStructFields(derefType(field.Type), raw, out, acc, path, matched)
			if err != nil {
				return nil, err
			}
			if remainElemType == nil {
				remainElemType = nestedRemainElemType
			}
			continue
		}

		name, ok := jsonFieldName(field)
		if !ok {
			continue
		}

		value, exists := raw[name]
		if !exists {
			continue
		}

		normalized, err := normalizeValueForType(field.Type, value, acc, joinFieldPath(path, name))
		if err != nil {
			return nil, err
		}
		out[name] = normalized
		matched[name] = struct{}{}
	}

	return remainElemType, nil
}

func normalizeValueForType(targetType reflect.Type, value interface{}, acc *artifactKeyAccumulator, path string) (interface{}, error) {
	if value == nil || isNullValue(value) {
		return value, nil
	}

	targetType = derefType(targetType)
	if targetType == nil {
		return value, nil
	}

	switch targetType {
	case artifactKeyType:
		return normalizeStoredArtifactKeyValue(value, acc, path)
	case artifactRefType:
		return normalizeArtifactRefValue(value, acc, path)
	}

	switch targetType.Kind() {
	case reflect.Struct:
		rawMap, ok := toStringInterfaceMap(value)
		if !ok {
			return value, nil
		}
		out := cloneStringMap(rawMap)
		if err := normalizeStructMap(targetType, rawMap, out, acc, path); err != nil {
			return nil, err
		}
		return out, nil

	case reflect.Slice, reflect.Array:
		if targetType.Elem().Kind() == reflect.Uint8 {
			return value, nil
		}
		return normalizeSliceValue(targetType.Elem(), value, acc, path)

	case reflect.Map:
		if targetType.Key().Kind() != reflect.String {
			return value, nil
		}
		return normalizeMapValue(targetType.Elem(), value, acc, path)

	case reflect.Interface:
		return normalizeDynamicValue(value, acc, path)

	default:
		return value, nil
	}
}

func normalizeStoredArtifactKeyValue(value interface{}, acc *artifactKeyAccumulator, path string) (interface{}, error) {
	artifactRef, err := coerceArtifactRef(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", displayPath(path), err)
	}

	key, ok := artifactRef.StoredKey()
	if !ok {
		return nil, fmt.Errorf("%s expects a stored artifact, got external artifact ref", displayPath(path))
	}
	if err := acc.Add(key); err != nil {
		return nil, fmt.Errorf("%s: %w", displayPath(path), err)
	}
	return key, nil
}

func normalizeArtifactRefValue(value interface{}, acc *artifactKeyAccumulator, path string) (interface{}, error) {
	artifactRef, err := coerceArtifactRef(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", displayPath(path), err)
	}
	if err := acc.AddRef(artifactRef); err != nil {
		return nil, fmt.Errorf("%s: %w", displayPath(path), err)
	}
	return artifactRef, nil
}

func normalizeSliceValue(elemType reflect.Type, value interface{}, acc *artifactKeyAccumulator, path string) (interface{}, error) {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return value, nil
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return value, nil
	}
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		return nil, nil
	}

	out := make([]interface{}, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		normalized, err := normalizeValueForType(elemType, rv.Index(i).Interface(), acc, joinIndexPath(path, i))
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeMapValue(elemType reflect.Type, value interface{}, acc *artifactKeyAccumulator, path string) (interface{}, error) {
	rawMap, ok := toStringInterfaceMap(value)
	if !ok {
		return value, nil
	}
	out := make(map[string]interface{}, len(rawMap))
	for key, item := range rawMap {
		normalized, err := normalizeValueForType(elemType, item, acc, joinFieldPath(path, key))
		if err != nil {
			return nil, err
		}
		out[key] = normalized
	}
	return out, nil
}

func normalizeDynamicMap(raw map[string]interface{}, acc *artifactKeyAccumulator, path string) (map[string]interface{}, error) {
	out := make(map[string]interface{}, len(raw))
	for key, value := range raw {
		normalized, err := normalizeDynamicValue(value, acc, joinFieldPath(path, key))
		if err != nil {
			return nil, err
		}
		out[key] = normalized
	}
	return out, nil
}

func normalizeDynamicValue(value interface{}, acc *artifactKeyAccumulator, path string) (interface{}, error) {
	if value == nil || isNullValue(value) {
		return value, nil
	}

	switch v := value.(type) {
	case recipeartifacts.Ref:
		if err := acc.AddRef(v); err != nil {
			return nil, fmt.Errorf("%s: %w", displayPath(path), err)
		}
		return v, nil

	case *recipeartifacts.Ref:
		if v == nil {
			return nil, nil
		}
		return normalizeDynamicValue(*v, acc, path)

	case swf.ArtifactKey:
		if err := acc.Add(v); err != nil {
			return nil, fmt.Errorf("%s: %w", displayPath(path), err)
		}
		return v, nil

	case *swf.ArtifactKey:
		if v == nil {
			return nil, nil
		}
		return normalizeDynamicValue(*v, acc, path)
	}

	if keyer, ok := value.(interface {
		ArtifactKey() (swf.ArtifactKey, error)
	}); ok {
		key, err := keyer.ArtifactKey()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", displayPath(path), err)
		}
		if err := acc.Add(key); err != nil {
			return nil, fmt.Errorf("%s: %w", displayPath(path), err)
		}
		return value, nil
	}

	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return value, nil
	}

	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return value, nil
		}
		out := make([]interface{}, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			normalized, err := normalizeDynamicValue(rv.Index(i).Interface(), acc, joinIndexPath(path, i))
			if err != nil {
				return nil, err
			}
			out = append(out, normalized)
		}
		return out, nil

	case reflect.Map:
		rawMap, ok := toStringInterfaceMap(value)
		if !ok {
			return value, nil
		}
		out := make(map[string]interface{}, len(rawMap))
		for key, item := range rawMap {
			normalized, err := normalizeDynamicValue(item, acc, joinFieldPath(path, key))
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil

	default:
		return value, nil
	}
}

func decodeJSONTaggedMap(input map[string]interface{}, out interface{}) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		TagName: "json",
		Result:  out,
	})
	if err != nil {
		return err
	}
	return decoder.Decode(input)
}

func toStringInterfaceMap(value interface{}) (map[string]interface{}, bool) {
	switch v := value.(type) {
	case map[string]interface{}:
		return v, true
	}

	rv := reflect.ValueOf(value)
	if !rv.IsValid() || rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return nil, false
	}

	out := make(map[string]interface{}, rv.Len())
	for _, key := range rv.MapKeys() {
		out[key.String()] = rv.MapIndex(key).Interface()
	}
	return out, true
}

func cloneStringMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func isRemainField(field reflect.StructField) bool {
	for _, option := range strings.Split(field.Tag.Get("mapstructure"), ",")[1:] {
		if option == "remain" {
			return true
		}
	}
	return false
}

func remainMapElementType(fieldType reflect.Type) reflect.Type {
	fieldType = derefType(fieldType)
	if fieldType == nil || fieldType.Kind() != reflect.Map || fieldType.Key().Kind() != reflect.String {
		return nil
	}
	return fieldType.Elem()
}

func shouldInlineAnonymousField(field reflect.StructField) bool {
	if !field.Anonymous {
		return false
	}
	if tagName := strings.Split(field.Tag.Get("json"), ",")[0]; tagName != "" {
		return false
	}
	targetType := derefType(field.Type)
	if targetType == nil || targetType.Kind() != reflect.Struct {
		return false
	}
	return targetType != artifactKeyType && targetType != artifactRefType
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tagName := strings.Split(field.Tag.Get("json"), ",")[0]
	switch tagName {
	case "-":
		return "", false
	case "":
		return field.Name, true
	default:
		return tagName, true
	}
}

func joinFieldPath(base string, name string) string {
	if base == "" {
		return name
	}
	return base + "." + name
}

func joinIndexPath(base string, idx int) string {
	if base == "" {
		return fmt.Sprintf("[%d]", idx)
	}
	return fmt.Sprintf("%s[%d]", base, idx)
}

func displayPath(path string) string {
	if path == "" {
		return "value"
	}
	return path
}
