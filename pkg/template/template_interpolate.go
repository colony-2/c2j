package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	texttemplate "text/template"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/colony-2/swf-go/pkg/swf"
)

var simpleGoTemplateExprPattern = regexp.MustCompile(`^\s*\{\{\s*([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+)\s*\}\}\s*$`)

// interpolateString performs string interpolation with embedded CEL expressions
func (rc *ResolutionContext) interpolateString(template string, mode RenderMode) (interface{}, error) {
	// For pure CEL mode (when conditions), don't do interpolation
	if mode == ModePureCEL {
		// This should be a pure CEL expression, no {{ }} markers
		return rc.EvaluateCEL(template)
	}

	// Parse the template into segments
	segments, err := parseTemplate(template)
	if err != nil {
		return nil, fmt.Errorf("template parse error: %w", err)
	}

	// Check if entire string is a single expression (for backward compatibility)
	if len(segments) == 1 {
		if expr, ok := segments[0].(ExpressionSegment); ok {
			// Single expression - evaluate and return raw result (could be any type)
			return rc.evaluateCELExpression(expr.Expression)
		}
		// Single text segment - return as-is
		if text, ok := segments[0].(TextSegment); ok {
			if strings.Contains(text.Text, "{{") && strings.Contains(text.Text, "}}") {
				if value, ok, err := rc.resolveSimpleGoTemplateScalar(text.Text); err != nil {
					return nil, err
				} else if ok {
					return value, nil
				}
				return rc.renderGoTemplate(text.Text)
			}
			return text.Text, nil
		}
	}

	// Multiple segments or mixed content - interpolate as string
	var result strings.Builder
	for _, segment := range segments {
		switch s := segment.(type) {
		case TextSegment:
			rendered, err := rc.renderGoTemplateSegment(s.Text)
			if err != nil {
				return nil, fmt.Errorf("Go template error at position %d: %w", s.Pos, err)
			}
			result.WriteString(rendered)

		case ExpressionSegment:
			value, err := rc.evaluateCELExpression(s.Expression)
			if err != nil {
				// Include position in error for better debugging
				return nil, fmt.Errorf("expression error at position %d: %w", s.Pos, err)
			}
			if isArtifactInterpolationValue(value) {
				return nil, fmt.Errorf("artifact values cannot be interpolated into strings")
			}
			result.WriteString(convertToString(value))
		}
	}

	return result.String(), nil
}

func (rc *ResolutionContext) resolveSimpleGoTemplateScalar(input string) (interface{}, bool, error) {
	matches := simpleGoTemplateExprPattern.FindStringSubmatch(input)
	if len(matches) != 2 {
		return nil, false, nil
	}
	path := strings.Split(matches[1], ".")
	if len(path) < 2 {
		return nil, false, nil
	}

	root := path[0]
	var current interface{}
	switch root {
	case "inputs":
		current = rc.TemplateData.ContainerInputs
	case "sequence":
		current = flattenTemplateValue(rc.TemplateData.Sequence)
	case "states":
		current = flattenTemplateValue(rc.TemplateData.States)
	case "vars":
		current = flattenTemplateValue(rc.TemplateData.Vars)
	case "scope":
		current = flattenTemplateValue(rc.TemplateData.Scope)
	case "context":
		current = rc.goTemplateContextMap()
	default:
		return nil, false, nil
	}

	for _, key := range path[1:] {
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		next, exists := asMap[key]
		if !exists {
			return nil, false, fmt.Errorf("map has no entry for key %q", key)
		}
		current = next
	}

	switch value := current.(type) {
	case nil:
		return "", true, nil
	case string:
		return value, true, nil
	case bool:
		return value, true, nil
	case int:
		return value, true, nil
	case int8:
		return value, true, nil
	case int16:
		return value, true, nil
	case int32:
		return value, true, nil
	case int64:
		return value, true, nil
	case uint:
		return value, true, nil
	case uint8:
		return value, true, nil
	case uint16:
		return value, true, nil
	case uint32:
		return value, true, nil
	case uint64:
		return value, true, nil
	case float32:
		return value, true, nil
	case float64:
		return value, true, nil
	default:
		// Preserve backward compatibility for non-scalar values by using normal Go template rendering.
		return nil, false, nil
	}
}

func (rc *ResolutionContext) renderGoTemplateSegment(input string) (string, error) {
	if !strings.Contains(input, "{{") || !strings.Contains(input, "}}") {
		return input, nil
	}
	return rc.renderGoTemplate(input)
}

func (rc *ResolutionContext) renderGoTemplate(input string) (string, error) {
	tmpl, err := texttemplate.New("string").
		Option("missingkey=error").
		Funcs(rc.goTemplateFuncMap()).
		Parse(input)
	if err != nil {
		return "", err
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, nil); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (rc *ResolutionContext) goTemplateFuncMap() texttemplate.FuncMap {
	funcMap := texttemplate.FuncMap{
		"inputs": func() map[string]interface{} {
			return rc.TemplateData.ContainerInputs
		},
		"sequence": func() interface{} {
			return flattenTemplateValue(rc.TemplateData.Sequence)
		},
		"states": func() interface{} {
			return flattenTemplateValue(rc.TemplateData.States)
		},
		"vars": func() map[string]interface{} {
			return flattenTemplateValue(rc.TemplateData.Vars)
		},
		"scope": func() map[string]interface{} {
			return flattenTemplateValue(rc.TemplateData.Scope)
		},
		"context": func() map[string]interface{} {
			return rc.goTemplateContextMap()
		},
	}
	for name, fn := range rc.stateLookupTemplateFuncs() {
		funcMap[name] = fn
	}

	type goTemplateFuncProvider interface {
		TemplateFuncsWithContext(funcregistry.ContextProvider) map[string]interface{}
	}
	if provider, ok := rc.Options.CELOptionsProvider.(goTemplateFuncProvider); ok {
		ctxProvider := func() contextual.TaskExecutionContext {
			return rc.TemplateData.Context
		}
		for name, fn := range provider.TemplateFuncsWithContext(ctxProvider) {
			if _, exists := funcMap[name]; exists {
				// Keep root context helpers authoritative.
				continue
			}
			funcMap[name] = fn
		}
	}

	return funcMap
}

func (rc *ResolutionContext) goTemplateContextMap() map[string]interface{} {
	ctx := rc.TemplateData.Context

	return map[string]interface{}{
		"environment": map[string]interface{}{
			"worktree_path": ctx.Environment.WorktreePath,
			"workdir":       ctx.Environment.WorkdirPath,
			"inbox":         ctx.Environment.ArtifactInbox,
			"outbox":        ctx.Environment.ArtifactOutbox,
			"host": map[string]interface{}{
				"worktree_path": ctx.Environment.Host.WorktreePath,
				"workdir":       ctx.Environment.Host.Workdir,
				"inbox":         ctx.Environment.Host.Inbox,
				"outbox":        ctx.Environment.Host.Outbox,
			},
			"op": map[string]interface{}{
				"worktree_path": ctx.Environment.Op.WorktreePath,
				"workdir":       ctx.Environment.Op.Workdir,
				"inbox":         ctx.Environment.Op.Inbox,
				"outbox":        ctx.Environment.Op.Outbox,
			},
		},
		"workflow": map[string]interface{}{
			"cell_id":    ctx.Workflow.CellID,
			"cell":       ctx.Workflow.CellName,
			"cell_path":  ctx.Workflow.CellPath,
			"job_id":     ctx.Workflow.JobID,
			"project_id": ctx.Workflow.ProjectId,
		},
		"git": map[string]interface{}{
			"repo":          ctx.GitTask.BaseRepo,
			"ref":           ctx.GitTask.BaseRef,
			"resolved_hash": ctx.GitTask.ResolvedBaseHash,
			"author":        ctx.GitTask.GitAuthor,
			"parent_ref":    ctx.GitTask.ParentRef,
			"hash":          ctx.GitTask.PersistHash,
			"parent_hash":   ctx.GitTask.ParentHash,
		},
		"invocation": map[string]interface{}{
			"hash":     ctx.Invocation.Hash,
			"path":     ctx.Invocation.NodePath,
			"sequence": ctx.Invocation.InvokeSeq,
		},
	}
}

func flattenTemplateValue(value interface{}) map[string]interface{} {
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]interface{}{}
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]interface{}{}
	}
	if out == nil {
		return map[string]interface{}{}
	}
	return out
}

func isArtifactInterpolationValue(value interface{}) bool {
	switch v := value.(type) {
	case recipeartifacts.Ref:
		return !v.IsZero()
	case *recipeartifacts.Ref:
		return v != nil && !v.IsZero()
	case swf.ArtifactKey:
		return true
	case *swf.ArtifactKey:
		return v != nil
	case map[string]recipeartifacts.Ref:
		return true
	case map[string]*recipeartifacts.Ref:
		return true
	case map[string]swf.ArtifactKey:
		return true
	case map[string]*swf.ArtifactKey:
		return true
	case map[string]interface{}:
		for _, entry := range v {
			if isArtifactInterpolationValue(entry) {
				return true
			}
		}
		return false
	case []recipeartifacts.Ref:
		return true
	case []swf.ArtifactKey:
		return true
	case []interface{}:
		for _, entry := range v {
			if isArtifactInterpolationValue(entry) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// ResolveTemplateWithMode handles expression evaluation with a specific mode
func (rc *ResolutionContext) ResolveTemplateWithMode(expr string, mode RenderMode) (interface{}, error) {
	// For pure CEL mode, this is a when condition - no {{ }} expected
	if mode == ModePureCEL {
		return rc.EvaluateCEL(expr)
	}

	// For interpolation mode, check if it looks like a template
	trimmed := strings.TrimSpace(expr)

	// If it doesn't have template markers, return as-is
	if !(strings.Contains(trimmed, "${{") || strings.Contains(trimmed, "{{")) {
		return expr, nil
	}

	// Use interpolation
	return rc.interpolateString(expr, mode)
}

// ResolveValueWithMode recursively resolves templates in a value with a specific mode
func (rc *ResolutionContext) ResolveValueWithMode(value interface{}, mode RenderMode) (interface{}, error) {
	switch v := value.(type) {
	case string:
		// Resolve string templates with the given mode
		return rc.ResolveTemplateWithMode(v, mode)
	case map[string]interface{}:
		// Recursively resolve map values
		result := make(map[string]interface{})
		for key, val := range v {
			resolved, err := rc.ResolveValueWithMode(val, mode)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve key %s: %w", key, err)
			}
			result[key] = resolved
		}
		return result, nil
	case []interface{}:
		// Recursively resolve slice values
		result := make([]interface{}, len(v))
		for i, val := range v {
			resolved, err := rc.ResolveValueWithMode(val, mode)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve index %d: %w", i, err)
			}
			result[i] = resolved
		}
		return result, nil
	default:
		// For other types (numbers, bools, etc.), return as-is
		return value, nil
	}
}
