package recipetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
)

// FixtureReference selects only an immutable, registered portable repository.
// Local transport paths and dynamically registered cells are never exposed.
type FixtureReference struct {
	Cell  string `json:"cell"`
	Field string `json:"field"`
}

func (r *FixtureReference) UnmarshalJSON(data []byte) error {
	type plain FixtureReference
	var v plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	if strings.TrimSpace(v.Cell) == "" || v.Field != "repo" {
		return fmt.Errorf("fixture reference requires nonempty cell and field repo")
	}
	*r = FixtureReference(v)
	return nil
}

// Presence matters: an explicit null is not omission, including alternatives.
func validateFixtureFields(fields map[string]json.RawMessage) error {
	for _, name := range []string{"fixture_refs", "outputs_fixture_expr", "artifacts_expr", "worktree_expr"} {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		if string(raw) == "null" {
			return fmt.Errorf("%s must not be null", name)
		}
		if name == "outputs_fixture_expr" {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s requires a nonempty expression", name)
			}
			for _, other := range []string{"outputs", "outputs_expr"} {
				if _, ok := fields[other]; ok {
					return fmt.Errorf("outputs_fixture_expr conflicts with %s", other)
				}
			}
		} else {
			var values map[string]json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil {
				return fmt.Errorf("%s requires a map: %w", name, err)
			}
			for key, value := range values {
				if string(value) == "null" {
					return fmt.Errorf("%s[%q] must not be null", name, key)
				}
				if name != "fixture_refs" {
					var expression string
					if err := json.Unmarshal(value, &expression); err != nil || strings.TrimSpace(expression) == "" {
						return fmt.Errorf("%s[%q] requires a nonempty expression", name, key)
					}
				}
			}
		}
	}
	return nil
}

func (e *FixtureEffects) UnmarshalJSON(data []byte) error {
	type plain FixtureEffects
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if err := validateFixtureFields(fields); err != nil {
		return err
	}
	*e = FixtureEffects(v)
	return nil
}

func hasFixtureComputations(b MockBehavior) bool {
	return b.FixtureRefs != nil || b.OutputsFixtureExpr != nil || b.ArtifactsExpr != nil || (b.Effects != nil && b.Effects.WorktreeExpr != nil)
}

func seededProgram(expression string, refs map[string]FixtureReference) (cel.Program, error) {
	variables := []cel.EnvOption{}
	for _, alias := range sortedKeys(refs) {
		variables = append(variables, cel.Variable(alias, cel.StringType))
	}
	return fixtureProgram(expression, true, variables...)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var fixtureAlias = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateFixtureComputations(c Case) []Issue {
	var issues []Issue
	for i, mock := range c.Mocks.Ops {
		b := mock.Behavior
		if !hasFixtureComputations(b) {
			continue
		}
		location := fmt.Sprintf("mock[%d] cell=%q node=%q", i, mock.Match.Cell, mock.Match.NodePath)
		add := func(err error) {
			if err != nil {
				issues = append(issues, Issue{Code: "invalid_runtime_fixture", Message: location + ": " + err.Error()})
			}
		}
		if c.Runtime == nil || b.Mode != "return" {
			add(fmt.Errorf("seeded computations require native runtime return mode"))
		}
		if len(b.FixtureRefs) > 64 {
			add(fmt.Errorf("fixture_refs exceeds 64 references"))
		}
		for _, alias := range sortedKeys(b.FixtureRefs) {
			r := b.FixtureRefs[alias]
			reserved := map[string]bool{"children": true, "json_encode": true, "true": true, "false": true, "null": true, "in": true, "as": true, "break": true, "const": true, "continue": true, "else": true, "for": true, "function": true, "if": true, "import": true, "let": true, "loop": true, "namespace": true, "package": true, "return": true, "var": true, "void": true, "while": true}
			if !fixtureAlias.MatchString(alias) || reserved[alias] {
				add(fmt.Errorf("invalid fixture alias %q", alias))
			}
			if r.Field != "repo" || strings.TrimSpace(r.Cell) == "" {
				add(fmt.Errorf("fixture_refs[%q] requires cell and field repo", alias))
			}
			if c.Runtime != nil {
				_, seeded := c.Runtime.Cells[r.Cell]
				primary := c.Runtime.Cell
				if primary == "" {
					primary = "root"
				}
				if !seeded && r.Cell != primary {
					add(fmt.Errorf("fixture_refs[%q] unknown seeded cell %q", alias, r.Cell))
				}
			}
		}
		if b.OutputsFixtureExpr != nil && (b.Outputs != nil || b.OutputsExpr != nil) {
			add(fmt.Errorf("outputs_fixture_expr conflicts with outputs or outputs_expr"))
		}
		check := func(field, expression string) {
			if strings.TrimSpace(expression) == "" {
				add(fmt.Errorf("%s requires nonempty expression", field))
				return
			}
			if _, err := seededProgram(expression, b.FixtureRefs); err != nil {
				add(fmt.Errorf("%s: %w", field, err))
			}
		}
		if b.OutputsFixtureExpr != nil {
			check("outputs_fixture_expr", *b.OutputsFixtureExpr)
		}
		count := len(b.ArtifactsExpr)
		for _, name := range sortedKeys(b.ArtifactsExpr) {
			check("artifacts_expr["+name+"]", b.ArtifactsExpr[name])
		}
		if b.Effects != nil {
			count += len(b.Effects.WorktreeExpr)
			for _, name := range sortedKeys(b.Effects.WorktreeExpr) {
				check("worktree_expr["+name+"]", b.Effects.WorktreeExpr[name])
			}
		}
		if count > 64 {
			add(fmt.Errorf("computed files exceeds 64"))
		}
		add(fixtureDestinations(b))
	}
	return issues
}

// Validate selected destinations together, including cleaned aliases and file/
// directory collisions. Literal-only fixtures retain their existing behavior.
func fixtureDestinations(b MockBehavior) error {
	for name := range b.ArtifactsExpr {
		if name == "" || strings.ContainsRune(name, 0) {
			return fmt.Errorf("invalid computed artifact destination")
		}
		if _, ok := b.Artifacts[name]; ok {
			return fmt.Errorf("artifacts_expr destination %q conflicts with artifacts", name)
		}
		if b.Effects != nil {
			if _, ok := b.Effects.ArtifactFiles[name]; ok {
				return fmt.Errorf("artifacts_expr destination %q conflicts with artifact_files", name)
			}
		}
	}
	if b.Effects == nil {
		return nil
	}
	destinations := map[string]string{}
	for _, group := range []map[string]string{b.Effects.Worktree, b.Effects.WorktreeExpr} {
		for _, name := range sortedKeys(group) {
			if _, err := fixturePath("", name); err != nil {
				return err
			}
			clean := filepath.Clean(name)
			if clean == "." {
				return fmt.Errorf("worktree destination must name a file")
			}
			for prior, original := range destinations {
				if prior == clean || strings.HasPrefix(prior, clean+string(filepath.Separator)) || strings.HasPrefix(clean, prior+string(filepath.Separator)) {
					return fmt.Errorf("worktree destinations %q and %q conflict", original, name)
				}
			}
			destinations[clean] = name
		}
	}
	return nil
}

type resolvedFixture struct {
	outputs   map[string]any
	artifacts map[string]string
	worktree  map[string]string
}

// All new computations finish into detached buffers before any mock effects.
func preflightFixture(b MockBehavior, repos map[string]string, root string) (resolvedFixture, error) {
	result := resolvedFixture{outputs: cloneStringMap(b.Outputs), artifacts: map[string]string{}, worktree: map[string]string{}}
	if !hasFixtureComputations(b) {
		return result, nil
	}
	fail := func(field string, err error) (resolvedFixture, error) {
		return resolvedFixture{}, fmt.Errorf("%s: %w", field, err)
	}
	if err := fixtureDestinations(b); err != nil {
		return fail("destinations", err)
	}
	bindings := map[string]any{}
	for _, alias := range sortedKeys(b.FixtureRefs) {
		r := b.FixtureRefs[alias]
		value, ok := repos[r.Cell]
		if !ok || value == "" || r.Field != "repo" {
			return fail("fixture_refs["+alias+"]", fmt.Errorf("unresolved seeded repository"))
		}
		bindings[alias] = value
	}
	var cost uint64
	eval := func(expression string) (any, error) {
		p, err := seededProgram(expression, b.FixtureRefs)
		if err != nil {
			return nil, err
		}
		value, details, err := p.Eval(bindings)
		if err != nil {
			return nil, err
		}
		if details != nil && details.ActualCost() != nil {
			cost += *details.ActualCost()
			if cost > 100000 {
				return nil, fmt.Errorf("aggregate CEL cost exceeds 100000")
			}
		}
		return responseJSON(value)
	}
	if b.OutputsFixtureExpr != nil {
		value, err := eval(*b.OutputsFixtureExpr)
		if err != nil {
			return fail("outputs_fixture_expr", err)
		}
		data, err := json.Marshal(value)
		if err != nil {
			return fail("outputs_fixture_expr", err)
		}
		if len(data) > 65536 {
			return fail("outputs_fixture_expr", fmt.Errorf("exceeds 65536 bytes"))
		}
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil || out == nil {
			return fail("outputs_fixture_expr", fmt.Errorf("must return a JSON object"))
		}
		if b.Effects != nil {
			for name := range b.Effects.Objects {
				if _, ok := out[name]; ok {
					return fail("outputs_fixture_expr", fmt.Errorf("collides with object effect key %q", name))
				}
			}
		}
		result.outputs = out
	}
	total := 0
	files := func(field string, expressions, output map[string]string) error {
		for _, name := range sortedKeys(expressions) {
			value, err := eval(expressions[name])
			if err != nil {
				return fmt.Errorf("%s[%q]: %w", field, name, err)
			}
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("%s[%q] must return text", field, name)
			}
			if len(text) > 65536 {
				return fmt.Errorf("%s[%q] exceeds 65536 bytes", field, name)
			}
			total += len(text)
			if total > 262144 {
				return fmt.Errorf("aggregate computed text exceeds 262144 bytes")
			}
			output[name] = text
		}
		return nil
	}
	if err := files("artifacts_expr", b.ArtifactsExpr, result.artifacts); err != nil {
		return resolvedFixture{}, err
	}
	if b.Effects != nil {
		if err := files("worktree_expr", b.Effects.WorktreeExpr, result.worktree); err != nil {
			return resolvedFixture{}, err
		}
		for _, group := range []map[string]string{b.Effects.Worktree, result.worktree} {
			for _, name := range sortedKeys(group) {
				if _, err := fixtureWriteDestination(root, name); err != nil {
					return fail("worktree["+name+"]", err)
				}
			}
		}
	}
	return result, nil
}

// Shared by read-only preflight and ordinary writes. No directories are created.
func fixtureWriteDestination(root, name string) (string, error) {
	root = filepath.Clean(root)
	dest, err := fixturePath(root, name)
	if err != nil {
		return "", err
	}
	for path := dest; path != root; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("cannot inspect fixture destination %q", name)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("fixture path contains symlink: %s", name)
		}
		if (path == dest && info.IsDir()) || (path != dest && !info.IsDir()) {
			return "", fmt.Errorf("fixture destination has file/directory conflict: %s", name)
		}
	}
	return dest, nil
}

// Sticky failures are applied after expected-error handling and recipe catches.
func (f *runtimeFixture) fixtureError(index int, cell, node string, err error) error {
	failure := fmt.Errorf("mock[%d] cell=%q node=%q: %w", index, cell, node, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fixtureFailure == "" {
		f.fixtureFailure = failure.Error()
	}
	return failure
}
