package template

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"

	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/task"
	jsonpatch "github.com/evanphx/json-patch/v5"
)

// ApplyRestartPayload is the persisted-payload boundary. Public callers must use
// ApplyContextPatch; object payloads here are historical merge patches only.
func (rc *ResolutionContext) ApplyRestartPayload(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '[' {
		p, err := jobrestart.ParsePatch(raw)
		if err != nil {
			return err
		}
		return rc.ApplyContextPatch(*p)
	}
	if raw[0] != '{' {
		return fmt.Errorf("invalid stored restart patch shape")
	}
	var p legacyContextPatch
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return fmt.Errorf("stored legacy patch: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("stored legacy patch has trailing data")
	}
	return rc.applyLegacyContextPatch(p)
}

func decodePatchDocument(raw []byte, dst any) error {
	return jsonexact.Unmarshal(raw, dst)
}

// ApplyContextPatch stages every operation and ancestor update before publishing
// changes. No live map or workspace base is touched on failure.
func (rc *ResolutionContext) ApplyContextPatch(p task.ContextPatch) error {
	if rc == nil {
		return fmt.Errorf("nil resolution context")
	}
	if err := jobrestart.ValidatePatch(&p); err != nil {
		return err
	}
	if len(p) == 0 {
		return nil
	}
	// Nil output maps still represent editable empty objects.
	sequence, states := cloneRestartSteps(rc.TemplateData.Sequence), cloneRestartSteps(rc.TemplateData.States)
	for _, steps := range []map[string]StepOutput{sequence, states} {
		for id, step := range steps {
			if step.Outputs == nil {
				step.Outputs = map[string]any{}
				steps[id] = step
			}
		}
	}
	projection := map[string]any{"context": patchJobContext(rc), "sequence": sequence, "states": states}
	raw, err := json.Marshal(projection)
	if err != nil {
		return err
	}
	var original map[string]any
	if err := decodePatchDocument(raw, &original); err != nil {
		return err
	}
	var current map[string]any
	if err := decodePatchDocument(raw, &current); err != nil {
		return err
	}
	options := jsonpatch.NewApplyOptions()
	options.SupportNegativeIndices = false
	options.AllowMissingPathOnRemove = false
	options.EnsurePathExistsOnAdd = false
	for i, op := range p {
		fail := func(reason string) error { return fmt.Errorf("operation %d: %s", i, reason) }
		tokens, _ := jobrestart.PointerTokens(op.Path)
		if !visiblePatchStep(original, tokens) {
			return fail("local step is not visible")
		}
		add := op.Op == "add" || op.Op == "copy" || op.Op == "move"
		if _, err := patchLookup(current, tokens, add); err != nil {
			return fail(err.Error())
		}
		if op.Op == "move" || op.Op == "copy" {
			source, _ := jobrestart.PointerTokens(op.From)
			if !visiblePatchStep(original, source) {
				return fail("source local step is not visible")
			}
			if _, err := patchLookup(current, source, false); err != nil {
				return fail("source target does not exist or has invalid index")
			}
		}
		if op.Op == "test" {
			actual, err := patchLookup(current, tokens, false)
			if err != nil {
				return fail("test target does not exist")
			}
			var expected any
			if err := decodePatchDocument(op.Value, &expected); err != nil {
				return fail("invalid test value")
			}
			if !patchEqual(actual, expected) {
				return fail("test failed")
			}
			continue
		}
		one, err := json.Marshal(task.ContextPatch{op})
		if err != nil {
			return fail("invalid operation")
		}
		engine, err := jsonpatch.DecodePatch(one)
		if err != nil {
			return fail("invalid operation")
		}
		raw, err = engine.ApplyWithOptions(raw, options)
		if err != nil {
			return fail("target does not exist or operation cannot be applied")
		}
		if err := decodePatchDocument(raw, &current); err != nil {
			return fail("invalid resulting document")
		}
		if err := validatePatchProjection(current); err != nil {
			return fail(err.Error())
		}
	}
	// Stage ancestors against their own baselines: apply the calculated delta,
	// never re-execute an array operation on a different ancestor array.
	type staged struct {
		rc         *ResolutionContext
		job        contextual.JobContext
		gitChanged bool
	}
	var jobs []staged
	before := original["context"].(map[string]any)
	after := current["context"].(map[string]any)
	for cur := rc; cur != nil; cur = cur.Parent {
		if patchEqual(before, after) {
			break
		}
		data, err := json.Marshal(patchJobContext(cur))
		if err != nil {
			return err
		}
		var base map[string]any
		if err := decodePatchDocument(data, &base); err != nil {
			return err
		}
		for key := range unionPatchKeys(before, after) {
			if key == "git" && cur.commitContext != rc.commitContext {
				continue
			}
			old, had := before[key]
			next, has := after[key]
			if had == has && patchEqual(old, next) {
				continue
			}
			if !has {
				delete(base, key)
			} else {
				base[key] = patchDelta(base[key], old, next)
			}
		}
		data, err = json.Marshal(base)
		if err != nil {
			return err
		}
		if err := jobrestart.ValidatePatchValue(data, reflect.TypeFor[contextual.JobContext]()); err != nil {
			return err
		}
		var job contextual.JobContext
		if err := decodePatchDocument(data, &job); err != nil {
			return err
		}
		jobs = append(jobs, staged{cur, job, !patchEqual(before["git"], after["git"]) && cur.commitContext == rc.commitContext})
	}
	// Stage only outputs: artifact/checkpoint/job metadata remains untouched.
	outputs := map[string]map[string]map[string]any{}
	for _, name := range []string{"sequence", "states"} {
		outputs[name] = map[string]map[string]any{}
		if steps, ok := current[name].(map[string]any); ok {
			for id, value := range steps {
				out := value.(map[string]any)["outputs"].(map[string]any)
				outputs[name][id] = out
			}
		}
	}
	for _, job := range jobs {
		exec := &job.rc.TemplateData.Context
		for key := range unionPatchKeys(before, after) {
			if patchEqual(before[key], after[key]) {
				continue
			}
			switch key {
			case "environment":
				exec.Environment = job.job.Environment
			case "workflow":
				exec.Workflow = job.job.Workflow
			case "artifacts":
				exec.Artifacts = job.job.Artifacts
			case "recipe_source":
				exec.RecipeSource = job.job.RecipeSource
			case "git":
				if job.gitChanged {
					exec.GitTask.BaseRepo = job.job.GitBase.BaseRepo
					exec.GitTask.BaseRef = job.job.GitBase.BaseRef
					exec.GitTask.ResolvedBaseHash = job.job.GitBase.ResolvedBaseHash
					exec.GitTask.GitAuthor = job.job.GitBase.GitAuthor
				}
			}
		}
		if job.rc.workspaceBase != nil && job.gitChanged {
			*job.rc.workspaceBase = job.job.GitBase
		}
	}
	for _, pair := range []struct {
		name  string
		steps map[string]StepOutput
	}{{"sequence", rc.TemplateData.Sequence}, {"states", rc.TemplateData.States}} {
		for id, out := range outputs[pair.name] {
			// Preserve original Go values and aliases for untouched outputs.
			old := original[pair.name].(map[string]any)[id].(map[string]any)["outputs"]
			if patchEqual(old, out) {
				continue
			}
			step := pair.steps[id]
			step.Outputs = jsonexact.Native(out).(map[string]any)
			pair.steps[id] = step
		}
	}
	return nil
}

func visiblePatchStep(original map[string]any, tokens []string) bool {
	if tokens[0] == "context" {
		return true
	}
	steps, _ := original[tokens[0]].(map[string]any)
	_, exists := steps[tokens[1]]
	return exists
}

func validatePatchProjection(doc map[string]any) error {
	raw, err := json.Marshal(doc["context"])
	if err != nil {
		return err
	}
	if err := jobrestart.ValidatePatchValue(raw, reflect.TypeFor[contextual.JobContext]()); err != nil {
		return err
	}
	for _, name := range []string{"sequence", "states"} {
		steps, _ := doc[name].(map[string]any)
		for _, value := range steps {
			step, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("step must remain an object")
			}
			if _, ok := step["outputs"].(map[string]any); !ok {
				return fmt.Errorf("outputs must remain an object")
			}
		}
	}
	return nil
}

// patchLookup checks RFC array grammar as well as existence; the upstream
// engine accepts leading zeros, so validation cannot be delegated to it.
func patchLookup(doc any, tokens []string, add bool) (any, error) {
	for i, token := range tokens {
		last := i == len(tokens)-1
		switch v := doc.(type) {
		case map[string]any:
			next, ok := v[token]
			if !ok && !(last && add) {
				return nil, fmt.Errorf("target or parent does not exist")
			}
			doc = next
		case []any:
			if last && add && token == "-" {
				return nil, nil
			}
			if token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, fmt.Errorf("invalid array index")
			}
			for _, c := range token {
				if c < '0' || c > '9' {
					return nil, fmt.Errorf("invalid array index")
				}
			}
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index > len(v) || (index == len(v) && !(last && add)) {
				return nil, fmt.Errorf("array index out of bounds")
			}
			if index == len(v) {
				return nil, nil
			}
			doc = v[index]
		default:
			return nil, fmt.Errorf("target parent is not a container")
		}
	}
	return doc, nil
}

func patchEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		ax, ok := new(big.Rat).SetString(string(x))
		if !ok {
			return false
		}
		by, ok := new(big.Rat).SetString(string(y))
		return ok && ax.Cmp(by) == 0
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			other, ok := y[k]
			if !ok || !patchEqual(v, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !patchEqual(v, y[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}
func unionPatchKeys(a, b map[string]any) map[string]bool {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	return keys
}
func patchDelta(base, before, after any) any {
	a, aok := before.(map[string]any)
	b, bok := after.(map[string]any)
	if aok && bok {
		out, _ := base.(map[string]any)
		if out == nil {
			out = map[string]any{}
		}
		for key := range unionPatchKeys(a, b) {
			old, had := a[key]
			next, has := b[key]
			if had == has && patchEqual(old, next) {
				continue
			}
			if !has {
				delete(out, key)
			} else {
				out[key] = patchDelta(out[key], old, next)
			}
		}
		return out
	}
	return after
}

// TaskExecutionContext refreshes cached Git fields; use a detached receiver so
// even those cache writes cannot escape a failing patch.
func patchJobContext(rc *ResolutionContext) contextual.JobContext {
	view := *rc
	return view.TaskExecutionContext().JobContext()
}
