package extensions

import (
	"fmt"
	"reflect"

	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
)

type defaultsKind int

const (
	defaultsKindScalar defaultsKind = iota
	defaultsKindObject
	defaultsKindArray
)

// InputDefaults applies schema-backed defaults to extension-op invocation inputs.
type InputDefaults struct {
	root *defaultsNode
}

type defaultsNode struct {
	schema          *jsonschemav6.Schema
	kind            defaultsKind
	explicitDefault interface{}
	objectProps     map[string]*defaultsNode
	arrayItem       *defaultsNode
}

func BuildInputDefaults(raw map[string]any, compiled *jsonschemav6.Schema) (*InputDefaults, error) {
	if !schemaHasDefaultKeyword(raw) {
		return nil, nil
	}
	if compiled == nil {
		return nil, fmt.Errorf("input_schema defaults require a valid compiled schema")
	}

	seen := map[*jsonschemav6.Schema]visitState{}
	if err := validateDefaultLocations(compiled, "input_schema", true, seen); err != nil {
		return nil, err
	}

	node, err := buildDefaultsNode(compiled, "input_schema", map[*jsonschemav6.Schema]*defaultsNode{}, map[*jsonschemav6.Schema]visitState{})
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, nil
	}
	return &InputDefaults{root: node}, nil
}

func (d *InputDefaults) Apply(input map[string]interface{}) (bool, error) {
	if d == nil || d.root == nil || input == nil {
		return false, nil
	}

	value, changed, err := d.root.applyToValue(input, true)
	if err != nil {
		return false, err
	}
	if _, ok := value.(map[string]interface{}); !ok {
		return false, fmt.Errorf("input_schema defaults produced non-object root value %T", value)
	}
	return changed, nil
}

func buildDefaultsNode(schema *jsonschemav6.Schema, path string, memo map[*jsonschemav6.Schema]*defaultsNode, states map[*jsonschemav6.Schema]visitState) (*defaultsNode, error) {
	resolved, err := derefSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if resolved == nil {
		return nil, nil
	}
	if existing, ok := memo[resolved]; ok {
		return existing, nil
	}
	if states[resolved] == visitStateVisiting {
		return nil, fmt.Errorf("%s: cyclic schema references are not supported for input defaults", path)
	}
	if states[resolved] == visitStateDone {
		return memo[resolved], nil
	}

	states[resolved] = visitStateVisiting
	node := &defaultsNode{
		schema: resolved,
		kind:   detectDefaultsKind(resolved),
	}
	memo[resolved] = node

	if resolved.Default != nil {
		node.explicitDefault = deepCopyAny(*resolved.Default)
	}

	for name, prop := range resolved.Properties {
		child, err := buildDefaultsNode(prop, path+".properties."+name, memo, states)
		if err != nil {
			return nil, err
		}
		if child != nil {
			if node.objectProps == nil {
				node.objectProps = make(map[string]*defaultsNode)
			}
			node.objectProps[name] = child
			node.kind = defaultsKindObject
		}
	}

	switch items := resolved.Items.(type) {
	case *jsonschemav6.Schema:
		child, err := buildDefaultsNode(items, path+".items", memo, states)
		if err != nil {
			return nil, err
		}
		if child != nil {
			node.arrayItem = child
			node.kind = defaultsKindArray
		}
	}
	if resolved.Items2020 != nil {
		child, err := buildDefaultsNode(resolved.Items2020, path+".items", memo, states)
		if err != nil {
			return nil, err
		}
		if child != nil {
			node.arrayItem = child
			node.kind = defaultsKindArray
		}
	}

	if node.explicitDefault == nil && len(node.objectProps) == 0 && node.arrayItem == nil {
		delete(memo, resolved)
		states[resolved] = visitStateDone
		return nil, nil
	}

	if node.explicitDefault != nil {
		value, _, err := node.applyDescendantsToValue(deepCopyAny(node.explicitDefault), true)
		if err != nil {
			return nil, fmt.Errorf("%s default: %w", path, err)
		}
		if err := resolved.Validate(value); err != nil {
			return nil, fmt.Errorf("%s default failed schema validation: %w", path, err)
		}
	}

	states[resolved] = visitStateDone
	return node, nil
}

type visitState int

const (
	visitStateUnknown visitState = iota
	visitStateVisiting
	visitStateDone
)

func validateDefaultLocations(schema *jsonschemav6.Schema, path string, supported bool, seen map[*jsonschemav6.Schema]visitState) error {
	resolved, err := derefSchema(schema)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if resolved == nil {
		return nil
	}
	if seen[resolved] == visitStateVisiting {
		return fmt.Errorf("%s: cyclic schema references are not supported for input defaults", path)
	}
	if seen[resolved] == visitStateDone {
		return nil
	}

	seen[resolved] = visitStateVisiting
	defer func() {
		seen[resolved] = visitStateDone
	}()

	if !supported && resolved.Default != nil {
		return fmt.Errorf("%s uses defaults in an unsupported schema location", path)
	}

	for name, prop := range resolved.Properties {
		if err := validateDefaultLocations(prop, path+".properties."+name, supported, seen); err != nil {
			return err
		}
	}

	switch items := resolved.Items.(type) {
	case *jsonschemav6.Schema:
		if err := validateDefaultLocations(items, path+".items", supported, seen); err != nil {
			return err
		}
	case []*jsonschemav6.Schema:
		for i, item := range items {
			if err := validateDefaultLocations(item, fmt.Sprintf("%s.items[%d]", path, i), false, seen); err != nil {
				return err
			}
		}
	}
	if resolved.Items2020 != nil {
		if err := validateDefaultLocations(resolved.Items2020, path+".items", supported, seen); err != nil {
			return err
		}
	}

	for i, item := range resolved.PrefixItems {
		if err := validateDefaultLocations(item, fmt.Sprintf("%s.prefixItems[%d]", path, i), false, seen); err != nil {
			return err
		}
	}
	for i, item := range resolved.AllOf {
		if err := validateDefaultLocations(item, fmt.Sprintf("%s.allOf[%d]", path, i), false, seen); err != nil {
			return err
		}
	}
	for i, item := range resolved.AnyOf {
		if err := validateDefaultLocations(item, fmt.Sprintf("%s.anyOf[%d]", path, i), false, seen); err != nil {
			return err
		}
	}
	for i, item := range resolved.OneOf {
		if err := validateDefaultLocations(item, fmt.Sprintf("%s.oneOf[%d]", path, i), false, seen); err != nil {
			return err
		}
	}

	if err := validateUnsupportedChild(resolved.Not, path+".not", seen); err != nil {
		return err
	}
	if err := validateUnsupportedChild(resolved.If, path+".if", seen); err != nil {
		return err
	}
	if err := validateUnsupportedChild(resolved.Then, path+".then", seen); err != nil {
		return err
	}
	if err := validateUnsupportedChild(resolved.Else, path+".else", seen); err != nil {
		return err
	}
	if err := validateUnsupportedChild(resolved.PropertyNames, path+".propertyNames", seen); err != nil {
		return err
	}
	if err := validateUnsupportedChild(resolved.Contains, path+".contains", seen); err != nil {
		return err
	}
	if err := validateUnsupportedChild(resolved.UnevaluatedItems, path+".unevaluatedItems", seen); err != nil {
		return err
	}
	if err := validateUnsupportedChild(resolved.UnevaluatedProperties, path+".unevaluatedProperties", seen); err != nil {
		return err
	}
	if err := validateUnsupportedChild(resolved.ContentSchema, path+".contentSchema", seen); err != nil {
		return err
	}

	for pattern, prop := range resolved.PatternProperties {
		if err := validateDefaultLocations(prop, fmt.Sprintf("%s.patternProperties[%s]", path, pattern), false, seen); err != nil {
			return err
		}
	}
	switch extra := resolved.AdditionalProperties.(type) {
	case *jsonschemav6.Schema:
		if err := validateDefaultLocations(extra, path+".additionalProperties", false, seen); err != nil {
			return err
		}
	}
	switch extra := resolved.AdditionalItems.(type) {
	case *jsonschemav6.Schema:
		if err := validateDefaultLocations(extra, path+".additionalItems", false, seen); err != nil {
			return err
		}
	}
	for name, dep := range resolved.Dependencies {
		if schemaDep, ok := dep.(*jsonschemav6.Schema); ok {
			if err := validateDefaultLocations(schemaDep, path+".dependencies."+name, false, seen); err != nil {
				return err
			}
		}
	}
	for name, dep := range resolved.DependentSchemas {
		if err := validateDefaultLocations(dep, path+".dependentSchemas."+name, false, seen); err != nil {
			return err
		}
	}

	return nil
}

func validateUnsupportedChild(schema *jsonschemav6.Schema, path string, seen map[*jsonschemav6.Schema]visitState) error {
	if schema == nil {
		return nil
	}
	return validateDefaultLocations(schema, path, false, seen)
}

func detectDefaultsKind(schema *jsonschemav6.Schema) defaultsKind {
	switch {
	case schemaLooksLikeObject(schema):
		return defaultsKindObject
	case schemaLooksLikeArray(schema):
		return defaultsKindArray
	default:
		return defaultsKindScalar
	}
}

func schemaLooksLikeObject(schema *jsonschemav6.Schema) bool {
	if schema == nil {
		return false
	}
	if len(schema.Properties) > 0 || len(schema.Required) > 0 {
		return true
	}
	if schema.MaxProperties != nil || schema.MinProperties != nil || schema.PropertyNames != nil {
		return true
	}
	if len(schema.PatternProperties) > 0 || schema.AdditionalProperties != nil || schema.UnevaluatedProperties != nil {
		return true
	}
	if len(schema.Dependencies) > 0 || len(schema.DependentRequired) > 0 || len(schema.DependentSchemas) > 0 {
		return true
	}
	if schema.Default == nil {
		return false
	}
	_, isMap := (*schema.Default).(map[string]interface{})
	return isMap
}

func schemaLooksLikeArray(schema *jsonschemav6.Schema) bool {
	if schema == nil {
		return false
	}
	if schema.Items != nil || schema.Items2020 != nil || len(schema.PrefixItems) > 0 || schema.Contains != nil {
		return true
	}
	if schema.MinItems != nil || schema.MaxItems != nil || schema.UniqueItems || schema.AdditionalItems != nil || schema.UnevaluatedItems != nil {
		return true
	}
	if schema.Default == nil {
		return false
	}
	_, isSlice := (*schema.Default).([]interface{})
	return isSlice
}

func (n *defaultsNode) applyToValue(current interface{}, present bool) (interface{}, bool, error) {
	if n == nil {
		return current, false, nil
	}

	switch n.kind {
	case defaultsKindObject:
		base, ok, converted := toStringMap(current)
		changed := converted
		if !present {
			base = map[string]interface{}{}
			ok = true
		}
		if !ok {
			return current, false, nil
		}

		if n.explicitDefault != nil {
			defMap, ok, _ := toStringMap(deepCopyAny(n.explicitDefault))
			if !ok {
				return nil, false, fmt.Errorf("object default must be a map, got %T", n.explicitDefault)
			}
			if mergeMissingKeys(base, defMap) {
				changed = true
			}
		}

		_, childChanged, err := n.applyDescendantsToValue(base, true)
		if err != nil {
			return nil, false, err
		}
		changed = changed || childChanged
		if !present && !changed {
			return current, false, nil
		}
		return base, changed || !present, nil

	case defaultsKindArray:
		if !present {
			if n.explicitDefault == nil {
				return current, false, nil
			}
			value, _, err := n.applyDescendantsToValue(deepCopyAny(n.explicitDefault), true)
			if err != nil {
				return nil, false, err
			}
			return value, true, nil
		}
		value, changed, err := n.applyDescendantsToValue(current, true)
		if err != nil {
			return nil, false, err
		}
		return value, changed, nil

	default:
		if present || n.explicitDefault == nil {
			return current, false, nil
		}
		return deepCopyAny(n.explicitDefault), true, nil
	}
}

func (n *defaultsNode) applyDescendantsToValue(current interface{}, present bool) (interface{}, bool, error) {
	if n == nil || !present {
		return current, false, nil
	}

	switch n.kind {
	case defaultsKindObject:
		base, ok, converted := toStringMap(current)
		if !ok {
			return current, false, nil
		}
		changed := converted
		for name, child := range n.objectProps {
			existing, childPresent := base[name]
			value, childChanged, err := child.applyToValue(existing, childPresent)
			if err != nil {
				return nil, false, err
			}
			if childChanged {
				base[name] = value
				changed = true
			}
		}
		return base, changed, nil

	case defaultsKindArray:
		items, ok, converted := toInterfaceSlice(current)
		if !ok {
			return current, false, nil
		}
		changed := converted
		if n.arrayItem == nil {
			return items, changed, nil
		}
		for i, item := range items {
			value, childChanged, err := n.arrayItem.applyToValue(item, true)
			if err != nil {
				return nil, false, err
			}
			if childChanged {
				items[i] = value
				changed = true
			}
		}
		return items, changed, nil

	default:
		return current, false, nil
	}
}

func derefSchema(schema *jsonschemav6.Schema) (*jsonschemav6.Schema, error) {
	seen := map[*jsonschemav6.Schema]struct{}{}
	for schema != nil && schema.Ref != nil {
		if _, ok := seen[schema]; ok {
			return nil, fmt.Errorf("cyclic schema references are not supported")
		}
		seen[schema] = struct{}{}
		schema = schema.Ref
	}
	return schema, nil
}

func schemaHasDefaultKeyword(raw interface{}) bool {
	switch typed := raw.(type) {
	case map[string]any:
		for key, value := range typed {
			if key == "default" {
				return true
			}
			if schemaHasDefaultKeyword(value) {
				return true
			}
		}
	case []any:
		for _, value := range typed {
			if schemaHasDefaultKeyword(value) {
				return true
			}
		}
	}
	return false
}

func deepCopyAny(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			out[key] = deepCopyAny(item)
		}
		return out
	case []any:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = deepCopyAny(item)
		}
		return out
	default:
		rv := reflect.ValueOf(value)
		switch rv.Kind() {
		case reflect.Map:
			if rv.Type().Key().Kind() != reflect.String {
				return value
			}
			out := make(map[string]interface{}, rv.Len())
			iter := rv.MapRange()
			for iter.Next() {
				out[iter.Key().String()] = deepCopyAny(iter.Value().Interface())
			}
			return out
		case reflect.Slice:
			out := make([]interface{}, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				out[i] = deepCopyAny(rv.Index(i).Interface())
			}
			return out
		default:
			return value
		}
	}
}

func toStringMap(value interface{}) (map[string]interface{}, bool, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false, false
	case map[string]any:
		return typed, true, false
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			keyStr, ok := key.(string)
			if !ok {
				return nil, false, false
			}
			out[keyStr] = item
		}
		return out, true, true
	default:
		rv := reflect.ValueOf(value)
		if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
			return nil, false, false
		}
		out := make(map[string]interface{}, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = iter.Value().Interface()
		}
		return out, true, true
	}
}

func toInterfaceSlice(value interface{}) ([]interface{}, bool, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false, false
	case []any:
		return typed, true, false
	default:
		rv := reflect.ValueOf(value)
		if rv.Kind() != reflect.Slice {
			return nil, false, false
		}
		out := make([]interface{}, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = rv.Index(i).Interface()
		}
		return out, true, true
	}
}

func mergeMissingKeys(dst map[string]interface{}, src map[string]interface{}) bool {
	changed := false
	for key, value := range src {
		if _, exists := dst[key]; exists {
			continue
		}
		dst[key] = value
		changed = true
	}
	return changed
}
