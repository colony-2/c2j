// Package jobsemantics interprets immutable job snapshots independently of
// recipe display versions, input schemas and operation task contracts.
package jobsemantics

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/ast"
)

const Current = 2

type versionKey struct{}

// WithVersion carries interpretation to operation execution without changing
// recorded operation commands or task contract versions.
func WithVersion(ctx context.Context, version int) context.Context {
	return context.WithValue(ctx, versionKey{}, version)
}

func Version(ctx context.Context) int {
	if version, ok := ctx.Value(versionKey{}).(int); ok {
		return version
	}
	return Current
}

func Validate(version int) error {
	switch version {
	case 0, 1, Current:
		return nil
	}
	return fmt.Errorf("unsupported job_semantics_version %d; supported versions are 1 and 2", version)
}

// CheckOperation runs on declared inputs in preflight and resolved inputs in
// the task builder. Legacy omissions keep their old meaning and are rejected,
// never silently assigned current defaults. Application inputs remain opaque.
func CheckOperation(version int, op string, inputs map[string]any, resolved bool) error {
	if err := Validate(version); err != nil {
		return err
	}
	if !strings.HasPrefix(op, "recipe.") && !strings.HasPrefix(op, "recipes.") {
		return nil
	}
	fail := func(reason string) error {
		return fmt.Errorf("%s (job_semantics_version %d): %s; select an exact job with wait.on: job, or explicitly prepare a new version 2 job", op, version, reason)
	}
	if goal, ok := inputs["goal_id"]; ok && goal != nil && goal != "" {
		return fail("goal selection is unsupported")
	}
	if target, ok := inputs["target"].(map[string]any); ok {
		if goal, ok := target["goal"]; ok && goal != nil && goal != "" {
			return fail("goal targets are unsupported")
		}
	}
	wait, _ := inputs["wait"].(map[string]any)
	on, _ := wait["on"].(string)
	if on != "" && on != "job" && (resolved || !templated(on)) {
		return fail("goal or unknown wait policy is unsupported")
	}
	// Historically ungrouped ordinary/result operations selected a goal when on
	// was omitted. Child groups and get_result already used exact jobs.
	if version < Current && on == "" {
		switch op {
		case "recipe.await_result", "recipe.await_result_soft", "recipe.run_and_get_result":
			if _, grouped := inputs["grouping"]; !grouped {
				return fail("legacy implicit goal wait cannot continue")
			}
		case "recipes.run", "recipes.run_and_wait":
			if recipes, ok := inputs["recipes"].([]any); ok {
				for _, item := range recipes {
					if child, ok := item.(map[string]any); ok {
						childWait, _ := child["wait"].(map[string]any)
						if childWait["on"] != "job" {
							if _, grouped := child["grouping"]; !grouped {
								return fail("legacy child has an implicit goal wait")
							}
						}
					}
				}
			} else if resolved {
				return fail("legacy child wait interpretation is unavailable")
			}
		}
	}
	for _, key := range []string{"recipes", "children"} {
		if children, ok := inputs[key].([]any); ok {
			for _, item := range children {
				if child, ok := item.(map[string]any); ok {
					effective := make(map[string]any, len(child)+1)
					for k, v := range child {
						effective[k] = v
					}
					if _, exists := child["wait"]; !exists && wait != nil {
						effective["wait"] = wait
					}
					childVersion, childOp := Current, "recipe.child"
					if strings.HasPrefix(op, "recipe.child_group.") {
						childVersion, childOp = version, "recipe.await_result"
					}
					if err := CheckOperation(childVersion, childOp, effective, resolved); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func templated(s string) bool { return strings.Contains(s, "{{") }

var templateExpression = regexp.MustCompile(`(?s)\{\{((?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^'"])*?)\}\}`)

// CheckTemplate checks interpolated expressions. Unlike raw CEL, text outside
// delimiters is not executable. Quoted closing delimiters remain inside the
// expression so a string constant cannot hide a later live context access.
func CheckTemplate(template string) error {
	for _, part := range templateExpression.FindAllStringSubmatch(template, -1) {
		if err := checkCELExpression(part[1], false); err != nil {
			return err
		}
	}
	return nil
}

var expressionEnv, expressionEnvError = cel.NewEnv(cel.OptionalTypes())

// CheckExpression checks one raw CEL expression, including any quoted template
// markers. Call CheckTemplate for interpolation instead. String constants and
// application fields are data, not context references. Runtime exports also omit
// retired fields, so dynamically selected paths cannot revive live goals.
func CheckExpression(expression string) error {
	return checkCELExpression(expression, true)
}

var invalidExpression = errors.New("invalid raw CEL expression")

// CheckChildCondition preserves the child's supported raw CEL and interpolated
// forms. Always check the complete raw expression first, including quoted markers.
func CheckChildCondition(expression string) error {
	err := CheckExpression(expression)
	if err != nil && !errors.Is(err, invalidExpression) {
		return err
	}
	if strings.Contains(expression, "{{") {
		return CheckTemplate(expression)
	}
	return err
}

func checkCELExpression(expression string, raw bool) error {
	if strings.TrimSpace(expression) == "" {
		return nil
	}
	if expressionEnvError != nil {
		return expressionEnvError
	}
	parsed, issues := expressionEnv.Parse(expression)
	if issues.Err() != nil {
		// Interpolation may also contain Go templates, whose syntax belongs
		// to that evaluator. Raw CEL conditions must parse before any effects.
		if raw {
			return fmt.Errorf("%w: %v", invalidExpression, issues.Err())
		}
		return nil
	}
	matches := ast.MatchDescendants(ast.NavigateAST(parsed.NativeRep()), func(e ast.NavigableExpr) bool {
		path := expressionPath(e)
		if len(path) == 3 && path[0] == "context" && path[1] == "workflow" && path[2] == "goal_id" {
			return true
		}
		if len(path) < 4 || (path[0] != "sequence" && path[0] != "states") {
			return false
		}
		// Step outputs are application data, even if named jobs or goal_ids.
		for _, field := range path[1:] {
			if field == "outputs" {
				return false
			}
		}
		n := len(path)
		return path[n-2] == "jobs" && path[n-1] == "goal_ids"
	})
	if len(matches) != 0 {
		return fmt.Errorf("retired live goal context; use exact job identity")
	}
	return nil
}

func expressionPath(e ast.Expr) []string {
	switch e.Kind() {
	case ast.IdentKind:
		return []string{e.AsIdent()}
	case ast.SelectKind:
		s := e.AsSelect()
		if prefix := expressionPath(s.Operand()); prefix != nil {
			return append(prefix, s.FieldName())
		}
	case ast.CallKind:
		c := e.AsCall()
		if c.FunctionName() == "_[_]" && len(c.Args()) == 2 {
			if prefix := expressionPath(c.Args()[0]); prefix != nil && c.Args()[1].Kind() == ast.LiteralKind {
				return append(prefix, fmt.Sprint(c.Args()[1].AsLiteral().Value()))
			}
		}
	}
	return nil
}
