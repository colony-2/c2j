package compiler

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
)

func (d DefaultRecipeExecutor) ExecuteChildGroup(ctx workflow.Context, parent *template.ResolutionContext, metadata recipe.NodeMetadata, group recipe.ChildGroupData) error {
	renderCtx, err := parent.NewChildContext(template.ScopeOp, metadata, "child_group", nil)
	if err != nil {
		return fmt.Errorf("failed to create child_group resolution context: %w", err)
	}
	if err := renderCtx.ResolveVars(metadata.Vars); err != nil {
		return fmt.Errorf("failed to resolve child_group vars: %w", err)
	}

	input, err := renderChildGroupInput(renderCtx, group)
	if err != nil {
		return err
	}

	opType, err := childGroupInternalOp(input.Mode)
	if err != nil {
		return err
	}

	internalMetadata := metadata
	internalMetadata.Inputs = childGroupInputMap(input)
	internalMetadata.Vars = nil
	internalMetadata.Artifacts = nil
	return d.self().ExecuteOp(ctx, parent, internalMetadata, opType)
}

func renderChildGroupInput(resCtx *template.ResolutionContext, group recipe.ChildGroupData) (recipeops.ChildGroupStartInput, error) {
	mode, err := renderOptionalString(resCtx, group.Mode, nil)
	if err != nil {
		return recipeops.ChildGroupStartInput{}, fmt.Errorf("child_group.mode: %w", err)
	}
	if mode == "" {
		mode = "run_and_get_result"
	}

	aggregate, err := renderChildGroupAggregate(resCtx, group.Aggregate)
	if err != nil {
		return recipeops.ChildGroupStartInput{}, err
	}

	commonArtifacts, err := renderArtifactRefs(resCtx, group.Artifacts.Use, nil)
	if err != nil {
		return recipeops.ChildGroupStartInput{}, fmt.Errorf("child_group.artifacts.use: %w", err)
	}

	var children []recipeops.ChildGroupChildSpec
	for i, child := range group.Children {
		rendered, err := renderChildGroupChild(resCtx, child, nil, i, commonArtifacts)
		if err != nil {
			return recipeops.ChildGroupStartInput{}, fmt.Errorf("child_group.children[%d]: %w", i, err)
		}
		children = append(children, rendered)
	}

	if group.ChildrenFrom != nil {
		items, err := renderChildrenFrom(resCtx, group.ChildrenFrom)
		if err != nil {
			return recipeops.ChildGroupStartInput{}, err
		}
		if group.Child == nil {
			return recipeops.ChildGroupStartInput{}, fmt.Errorf("child_group.child is required when children_from is set")
		}
		for i, item := range items {
			locals := map[string]interface{}{
				"item":  item,
				"index": i,
			}
			rendered, err := renderChildGroupChild(resCtx, *group.Child, locals, len(children), commonArtifacts)
			if err != nil {
				return recipeops.ChildGroupStartInput{}, fmt.Errorf("child_group.child[%d]: %w", i, err)
			}
			children = append(children, rendered)
		}
	}

	seen := map[string]struct{}{}
	for i := range children {
		if strings.TrimSpace(children[i].Key) == "" {
			children[i].Key = strconv.Itoa(i)
		}
		if _, exists := seen[children[i].Key]; exists {
			return recipeops.ChildGroupStartInput{}, fmt.Errorf("child_group child key %q is duplicated", children[i].Key)
		}
		seen[children[i].Key] = struct{}{}
		children[i].Index = i
	}

	return recipeops.ChildGroupStartInput{
		Mode:      mode,
		Children:  children,
		Aggregate: aggregate,
	}, nil
}

func renderChildGroupAggregate(resCtx *template.ResolutionContext, aggregate recipe.ChildGroupAggregate) (recipeops.ChildGroupAggregateConfig, error) {
	shape, err := renderOptionalString(resCtx, aggregate.Shape, nil)
	if err != nil {
		return recipeops.ChildGroupAggregateConfig{}, fmt.Errorf("child_group.aggregate.shape: %w", err)
	}
	artifact, err := renderOptionalString(resCtx, aggregate.Artifact, nil)
	if err != nil {
		return recipeops.ChildGroupAggregateConfig{}, fmt.Errorf("child_group.aggregate.artifact: %w", err)
	}
	if shape == "" {
		shape = "none"
	}
	switch shape {
	case "none", "job_ids", "review_pack":
	default:
		return recipeops.ChildGroupAggregateConfig{}, fmt.Errorf("child_group.aggregate.shape %q is not supported", shape)
	}
	return recipeops.ChildGroupAggregateConfig{Shape: shape, Artifact: artifact}, nil
}

func renderChildGroupChild(resCtx *template.ResolutionContext, child recipe.ChildGroupChild, locals map[string]interface{}, index int, commonArtifacts []recipeartifacts.Ref) (recipeops.ChildGroupChildSpec, error) {
	key, err := renderOptionalString(resCtx, child.Key, locals)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("key: %w", err)
	}
	recipeName, err := renderOptionalString(resCtx, child.Recipe, locals)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("recipe: %w", err)
	}
	cellName, err := renderOptionalString(resCtx, child.CellName, locals)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("cell_name: %w", err)
	}
	gitRef, err := renderOptionalString(resCtx, child.GitRef, locals)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("git_ref: %w", err)
	}
	required, err := renderOptionalBool(resCtx, child.Required, locals, true)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("required: %w", err)
	}
	include, err := renderChildWhen(resCtx, child.When, locals)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("when: %w", err)
	}
	skipReason, err := renderOptionalString(resCtx, child.SkipReason, locals)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("skip_reason: %w", err)
	}
	inputs, err := renderChildInputs(resCtx, child.Inputs, locals)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("inputs: %w", err)
	}
	childArtifacts, err := renderArtifactRefs(resCtx, child.Artifacts, locals)
	if err != nil {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("artifacts: %w", err)
	}
	artifacts := append([]recipeartifacts.Ref(nil), commonArtifacts...)
	artifacts = append(artifacts, childArtifacts...)

	skipped := !include
	if !skipped && strings.TrimSpace(recipeName) == "" {
		return recipeops.ChildGroupChildSpec{}, fmt.Errorf("recipe is required")
	}

	return recipeops.ChildGroupChildSpec{
		Key:        key,
		Index:      index,
		Recipe:     recipeName,
		CellName:   cellName,
		Required:   required,
		Skipped:    skipped,
		SkipReason: skipReason,
		GitRef:     gitRef,
		Inputs:     inputs,
		Artifacts:  artifacts,
	}, nil
}

func renderChildrenFrom(resCtx *template.ResolutionContext, source interface{}) ([]interface{}, error) {
	resolved, err := resCtx.ResolveValueWithMode(source, template.ModeInterpolation)
	if err != nil {
		return nil, fmt.Errorf("child_group.children_from: %w", err)
	}
	return interfaceList(resolved)
}

func renderChildInputs(resCtx *template.ResolutionContext, input map[string]interface{}, locals map[string]interface{}) (map[string]interface{}, error) {
	if input == nil {
		return nil, nil
	}
	resolved, err := resCtx.ResolveValueWithLocals(input, locals)
	if err != nil {
		return nil, err
	}
	out, ok := resolved.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("resolved inputs must be an object")
	}
	return out, nil
}

func renderChildWhen(resCtx *template.ResolutionContext, value interface{}, locals map[string]interface{}) (bool, error) {
	if value == nil {
		return true, nil
	}
	localCtx := resCtx.WithLocals(locals)
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return true, nil
		}
		if strings.Contains(trimmed, "${{") || strings.Contains(trimmed, "{{") {
			return renderOptionalBool(resCtx, typed, locals, true)
		}
		return localCtx.EvaluateCEL(trimmed)
	default:
		return renderOptionalBool(resCtx, typed, locals, true)
	}
}

func renderOptionalString(resCtx *template.ResolutionContext, value interface{}, locals map[string]interface{}) (string, error) {
	if value == nil {
		return "", nil
	}
	resolved, err := resCtx.ResolveValueWithLocals(value, locals)
	if err != nil {
		return "", err
	}
	switch typed := resolved.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(typed), nil
	default:
		return strings.TrimSpace(fmt.Sprint(typed)), nil
	}
}

func renderOptionalBool(resCtx *template.ResolutionContext, value interface{}, locals map[string]interface{}, defaultValue bool) (bool, error) {
	if value == nil {
		return defaultValue, nil
	}
	resolved, err := resCtx.ResolveValueWithLocals(value, locals)
	if err != nil {
		return false, err
	}
	switch typed := resolved.(type) {
	case nil:
		return defaultValue, nil
	case bool:
		return typed, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return defaultValue, nil
		}
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return false, fmt.Errorf("expected bool, got %q", typed)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("expected bool, got %T", resolved)
	}
}

func renderArtifactRefs(resCtx *template.ResolutionContext, values []interface{}, locals map[string]interface{}) ([]recipeartifacts.Ref, error) {
	if len(values) == 0 {
		return nil, nil
	}
	resolved, err := resCtx.ResolveValueWithLocals(values, locals)
	if err != nil {
		return nil, err
	}
	list, err := interfaceList(resolved)
	if err != nil {
		return nil, err
	}
	out := make([]recipeartifacts.Ref, 0, len(list))
	for i, item := range list {
		ref, err := artifactRefFromRendered(item)
		if err != nil {
			return nil, fmt.Errorf("index %d: %w", i, err)
		}
		if ref.IsZero() {
			continue
		}
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("index %d: %w", i, err)
		}
		out = append(out, ref)
	}
	return out, nil
}

func artifactRefFromRendered(value interface{}) (recipeartifacts.Ref, error) {
	switch typed := value.(type) {
	case nil:
		return recipeartifacts.Ref{}, nil
	case recipeartifacts.Ref:
		return typed, nil
	case *recipeartifacts.Ref:
		if typed == nil {
			return recipeartifacts.Ref{}, nil
		}
		return *typed, nil
	case swf.ArtifactKey:
		return recipeartifacts.NewStoredRef(typed), nil
	case *swf.ArtifactKey:
		if typed == nil {
			return recipeartifacts.Ref{}, nil
		}
		return recipeartifacts.NewStoredRef(*typed), nil
	case map[string]interface{}:
		var ref recipeartifacts.Ref
		buf, err := json.Marshal(typed)
		if err != nil {
			return recipeartifacts.Ref{}, err
		}
		if err := json.Unmarshal(buf, &ref); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return ref, nil
	default:
		return recipeartifacts.Ref{}, fmt.Errorf("expected artifact ref, got %T", value)
	}
}

func interfaceList(value interface{}) ([]interface{}, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []interface{}:
		return typed, nil
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make([]interface{}, 0, len(keys))
		for _, key := range keys {
			out = append(out, map[string]interface{}{"key": key, "value": typed[key]})
		}
		return out, nil
	default:
		rv := reflect.ValueOf(value)
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return nil, fmt.Errorf("expected list, got %T", value)
		}
		out := make([]interface{}, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, rv.Index(i).Interface())
		}
		return out, nil
	}
}

func childGroupInternalOp(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case "", "run_and_get_result":
		return recipeops.ChildGroupRunAndGetResultOpType, nil
	case "start":
		return recipeops.ChildGroupStartOpType, nil
	default:
		return "", fmt.Errorf("unsupported child_group mode %q", mode)
	}
}

func childGroupInputMap(input recipeops.ChildGroupStartInput) map[string]interface{} {
	buf, err := json.Marshal(input)
	if err != nil {
		return map[string]interface{}{}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(buf, &out); err != nil {
		return map[string]interface{}{}
	}
	return out
}
