package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	gitops "github.com/colony-2/c2j/pkg/git/export"
	inputop "github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/objects"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func seededBehavior() MockBehavior {
	return MockBehavior{Mode: "return", FixtureRefs: map[string]FixtureReference{"repo": {Cell: "root", Field: "repo"}}, OutputsFixtureExpr: expr(`{"nested":{"repo":repo}}`)}
}
func fixtureCase(b MockBehavior) Case {
	return Case{ID: "fixture", Type: "integration_case", Runtime: &RuntimeCase{}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{NodePath: "fixture"}, Behavior: b}}}}
}

func TestFixtureDeclarations(t *testing.T) {
	valid := fixtureCase(seededBehavior())
	require.Empty(t, validateResponses(valid))
	for name, change := range map[string]func(*Case){
		"unknown": func(c *Case) {
			c.Mocks.Ops[0].Behavior.FixtureRefs["unused"] = FixtureReference{Cell: "dynamic", Field: "repo"}
		},
		"field": func(c *Case) {
			c.Mocks.Ops[0].Behavior.FixtureRefs["unused"] = FixtureReference{Cell: "root", Field: "path"}
		},
		"alias": func(c *Case) {
			c.Mocks.Ops[0].Behavior.FixtureRefs["a-b"] = FixtureReference{Cell: "root", Field: "repo"}
		},
		"reserved": func(c *Case) {
			c.Mocks.Ops[0].Behavior.FixtureRefs["children"] = FixtureReference{Cell: "root", Field: "repo"}
		},
		"empty":            func(c *Case) { c.Mocks.Ops[0].Behavior.OutputsFixtureExpr = expr("") },
		"undeclared":       func(c *Case) { c.Mocks.Ops[0].Behavior.OutputsFixtureExpr = expr(`{"repo": beta}`) },
		"literal":          func(c *Case) { c.Mocks.Ops[0].Behavior.Outputs = map[string]any{} },
		"child expression": func(c *Case) { c.Mocks.Ops[0].Behavior.OutputsExpr = expr(`{}`) },
		"legacy":           func(c *Case) { c.Runtime = nil },
		"fail":             func(c *Case) { c.Mocks.Ops[0].Behavior.Mode = "fail" },
		"passthrough":      func(c *Case) { c.Mocks.Ops[0].Behavior.Mode = "passthrough" },
		"primary":          func(c *Case) { c.Runtime.Cell = "unseeded" },
	} {
		t.Run(name, func(t *testing.T) {
			c := fixtureCase(seededBehavior())
			change(&c)
			require.NotEmpty(t, validateRuntimeCase(HarnessOptions{}, c))
		})
	}
	for _, expression := range []string{`inputs.secret`, `secrets.key`, `cells.root.repo`, `children.a`, `environment.HOME`, `services.registry`, `{`} {
		_, err := seededProgram(expression, seededBehavior().FixtureRefs)
		require.Error(t, err)
	}
	for _, raw := range []string{
		`{"fixture_refs":null}`, `{"fixture_refs":{"r":null}}`, `{"fixture_refs":{"r":{}}}`, `{"fixture_refs":{"r":{"cell":null,"field":"repo"}}}`, `{"fixture_refs":{"r":{"cell":" ","field":"repo"}}}`, `{"fixture_refs":{"r":{"cell":"root","field":null}}}`, `{"fixture_refs":{"r":{"cell":"root","field":"repo","extra":1}}}`, `{"fixture_refs":[]}`,
		`{"outputs_fixture_expr":null}`, `{"outputs_fixture_expr":""}`, `{"outputs_fixture_expr":2}`, `{"outputs_fixture_expr":"{}","outputs":null}`, `{"outputs_fixture_expr":"{}","outputs":{}}`, `{"outputs_fixture_expr":"{}","outputs_expr":null}`, `{"outputs_fixture_expr":"{}","outputs_expr":""}`,
		`{"artifacts_expr":null}`, `{"artifacts_expr":{"a":null}}`, `{"artifacts_expr":{"a":" "}}`, `{"effects":{"worktree_expr":null}}`, `{"effects":{"worktree_expr":{"a":null}}}`,
	} {
		var b MockBehavior
		require.Error(t, json.Unmarshal([]byte(raw), &b), raw)
	}
}

func TestFixtureComputedTypesAndSerialization(t *testing.T) {
	for _, expression := range []string{`1`, `null`, `[1]`, `{1:"bad"}`, `{"x":b"bytes"}`, `{"x":timestamp("2020-01-01T00:00:00Z")}`, `{"x":duration("1s")}`, `{"x":0.0/0.0}`} {
		b := seededBehavior()
		b.OutputsFixtureExpr = expr(expression)
		_, err := preflightFixture(b, map[string]string{"root": "registered"}, t.TempDir())
		require.Error(t, err, expression)
	}
	for _, expression := range []string{`1`, `null`, `[1]`, `{}`, `b"bytes"`} {
		b := seededBehavior()
		b.ArtifactsExpr = map[string]string{"a": expression}
		_, err := preflightFixture(b, map[string]string{"root": "registered"}, t.TempDir())
		require.Error(t, err, expression)
	}
	b := seededBehavior()
	b.ArtifactsExpr = map[string]string{"config": `json_encode({"repo":repo,"quote":"\"\n","nested":[true,null,1]})`}
	r, err := preflightFixture(b, map[string]string{"root": "registered"}, t.TempDir())
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(r.artifacts["config"]), &decoded))
	require.Equal(t, "\"\n", decoded["quote"])
	require.Equal(t, "registered", decoded["repo"])
}

func TestFixtureBounds(t *testing.T) {
	for _, n := range []int{65536, 65537} {
		b := seededBehavior()
		b.ArtifactsExpr = map[string]string{"a": fmt.Sprintf("%q", strings.Repeat("é", n/2)+strings.Repeat("x", n%2))}
		_, err := preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
		require.Equal(t, n > 65536, err != nil, "file %d: %v", n, err)
		b = seededBehavior()
		b.OutputsFixtureExpr = expr(`{"x":` + fmt.Sprintf("%q", strings.Repeat("x", n-8)) + `}`)
		_, err = preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
		require.Equal(t, n > 65536, err != nil, "object %d: %v", n, err)
		b = seededBehavior()
		b.ArtifactsExpr = map[string]string{"a": `json_encode(` + fmt.Sprintf("%q", strings.Repeat("x", n-2)) + `)`}
		_, err = preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
		require.Equal(t, n > 65536, err != nil, "encode %d: %v", n, err)
	}
	for _, overflow := range []bool{false, true} {
		b := seededBehavior()
		b.ArtifactsExpr = map[string]string{}
		for i := 0; i < 4; i++ {
			b.ArtifactsExpr[fmt.Sprint(i)] = fmt.Sprintf("%q", strings.Repeat("x", 65536))
		}
		if overflow {
			b.ArtifactsExpr["later"] = `"x"`
		}
		_, err := preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
		require.Equal(t, overflow, err != nil, "aggregate: %v", err)
	}
	for _, n := range []int{64, 65} {
		b := seededBehavior()
		b.FixtureRefs = map[string]FixtureReference{}
		b.ArtifactsExpr = map[string]string{}
		for i := 0; i < n; i++ {
			b.FixtureRefs[fmt.Sprintf("r%d", i)] = FixtureReference{Cell: "root", Field: "repo"}
			b.ArtifactsExpr[fmt.Sprint(i)] = `""`
		}
		b.OutputsFixtureExpr = nil
		require.Equal(t, n > 64, len(validateResponses(fixtureCase(b))) > 0)
		if n == 64 {
			_, err := preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
			require.NoError(t, err)
		}
	}
	// Each exists() scans its entire list. Individually bounded evaluations can
	// collectively exceed the aggregate cost without exceeding any size bound.
	list := "[" + strings.Repeat("0,", 999) + "0]"
	small := `(` + list + `).exists(x,x == 1) ? "yes" : "no"`
	b := seededBehavior()
	b.ArtifactsExpr = map[string]string{"a": small}
	_, err := preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
	require.NoError(t, err)
	for i := 0; i < 30; i++ {
		b.ArtifactsExpr[fmt.Sprintf("%02d", i)] = small
	}
	_, err = preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
	require.ErrorContains(t, err, "aggregate CEL cost")
	b = seededBehavior()
	b.OutputsFixtureExpr = expr(`{"x": (` + list + `).exists(x, (` + list + `).exists(y, x+y == 1))}`)
	_, err = preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
	require.ErrorContains(t, err, "cost limit")
}

func TestFixtureDestinationPreflight(t *testing.T) {
	for _, effects := range []*FixtureEffects{
		{Worktree: map[string]string{"a": "literal"}, WorktreeExpr: map[string]string{"./a": `"computed"`}},
		{WorktreeExpr: map[string]string{"a": `"a"`, "a/b": `"b"`}},
		{WorktreeExpr: map[string]string{"/absolute": `"x"`}},
		{WorktreeExpr: map[string]string{"../escape": `"x"`}},
	} {
		b := seededBehavior()
		b.Effects = effects
		_, err := preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
		require.Error(t, err)
	}
	for _, source := range []bool{false, true} {
		b := seededBehavior()
		b.ArtifactsExpr = map[string]string{"a": `"text"`}
		if source {
			b.Effects = &FixtureEffects{ArtifactFiles: map[string]string{"a": "source"}}
		} else {
			b.Artifacts = map[string]string{"a": "literal"}
		}
		_, err := preflightFixture(b, map[string]string{"root": "r"}, t.TempDir())
		require.ErrorContains(t, err, "conflicts")
	}
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))
	b := seededBehavior()
	b.Effects = &FixtureEffects{WorktreeExpr: map[string]string{"link/escape": `"text"`}}
	_, err := preflightFixture(b, map[string]string{"root": "r"}, root)
	require.ErrorContains(t, err, "symlink")
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0600))
	b.Effects.WorktreeExpr = map[string]string{"file/sub": `"x"`}
	_, err = preflightFixture(b, map[string]string{"root": "r"}, root)
	require.Error(t, err)
}

func TestFixtureFailureCannotBeExpectedOrCaught(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	for _, kind := range []string{"late file", "object collision", "size"} {
		for _, accept := range []string{"case", "runtime", "catch"} {
			t.Run(kind+"/"+accept, func(t *testing.T) {
				b := seededBehavior()
				b.Artifacts = map[string]string{"literal.txt": "must not publish"}
				b.ArtifactsExpr = map[string]string{"a": `"success"`, "z": `1`}
				b.Effects = &FixtureEffects{Worktree: map[string]string{"selected.txt": "must not write"}, Objects: map[string]ObjectFixture{"checkpoint": {Type: "neutral/v1"}}, Children: []ChildFixture{{Cell: "root", Recipe: "absent.yaml"}}}
				if kind == "object collision" {
					b.ArtifactsExpr["z"] = `"ok"`
					b.OutputsFixtureExpr = expr(`{"checkpoint":repo}`)
				}
				if kind == "size" {
					b.ArtifactsExpr["z"] = fmt.Sprintf("%q", strings.Repeat("x", 65537))
				}
				c := fixtureCase(b)
				c.Runtime.Cells = map[string]CellFixture{"root": {}}
				c.Runtime.ObserveFiles = []string{"selected.txt"}
				parent := "id: fixture\nop: command_execution\ninputs: {run: unused}\n"
				if accept == "case" {
					c.ExpectError = "mock[0]"
				}
				if accept == "runtime" {
					c.Runtime.ExpectError = "mock[0]"
				}
				if accept == "catch" {
					parent += "catch:\n- when: 'true'\n  continue:\n    outputs: {accepted: true}\n"
				}
				r := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "fixture", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent}, c, ExecutionOptions{Timeout: "10s", ArtifactMode: "inline"})
				require.Equal(t, "failed", r.Status, "%+v", r)
				require.Contains(t, r.FailureReason, "mock[0]")
				require.Equal(t, "invalid_runtime_fixture", r.FailureCategory)
				require.NotNil(t, r.Runtime)
				require.Empty(t, r.Artifacts)
				for _, call := range r.Runtime.Calls {
					require.Empty(t, call.Outputs)
					require.Empty(t, call.Worktree["selected.txt"])
				}
				for _, e := range r.Runtime.Events {
					require.NotEqual(t, "child_submitted", e.Kind)
				}
				require.Empty(t, r.Runtime.Repositories["root"].Files)
			})
		}
	}
	// Even unused invalid declarations must prevent all root execution.
	c := fixtureCase(seededBehavior())
	c.Mocks.Ops[0].Behavior.FixtureRefs["unused"] = FixtureReference{Cell: "dynamic", Field: "repo"}
	c.ExpectError = "unknown"
	r := RunCase(context.Background(), HarnessOptions{}, "fixture", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: fixture\nop: command_execution\ninputs: {run: unused}\n"}, c, ExecutionOptions{})
	require.NotEqual(t, "passed", r.Status)
	require.Nil(t, r.Runtime)
}

func TestFixtureNativeParallel(t *testing.T) {
	ops := append(append(recipeops.GetOps(), inputop.GetOp()), commandop.GetOp())
	ops = append(ops, gitops.GetAll()...)
	withRegisteredCoreOps(t, ops...)
	dir := filepath.Join("testdata", "native-fixture-refs")
	data, err := os.ReadFile(filepath.Join(dir, "native-fixture-refs.test.json"))
	require.NoError(t, err)
	var suite struct {
		Cases []Case `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &suite))
	parent, err := os.ReadFile(filepath.Join(dir, "parent.yaml"))
	require.NoError(t, err)
	results := make([]CaseRunResult, len(suite.Cases))
	var wg sync.WaitGroup
	for i, c := range suite.Cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: dir}, "fixture", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: string(parent)}, c, ExecutionOptions{Timeout: "30s", ArtifactMode: "inline", ArtifactMaxBytes: 524288})
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, r := range results {
		require.Equal(t, "passed", r.Status, "%+v", r)
		require.NotNil(t, r.Runtime)
		proposal := r.Outputs["proposal"].(map[string]any)
		require.Equal(t, r.Runtime.Repositories["root"].Repo, proposal["primary"])
		for i, cell := range []string{"alpha", "beta"} {
			repo := proposal["cells"].([]any)[i].(map[string]any)["repo"].(string)
			require.Equal(t, r.Runtime.Repositories[cell].Repo, repo)
			require.False(t, seen[repo])
			seen[repo] = true
		}
		childCalls := 0
		for _, call := range r.Runtime.Calls {
			if call.NodePath == "fixture_child" {
				childCalls++
				require.Equal(t, proposal["cells"], call.Outputs["cells"])
				require.Equal(t, proposal["primary"], call.Outputs["primary"])
			}
		}
		require.Equal(t, 1, childCalls)
		require.Len(t, r.Runtime.Reviews, 1)
		formBytes, err := json.Marshal(r.Runtime.Reviews[0].Form)
		require.NoError(t, err)
		require.Contains(t, string(formBytes), "root-config.yaml")
		var reviewed []byte
		for _, call := range r.Runtime.Calls {
			if call.NodePath == "fixture_refs/compare" {
				reviewed = []byte(call.Artifacts["reviewed.yaml"])
			}
		}
		saved := []byte(r.Runtime.Repositories["root"].Files[".c2/config.yaml"])
		require.NotEmpty(t, reviewed)
		require.NotEmpty(t, saved)
		var form map[string]any
		require.NoError(t, json.Unmarshal(formBytes, &form))
		require.Equal(t, r.Outputs["reviewed_ref"], form["documents"].(map[string]any)["config"])
		require.Equal(t, string(reviewed), string(saved))
		require.Equal(t, string(saved), r.Runtime.Repositories["root"].Files[".c2/config.yaml"])
		t.Logf("registered=%v proposal=%v reviewed=%s saved=%s review=%s", r.Runtime.Repositories, proposal, reviewed, saved, formBytes)
	}
}

// Count effect entry itself; failed invocations do not necessarily persist
// output artifacts, so an empty final result alone cannot establish preflight.
type fixtureEffectDeps struct {
	coreops.OpDependencies
	artifacts, objects int
}

func (d *fixtureEffectDeps) AddOutputArtifact(a jobdb.Artifact) error {
	d.artifacts++
	return d.OpDependencies.AddOutputArtifact(a)
}
func (d *fixtureEffectDeps) Objects() *objects.Store { d.objects++; return d.OpDependencies.Objects() }

type fixtureJobTool struct{}

func (fixtureJobTool) InvocationID() string { return "invocation" }
func (fixtureJobTool) GetJobKey() jobdb.JobKey {
	return jobdb.JobKey{TenantId: "fixture", JobId: "job"}
}
func (fixtureJobTool) AwaitJobs(...string) error { return nil }

func TestFixturePreflightHasNoEffectEntry(t *testing.T) {
	for _, failure := range []string{"late computation", "unresolved", "object collision", "artifact collision", "cleaned path", "file directory", "symlink", "aggregate text", "aggregate cost"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			b := seededBehavior()
			b.Artifacts = map[string]string{"literal": "should not publish"}
			b.ArtifactsExpr = map[string]string{"a": `"earlier success"`}
			b.Effects = &FixtureEffects{Worktree: map[string]string{"literal.txt": "should not write"}, WorktreeExpr: map[string]string{"computed.txt": `"success"`}, Objects: map[string]ObjectFixture{"checkpoint": {Type: "neutral/v1"}}, Children: []ChildFixture{{Cell: "root", Recipe: "child.yaml", Bind: "child"}}}
			repos := map[string]string{"root": "registered"}
			switch failure {
			case "late computation":
				b.Effects.WorktreeExpr["z"] = `1`
			case "unresolved":
				repos = nil
			case "object collision":
				b.OutputsFixtureExpr = expr(`{"checkpoint":repo}`)
			case "artifact collision":
				b.ArtifactsExpr["literal"] = `"collision"`
			case "cleaned path":
				b.Effects.WorktreeExpr["./literal.txt"] = `"collision"`
			case "file directory":
				require.NoError(t, os.WriteFile(filepath.Join(root, "parent"), []byte("file"), 0600))
				b.Effects.WorktreeExpr["parent/sub"] = `"bad"`
			case "symlink":
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "link")))
				b.Effects.WorktreeExpr["link/sub"] = `"bad"`
			case "aggregate text":
				for i := 0; i < 4; i++ {
					b.ArtifactsExpr[fmt.Sprint(i)] = fmt.Sprintf("%q", strings.Repeat("x", 65536))
				}
			case "aggregate cost":
				list := "[" + strings.Repeat("0,", 999) + "0]"
				for i := 0; i < 30; i++ {
					b.ArtifactsExpr[fmt.Sprintf("%02d", i)] = `(` + list + `).exists(x,x == 1) ? "yes" : "no"`
				}
			}
			f := &runtimeFixture{root: t.TempDir(), c: fixtureCase(b), seededRepos: repos, selected: map[string]int{}, callIndexes: map[string]int{}, used: map[int]struct{}{}}
			deps := &fixtureEffectDeps{OpDependencies: coreops.NewOpDependenciesBuilder().WithWorktreePath(root).WithJobTool(fixtureJobTool{}).WithGitContext(coreops.GitExecutionContext{NodePath: "fixture", CellName: "root"}).Build()}
			out, err := f.invoke(deps, context.Background(), "command_execution", nil, map[string]any{})
			require.Error(t, err)
			require.Nil(t, out)
			require.NotEmpty(t, f.fixtureFailure)
			require.Zero(t, deps.artifacts)
			require.Zero(t, deps.objects)
			require.NoFileExists(t, filepath.Join(root, "literal.txt"))
			require.NoFileExists(t, filepath.Join(root, "computed.txt"))
			receipts, err := f.receipts()
			require.NoError(t, err)
			require.Empty(t, receipts)
			for _, event := range f.report.Events {
				require.NotEqual(t, "child_submitted", event.Kind)
			}
		})
	}
}

func TestFixtureCountsIndependently(t *testing.T) {
	for _, kind := range []string{"references", "files"} {
		for _, n := range []int{64, 65} {
			b := seededBehavior()
			b.OutputsFixtureExpr = nil
			if kind == "references" {
				b.FixtureRefs = map[string]FixtureReference{}
				for i := 0; i < n; i++ {
					b.FixtureRefs[fmt.Sprintf("r%d", i)] = FixtureReference{Cell: "root", Field: "repo"}
				}
			} else {
				b.ArtifactsExpr = map[string]string{}
				b.Effects = &FixtureEffects{WorktreeExpr: map[string]string{}}
				for i := 0; i < n; i++ {
					if i%2 == 0 {
						b.ArtifactsExpr[fmt.Sprint(i)] = `""`
					} else {
						b.Effects.WorktreeExpr[fmt.Sprint(i)] = `""`
					}
				}
			}
			require.Equal(t, n > 64, len(validateResponses(fixtureCase(b))) > 0, "%s %d", kind, n)
			if n == 64 {
				_, err := preflightFixture(b, map[string]string{"root": "registered"}, t.TempDir())
				require.NoError(t, err)
			}
		}
	}
}

func TestFixtureLiteralSurfaces(t *testing.T) {
	literal := "${{ cells.alpha.repo }}\n{fixture: cells.alpha.repo}\n"
	probe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "fixture_literal_probe", AcceptsArtifacts: true}, func(deps coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		saved, err := os.ReadFile(filepath.Join(deps.WorktreePath(), "literal.txt"))
		require.NoError(t, err)
		require.Equal(t, literal, string(saved))
		require.Len(t, deps.GetInputArtifacts(), 2)
		for _, a := range deps.GetInputArtifacts() {
			data, err := a.Bytes(ctx)
			require.NoError(t, err)
			require.Equal(t, literal, string(data))
		}
		return map[string]any{"saved": string(saved)}, nil
	})
	withRegisteredCoreOps(t, commandop.GetOp(), probe)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "literal.txt"), []byte(literal), 0600))
	parent := `id: literal
sequence:
- id: emit
  op: command_execution
  inputs: {run: unused}
- id: inspect
  op: fixture_literal_probe
  artifacts:
    inline.txt: '${{ sequence.emit.artifacts["inline.txt"] }}'
    source.txt: '${{ sequence.emit.artifacts["source.txt"] }}'
outputs:
  literal: '${{ sequence.emit.outputs.literal }}'
  text: '${{ sequence.emit.outputs.text }}'
  saved: '${{ sequence.inspect.outputs.saved }}'
`
	c := Case{ID: "literal", Type: "integration_case", Runtime: &RuntimeCase{}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{NodePath: "literal/emit"}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"literal": map[string]any{"fixture": "cells.alpha.repo"}, "text": literal}, Artifacts: map[string]string{"inline.txt": literal}, Effects: &FixtureEffects{ArtifactFiles: map[string]string{"source.txt": "literal.txt"}, Worktree: map[string]string{"literal.txt": literal}}}}}}}
	r := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), FixtureRoot: dir, WorkRoot: t.TempDir()}, "literal", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: parent}, c, ExecutionOptions{Timeout: "10s"})
	require.Equal(t, "passed", r.Status, r.FailureReason)
	require.Equal(t, map[string]any{"fixture": "cells.alpha.repo"}, r.Outputs["literal"])
	require.Equal(t, literal, r.Outputs["text"])
	require.Equal(t, literal, r.Outputs["saved"])
}

func TestFixtureRegistrationRejections(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt, closeRuntime, err := newCaseRuntime(ctx, t.TempDir(), "fixture", &RuntimeCase{Backend: backend})
			require.NoError(t, err)
			defer func() { require.NoError(t, closeRuntime()) }()
			first, err := rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "fixture", Name: "alpha", Repo: "example.test/alpha", RequestID: "first"})
			require.NoError(t, err)
			_, err = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "fixture", Name: "alpha", Repo: "example.test/other", RequestID: "second"})
			require.ErrorIs(t, err, jobdb.ErrCellConflict)
			current, err := rt.GetCell(ctx, jobdb.CellKey{TenantID: "fixture", CellID: first.ID})
			require.NoError(t, err)
			require.Equal(t, first.Repo, current.Repo)
			_, err = rt.CreateCell(context.Background(), jobdb.CreateCellRequest{TenantID: "fixture", Name: "denied", Repo: "example.test/denied"})
			require.ErrorIs(t, err, jobdb.ErrCellForbidden)
			_, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "fixture", Cell: "alpha", CellRepo: "example.test/other", JobType: "probe", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.ErrorIs(t, err, jobdb.ErrCellRepoMismatch)
		})
	}
}
