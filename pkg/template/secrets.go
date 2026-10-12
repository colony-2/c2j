package template

import (
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/secrets"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
)

// ResolveInputs is the only template entry point that can produce references.
// Callers must subsequently validate the destination schema.
func (rc *ResolutionContext) ResolveInputs(input map[string]any) (map[string]any, error) {
	copy := *rc
	copy.allowSecrets = true
	return copy.ResolveMap(input)
}

func (rc *ResolutionContext) secretScalar(text string) (any, bool, error) {
	var expr string
	if strings.HasPrefix(text, "${{") && strings.HasSuffix(text, "}}") {
		expr = strings.TrimSpace(text[3 : len(text)-2])
	} else if strings.HasPrefix(text, "{{") && strings.HasSuffix(text, "}}") {
		expr = strings.TrimSpace(text[2 : len(text)-2])
	}
	var value any
	if ref, ok := secrets.Expression(expr); ok {
		value = ref
	} else if strings.HasPrefix(expr, "inputs.") {
		name := strings.TrimPrefix(expr, "inputs.")
		if v, ok := rc.TemplateData.ContainerInputs[name]; ok && secrets.Contains(v) {
			value = v
		}
	}
	if value == nil {
		return nil, false, nil
	}
	if !rc.allowSecrets {
		return nil, true, fmt.Errorf("secret references are only allowed in secret-enabled inputs")
	}
	return value, true, nil
}

// Prevent functions, comparisons, dynamic indexing and aggregate conversions
// from observing references. Only direct whole-value forwarding bypasses CEL.
func (rc *ResolutionContext) rejectSecretCEL(e *exprpb.Expr) error {
	if e == nil {
		return nil
	}
	if v, ok := rc.staticInput(e); ok {
		if configuration.Contains(v) {
			return fmt.Errorf("config references cannot be used in expressions; echo the value in an op first")
		}
		if secrets.Contains(v) {
			return fmt.Errorf("secret inputs cannot be used in expressions")
		}
		return nil
	}
	switch x := e.ExprKind.(type) {
	case *exprpb.Expr_SelectExpr:
		return rc.rejectSecretCEL(x.SelectExpr.Operand)
	case *exprpb.Expr_CallExpr:
		if err := rc.rejectSecretCEL(x.CallExpr.Target); err != nil {
			return err
		}
		for _, a := range x.CallExpr.Args {
			if err := rc.rejectSecretCEL(a); err != nil {
				return err
			}
		}
	case *exprpb.Expr_ListExpr:
		for _, a := range x.ListExpr.Elements {
			if err := rc.rejectSecretCEL(a); err != nil {
				return err
			}
		}
	case *exprpb.Expr_StructExpr:
		for _, a := range x.StructExpr.Entries {
			if err := rc.rejectSecretCEL(a.GetMapKey()); err != nil {
				return err
			}
			if err := rc.rejectSecretCEL(a.Value); err != nil {
				return err
			}
		}
	case *exprpb.Expr_ComprehensionExpr:
		c := x.ComprehensionExpr
		for _, a := range []*exprpb.Expr{c.IterRange, c.AccuInit, c.LoopCondition, c.LoopStep, c.Result} {
			if err := rc.rejectSecretCEL(a); err != nil {
				return err
			}
		}
	}
	return nil
}
func (rc *ResolutionContext) staticInput(e *exprpb.Expr) (any, bool) {
	if e == nil {
		return nil, false
	}
	if i := e.GetIdentExpr(); i != nil && i.Name == "inputs" {
		return rc.TemplateData.ContainerInputs, true
	}
	if s := e.GetSelectExpr(); s != nil {
		if v, ok := rc.staticInput(s.Operand); ok {
			if _, ref, _ := configuration.Parse(v); ref {
				return v, true
			}
			if m, ok := v.(map[string]any); ok {
				return m[s.Field], true
			}
			return v, true
		}
	}
	return nil, false
}
