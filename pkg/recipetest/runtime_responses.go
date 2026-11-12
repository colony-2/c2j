package recipetest

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

// Only bindings are exposed: invocation inputs may contain materialized secrets.
func responseProgram(expression string) (cel.Program, error) {
	env, err := cel.NewEnv(cel.Variable("children", cel.MapType(cel.StringType, cel.StringType)),
		cel.Function("json_encode", cel.Overload("fixture_json_encode", []*cel.Type{cel.DynType}, cel.StringType,
			cel.UnaryBinding(func(v ref.Val) ref.Val {
				native, err := responseJSON(v)
				if err != nil {
					return types.NewErr("json_encode: %v", err)
				}
				data, err := json.Marshal(native)
				if err != nil {
					return types.NewErr("json_encode: %v", err)
				}
				if len(data) > 65536 {
					return types.NewErr("json_encode exceeds 65536 bytes")
				}
				return types.String(data)
			}))))
	if err != nil {
		return nil, err
	}
	ast, issues := env.Compile(expression)
	if issues.Err() != nil {
		return nil, issues.Err()
	}
	return env.Program(ast, cel.CostLimit(100000))
}

// Reject CEL-only values rather than silently coercing bytes, times or map keys.
func responseJSON(v ref.Val) (any, error) {
	switch x := v.(type) {
	case types.Null:
		return nil, nil
	case types.String:
		return string(x), nil
	case types.Bool:
		return bool(x), nil
	case types.Int:
		return int64(x), nil
	case types.Uint:
		return uint64(x), nil
	case types.Double:
		if math.IsInf(float64(x), 0) || math.IsNaN(float64(x)) {
			return nil, fmt.Errorf("non-finite JSON number")
		}
		return float64(x), nil
	case traits.Mapper:
		out := map[string]any{}
		for it := x.Iterator(); it.HasNext() == types.True; {
			key := it.Next()
			name, ok := key.(types.String)
			if !ok {
				return nil, fmt.Errorf("JSON object keys must be strings")
			}
			value, err := responseJSON(x.Get(key))
			if err != nil {
				return nil, err
			}
			out[string(name)] = value
		}
		return out, nil
	case traits.Lister:
		out := []any{}
		for it := x.Iterator(); it.HasNext() == types.True; {
			value, err := responseJSON(it.Next())
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %s", v.Type())
	}
}

func computedResponse(expression string, children map[string]string) (map[string]any, error) {
	p, err := responseProgram(expression)
	if err != nil {
		return nil, err
	}
	value, _, err := p.Eval(map[string]any{"children": children})
	if err != nil {
		return nil, fmt.Errorf("child bindings: %w", err)
	}
	native, err := responseJSON(value)
	if err != nil {
		return nil, fmt.Errorf("outputs_expr must return a JSON object: %w", err)
	}
	data, err := json.Marshal(native)
	if err != nil {
		return nil, fmt.Errorf("outputs_expr must return JSON: %w", err)
	}
	if len(data) > 65536 {
		return nil, fmt.Errorf("outputs_expr exceeds 65536 bytes")
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return nil, fmt.Errorf("outputs_expr must return a JSON object")
	}
	return out, nil
}

type childReceipt struct {
	Identity string `json:"identity"`
	Binding  string `json:"binding"`
	Location string `json:"location"`
	JobID    string `json:"job_id"`
	TenantID string `json:"tenant_id"`
}

// Receipts live with the disposable native runtime, independently of the fixture
// object's memory. A pending receipt fails closed if submission was interrupted.
func (f *runtimeFixture) receipts() ([]childReceipt, error) {
	entries, err := os.ReadDir(filepath.Join(f.root, "child-receipts"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipts []childReceipt
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(f.root, "child-receipts", e.Name()))
		if err != nil {
			return nil, err
		}
		var r childReceipt
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		receipts = append(receipts, r)
	}
	return receipts, nil
}

func (f *runtimeFixture) saveReceipt(r childReceipt) error {
	dir := filepath.Join(f.root, "child-receipts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(r.Identity))))
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(f.root, "receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func (f *runtimeFixture) boundChild(identity, binding, location string, submit func() (childReceipt, error)) (childReceipt, bool, error) {
	f.submissionMu.Lock()
	defer f.submissionMu.Unlock()
	receipts, err := f.receipts()
	if err != nil {
		return childReceipt{}, false, err
	}
	for _, r := range receipts {
		if r.Identity == identity {
			if r.Binding != binding || r.JobID == "" {
				return r, false, fmt.Errorf("binding %q at %s has unresolved or changed submission receipt", binding, location)
			}
			return r, false, nil
		}
		if binding != "" && r.Binding == binding {
			return r, false, fmt.Errorf("binding %q reused at %s; originally submitted at %s (%s)", binding, location, r.Location, r.Identity)
		}
	}
	r := childReceipt{Identity: identity, Binding: binding, Location: location}
	if err := f.saveReceipt(r); err != nil {
		return r, false, err
	}
	submitted, err := submit()
	if err != nil {
		// A rejected broker call must not expose a binding. Keep the pending
		// receipt: an uncertain transport failure must never cause resubmission.
		return r, false, err
	}
	r.JobID, r.TenantID = submitted.JobID, submitted.TenantID
	if err := f.saveReceipt(r); err != nil {
		return r, false, err
	}
	return r, true, nil
}

func (f *runtimeFixture) responseBindings() (map[string]string, error) {
	f.submissionMu.Lock()
	defer f.submissionMu.Unlock()
	receipts, err := f.receipts()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range receipts {
		if r.Binding != "" && r.JobID != "" {
			out[r.Binding] = r.JobID
		}
	}
	return out, nil
}

func validateResponses(c Case) []Issue {
	var issues []Issue
	add := func(message string) {
		issues = append(issues, Issue{Code: "invalid_runtime_fixture", Message: message})
	}
	names := map[string]string{}
	for i, m := range c.Mocks.Ops {
		location := fmt.Sprintf("mock[%d] node=%q cell=%q", i, m.Match.NodePath, m.Match.Cell)
		if expr := m.Behavior.OutputsExpr; expr != nil {
			if c.Runtime == nil || m.Behavior.Mode != "return" {
				add(location + ": outputs_expr requires native runtime return mode")
			}
			if m.Behavior.Outputs != nil {
				add(location + ": outputs and outputs_expr conflict")
			}
			if strings.TrimSpace(*expr) == "" {
				add(location + ": outputs_expr must not be empty")
			} else if _, err := responseProgram(*expr); err != nil {
				add(location + ": outputs_expr: " + err.Error())
			}
		}
		if e := m.Behavior.Effects; e != nil {
			for _, child := range e.Children {
				if child.Bind == "" {
					continue
				}
				if c.Runtime == nil || m.Behavior.Mode != "return" {
					add(location + ": binding " + child.Bind + " requires native runtime return mode")
				}
				if strings.TrimSpace(child.Bind) != child.Bind {
					add(location + ": invalid binding " + child.Bind)
				}
				if previous, ok := names[child.Bind]; ok {
					add(fmt.Sprintf("binding %q declared at %s and %s", child.Bind, previous, location))
				}
				names[child.Bind] = location
			}
		}
	}
	return issues
}

// Distinguish omission from invalid explicit null/empty declarations.
func (m *MockBehavior) UnmarshalJSON(data []byte) error {
	type plain MockBehavior
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, supplied := fields["outputs_expr"]; supplied {
		if value.OutputsExpr == nil || strings.TrimSpace(*value.OutputsExpr) == "" {
			return fmt.Errorf("outputs_expr requires a nonempty expression")
		}
		if _, literal := fields["outputs"]; literal {
			return fmt.Errorf("outputs and outputs_expr conflict")
		}
	}
	*m = MockBehavior(value)
	return nil
}
