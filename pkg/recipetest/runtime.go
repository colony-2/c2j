package recipetest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/contextual"
	inputop "github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type runtimeFixture struct {
	mu        sync.Mutex
	opts      HarnessOptions
	c         Case
	root      string
	cells     map[string]string
	selected  map[string]int
	used      map[int]struct{}
	responses map[int]bool
	report    RuntimeReport
	ctl       *workerworkflow.SWFWorkflowControl
}

func runRuntimeCase(parent context.Context, opts HarnessOptions, tenant string, req caseInput, prepared preparedCase) (result CaseRunResult) {
	start := time.Now()
	result = CaseRunResult{CaseId: req.Case.ID, CaseHash: prepared.Validation.CaseHash, Status: "failed"}
	defer func() { result.DurationMs = time.Since(start).Milliseconds() }()
	ctx, cancel := context.WithTimeout(parent, parseTimeout(req.Execution.Timeout, 60*time.Second))
	defer cancel()
	root, err := os.MkdirTemp("", "c2j-runtime-case-*")
	if err != nil {
		result.FailureReason = err.Error()
		return
	}
	defer os.RemoveAll(root)
	f := &runtimeFixture{opts: opts, c: req.Case, root: root, cells: map[string]string{}, selected: map[string]int{}, used: map[int]struct{}{}, responses: map[int]bool{}}
	defer func() { f.mu.Lock(); defer f.mu.Unlock(); result.Runtime = &f.report }()
	cell := req.Case.Runtime.Cell
	if cell == "" {
		cell = "root"
	}
	if err = f.seedCells(ctx, cell); err != nil {
		result.FailureReason = err.Error()
		return
	}
	rt := toyruntime.New()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(tenant).BuildEngine()
	if err != nil {
		result.FailureReason = err.Error()
		return
	}
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}
	f.ctl = &workerworkflow.SWFWorkflowControl{Engine: engine}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(f.ctl).Build()
	registry, err := workerops.NewActivityRegistry()
	if err != nil {
		result.FailureReason = err.Error()
		return
	}
	for name, registration := range registry.GetAll() {
		reg := registration
		invoke := reg.Step.Invoke
		reg.Step.Invoke = func(deps coreops.OpDependencies, ctx context.Context, in map[string]any) (map[string]any, error) {
			return f.invoke(deps, ctx, reg.Metadata.Type, invoke, in)
		}
		registry.UpdateRegistration(name, reg)
	}
	workers, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{RootSourceResolver: compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{})})
	if err == nil {
		err = engine.RegisterWorkers(workers)
	}
	if err != nil {
		result.FailureReason = err.Error()
		return
	}
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: tenant, RecipeName: prepared.Recipe.GetMetadata().ID, Inputs: req.Case.Inputs, GitRef: "main", JobContext: f.jobContext(cell)}, engine, *prepared.Recipe)
	if err != nil {
		result.FailureReason = err.Error()
		return
	}
	done := make(chan struct{})
	go func() { defer close(done); engine.Run(ctx) }()
	defer func() { cancel(); <-done }()
	input, err := inputop.NewRuntime(f.ctl, nil)
	if err != nil {
		result.FailureReason = err.Error()
		return
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var output jobdb.JobData
	for {
		inspection, inspectErr := f.ctl.InspectJob(ctx, key)
		if inspectErr != nil {
			result.FailureReason = inspectErr.Error()
			break
		}
		if inspection.Terminal {
			output = inspection.Output
			if inspection.Status == "completed" {
				result.Status = "passed"
			} else {
				result.FailureReason = inspection.FailureMessage
				if result.FailureReason == "" {
					result.FailureReason = inspection.Status
				}
			}
			break
		}
		if err = f.answerInputs(ctx, tenant, input); err != nil {
			result.FailureReason = err.Error()
			break
		}
		select {
		case <-ctx.Done():
			result.Status = "timed_out"
			result.FailureReason = ctx.Err().Error()
		case <-ticker.C:
			continue
		}
		break
	}
	cancel()
	<-done
	artifacts := map[string][]byte{}
	if output != nil {
		raw, readErr := output.GetData()
		if readErr == nil {
			readErr = json.Unmarshal(raw, &result.Outputs)
		}
		if result.Status != "passed" && result.Outputs != nil {
			if message, ok := result.Outputs["message"].(string); ok {
				result.FailureReason = message
			}
		}
		if readErr != nil {
			result.Status = "failed"
			result.FailureReason = readErr.Error()
		}
		list, readErr := output.GetArtifacts()
		if readErr == nil {
			for _, artifact := range list {
				b, e := artifact.Bytes(parent)
				if e == nil {
					artifacts[artifact.Name()] = b
				}
			}
		}
	}
	if expected := req.Case.Runtime.ExpectError; expected != "" {
		if result.Status == "failed" && strings.Contains(result.FailureReason, expected) {
			result.Status = "passed"
			result.FailureReason = ""
		} else {
			result.Status = "failed"
			result.FailureReason = fmt.Sprintf("expected error containing %q; got %s", expected, result.FailureReason)
		}
	}
	executed := map[string]bool{}
	for _, call := range f.report.Calls {
		executed[call.NodePath] = true
	}
	var ordinary []Assertion
	for _, assertion := range req.Case.Assertions {
		if assertion.Type == "op_input_equals" || assertion.Type == "op_call_count" || assertion.Type == "review_document_exists" {
			check := f.assert(assertion)
			result.Assertions = append(result.Assertions, check)
			if !check.Passed {
				markFailure(&result, "assertion_failure", "runtime assertion failed")
			}
		} else {
			ordinary = append(ordinary, assertion)
		}
	}
	checks, failed := runRecipeTestAssertions(ordinary, result.Outputs, artifacts, executed, result.Status, nil, nil, &f.report)
	result.Assertions = append(result.Assertions, checks...)
	if failed {
		markFailure(&result, "assertion_failure", "one or more assertions failed")
	}
	for i := range f.c.Mocks.Ops {
		if _, ok := f.used[i]; !ok {
			markFailure(&result, "mock_unused", fmt.Sprintf("required mock %d was not used", i))
		}
	}
	for i := range f.c.Runtime.Responses {
		if !f.responses[i] {
			markFailure(&result, "response_unused", fmt.Sprintf("required input response %d was not used", i))
		}
	}
	result.Evaluations, failed = runRecipeTestEvaluations(req.Case.Evaluations, result.Outputs, artifacts, req.Execution.EvaluationMode)
	if failed {
		markFailure(&result, "evaluation_failure", "evaluation failed")
	}
	if req.Execution.ArtifactMode == "inline" {
		result.Artifacts = inlineArtifacts(artifacts, req.Execution.ArtifactMaxBytes)
	}
	return
}

func (f *runtimeFixture) jobContext(cell string) contextual.JobContext {
	return contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: cell}, GitBase: contextual.GitBaseContext{BaseRepo: f.cells[cell], BaseRef: "main", GitAuthor: "Recipe Test <recipe-test@example.com>"}, CellResolution: &cellref.Context{Pattern: filepath.Join(f.root, "cells", "${{ cell }}")}}
}

func (f *runtimeFixture) seedCells(ctx context.Context, primary string) error {
	cells := map[string]CellFixture{primary: {}}
	for name, fixture := range f.c.Runtime.Cells {
		cells[name] = fixture
	}
	for name, fixture := range cells {
		dir, err := fixturePath(filepath.Join(f.root, "cells"), name)
		if err != nil {
			return err
		}
		f.cells[name] = dir
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		for file, content := range fixture.Files {
			if err := writeFixture(dir, file, []byte(content)); err != nil {
				return err
			}
		}
		for file, source := range fixture.FileSources {
			data, err := os.ReadFile(filepath.Join(f.opts.FixtureRoot, source))
			if err != nil {
				return err
			}
			if err := writeFixture(dir, file, data); err != nil {
				return err
			}
		}
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Recipe Test"}, {"config", "user.email", "recipe-test@example.com"}, {"config", "receive.denyCurrentBranch", "updateInstead"}, {"add", "."}, {"commit", "-q", "--allow-empty", "-m", "Test fixture"}} {
			command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
			if out, err := command.CombinedOutput(); err != nil {
				return fmt.Errorf("seed fixture: %w: %s", err, out)
			}
		}
	}
	return nil
}

func writeFixture(root, name string, content []byte) error {
	dest, err := fixturePath(root, name)
	if err != nil {
		return err
	}
	// Runtime worktrees may contain symlinks created by real ops. Refuse to
	// traverse them when materializing fixture effects.
	for path := dest; path != root; path = filepath.Dir(path) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture path contains symlink: %s", name)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return os.WriteFile(dest, content, 0644)
}

func (f *runtimeFixture) invoke(deps coreops.OpDependencies, ctx context.Context, op string, real func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error), in map[string]any) (map[string]any, error) {
	g := deps.GitContext()
	cell := g.CellName
	if g.Workspace != nil {
		cell = g.Workspace.Cell
	}
	key := deps.JobTool().GetJobKey()
	identity := key.JobId + "/" + g.InvokeHash + "/" + op
	f.mu.Lock()
	idx, exists := f.selected[identity]
	if !exists {
		eligible := append([]OpMock(nil), f.c.Mocks.Ops...)
		for i := range eligible {
			if eligible[i].Match.Cell != "" && eligible[i].Match.Cell != cell {
				eligible[i].Match.Op = "__unmatched__"
				eligible[i].Match.NodePath = "__unmatched__"
			}
		}
		idx, exists = selectOpMock(eligible, g.NodePath, op, f.used)
		if exists {
			f.selected[identity] = idx
			f.used[idx] = struct{}{}
		}
		f.report.Calls = append(f.report.Calls, OpCall{JobID: key.JobId, Cell: cell, NodePath: g.NodePath, Op: op, Inputs: in})
	}
	f.mu.Unlock()
	if !exists {
		// Built-in orchestration runs normally; external work requires an explicit
		// fixture or passthrough declaration so offline suites cannot call models.
		if op == "extension_execution" || op == "command_execution" {
			return nil, fmt.Errorf("no runtime fixture for %s at %s", op, g.NodePath)
		}
		return real(deps, ctx, in)
	}
	mock := f.c.Mocks.Ops[idx]
	if mock.Behavior.Mode == "passthrough" {
		return real(deps, ctx, in)
	}
	if mock.Behavior.Mode == "fail" {
		message := "fixture failure"
		if mock.Behavior.Error != nil {
			message = mock.Behavior.Error.Message
		}
		return nil, fmt.Errorf("%s", message)
	}
	deps.SetNextTaskType("")
	out := cloneStringMap(mock.Behavior.Outputs)
	if out == nil {
		out = map[string]any{}
	}
	for name, content := range mock.Behavior.Artifacts {
		if err := deps.AddOutputArtifact(jobdb.NewArtifactFromBytes(name, []byte(content))); err != nil {
			return nil, err
		}
	}
	if effects := mock.Behavior.Effects; effects != nil {
		for name, content := range effects.Worktree {
			if err := writeFixture(deps.WorktreePath(), name, []byte(content)); err != nil {
				return nil, err
			}
		}
		for name, source := range effects.ArtifactFiles {
			b, err := os.ReadFile(filepath.Join(f.opts.FixtureRoot, source))
			if err != nil {
				return nil, err
			}
			if err := deps.AddOutputArtifact(jobdb.NewArtifactFromBytes(name, b)); err != nil {
				return nil, err
			}
		}
		for name, object := range effects.Objects {
			files := map[string]string{}
			for part, source := range object.Files {
				files[part] = filepath.Join(f.opts.FixtureRoot, source)
			}
			ref, err := deps.Objects().Publish(ctx, object.Type, object.Metadata, files)
			if err != nil {
				return nil, err
			}
			out[name] = ref
		}
		for _, child := range effects.Children {
			data, err := os.ReadFile(filepath.Join(f.opts.FixtureRoot, child.Recipe))
			if err != nil {
				return nil, err
			}
			target, _, err := ExpandInlineRecipeTarget(ctx, TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: string(data)}, InlineTargetExpansionOptions{RootFile: filepath.Join(f.opts.FixtureRoot, child.Recipe), ProjectID: key.TenantId})
			if err != nil {
				return nil, err
			}
			r, err := recipe.LoadInternalRecipeFromReader(bytes.NewBufferString(target.Content))
			if err != nil {
				return nil, err
			}
			request, err := childbroker.NewSubmitRequest(ctx, workflowctl.StartJob{TenantId: key.TenantId, RecipeName: r.GetMetadata().ID, Inputs: child.Inputs, GitRef: "main", JobContext: f.jobContext(child.Cell)}, nil, *r)
			if err != nil {
				return nil, err
			}
			env := deps.ProtectedEnv()
			broker, ok, err := jobcontext.ChildJobBrokerFromEnv(func(name string) string { return env[name] })
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("child fixture requires a lease-scoped broker")
			}
			_, err = childbroker.Submit(ctx, broker, request)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (f *runtimeFixture) answerInputs(ctx context.Context, tenant string, input *inputop.Runtime) error {
	pending, err := input.ListPendingInputs(ctx, tenant)
	if err != nil {
		return err
	}
	for _, item := range pending {
		var nodePath, cell string
		f.mu.Lock()
		for i := len(f.report.Calls) - 1; i >= 0; i-- {
			call := f.report.Calls[i]
			if call.JobID == item.Id && call.Op == "input" {
				nodePath, cell = call.NodePath, call.Cell
				break
			}
		}
		f.mu.Unlock()
		form, err := input.GetForm(ctx, tenant, item.Id)
		if err != nil {
			continue
		}
		matched := false
		for i, response := range f.c.Runtime.Responses {
			if f.responses[i] || response.NodePath != nodePath {
				continue
			}
			if response.Cell != "" && response.Cell != cell {
				continue
			}
			fields := cloneStringMap(response.Fields)
			if fields == nil {
				fields = map[string]any{}
			}
			for field, source := range response.Attachments {
				b, err := os.ReadFile(filepath.Join(f.opts.FixtureRoot, source))
				if err != nil {
					return err
				}
				fields[field] = jobdb.NewArtifactFromBytes(filepath.Base(source), b)
			}
			actor := inputop.Actor{ID: "recipe-test", Kind: "test"}
			if form.ResponseSchema != nil {
				_, err = input.SubmitStructuredResponse(ctx, tenant, item.Id, inputop.StructuredSubmission{RequestID: form.RequestID, SubmissionID: fmt.Sprint(i), Response: response.Response}, actor)
			} else {
				_, err = input.SubmitFormResponse(ctx, tenant, item.Id, inputop.FormSubmission{RequestID: form.RequestID, SubmissionID: fmt.Sprint(i), Fields: fields, Response: response.Response}, actor)
			}
			if err != nil {
				return err
			}
			f.responses[i] = true
			f.report.Reviews = append(f.report.Reviews, ReviewCall{NodePath: response.NodePath, Form: form})
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("no input response fixture for %s", nodePath)
		}
	}
	return nil
}

func (f *runtimeFixture) assert(a Assertion) AssertionResult {
	// Reuse the ordinary output assertions for typed equality and path traversal.
	var value any
	if a.Type == "review_document_exists" {
		for _, review := range f.report.Reviews {
			if review.NodePath == a.NodePath {
				form := review.Form.(inputop.InputForm)
				_, ok := form.Documents[a.Path]
				if ok {
					value = true
				}
			}
		}
		if value == nil {
			value = false
		}
		a.Value = true
	} else {
		calls := []OpCall{}
		for _, call := range f.report.Calls {
			if call.NodePath == a.NodePath {
				calls = append(calls, call)
			}
		}
		if a.Type == "op_call_count" {
			value = len(calls)
		} else if len(calls) > 0 {
			value = calls[len(calls)-1].Inputs
		}
	}
	path := "value"
	if a.Type == "op_input_equals" && a.Path != "" {
		path += "." + a.Path
	}
	checks, _ := runRecipeTestAssertions([]Assertion{{Type: "output_equals", Path: path, Value: a.Value}}, map[string]any{"value": value}, nil, nil, "passed", nil, nil)
	checks[0].Type = a.Type
	return checks[0]
}
