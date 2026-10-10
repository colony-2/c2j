package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

// Run as the packaged executable in a subprocess. This is a real JSON-speaking
// op with artifact validation; only package retrieval is faked by the tests.
func TestNixPassthroughFixtureProcess(t *testing.T) {
	if os.Getenv("C2J_PASSTHROUGH_FIXTURE") != "1" {
		return
	}
	if err := runNixPassthroughFixture(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runNixPassthroughFixture() error {
	var in struct {
		Path          string `json:"path"`
		Outbox        string `json:"outbox"`
		InvalidOutput bool   `json:"invalid_output"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return err
	}
	f, err := os.OpenFile(os.Getenv("C2J_PASSTHROUGH_RUN_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString("run\n")
	f.Close()
	if err != nil {
		return err
	}
	artifact, err := os.ReadFile(in.Path)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(artifact, &value); err != nil {
		return err
	}
	validator := jsonschema.NewCompiler()
	if err := validator.AddResource("https://test.invalid/result.json", map[string]any{
		"type": "object", "required": []any{"ok"}, "properties": map[string]any{"ok": map[string]any{"const": true}},
	}); err != nil {
		return err
	}
	schema, err := validator.Compile("https://test.invalid/result.json")
	if err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("result artifact failed schema validation: %w", err)
	}
	out := map[string]any{"ok": true}
	for name, command := range map[string][]string{
		"recipe_tool": {"uvx", "--from", "uv-probe==1", "uv-probe"},
		"op_tool":     {"uv-probe"},
		"node_tool":   {"pnpm", "--package=pnpm-probe@3", "dlx", "pnpm-probe"},
	} {
		b, err := exec.Command(command[0], command[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, b)
		}
		out[name] = strings.TrimSpace(string(b))
	}
	if err := os.WriteFile(filepath.Join(in.Outbox, "checked.json"), artifact, 0600); err != nil {
		return err
	}
	if in.InvalidOutput {
		out["ok"] = "wrong type"
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"output": out})
}

type nixPassthroughFixture struct {
	root, packageDir, runLog, installLog, nixLog string
	manifest                                     []byte
}

func newNixPassthroughFixture(t *testing.T) *nixPassthroughFixture {
	t.Helper()
	withRegisteredCoreOps(t, extops.GetExecutionOp(), commandop.GetOp())
	f := &nixPassthroughFixture{root: t.TempDir(), packageDir: t.TempDir()}
	f.runLog = filepath.Join(f.root, "runs")
	f.installLog = filepath.Join(f.root, "installs")
	f.nixLog = filepath.Join(f.root, "nix-calls")
	t.Setenv("C2J_TOOL_CACHE_DIR", filepath.Join(f.root, "cache"))
	t.Setenv("C2J_PASSTHROUGH_RUN_LOG", f.runLog)
	t.Setenv("C2J_PASSTHROUGH_INSTALL_LOG", f.installLog)
	t.Setenv("C2J_PASSTHROUGH_NIX_LOG", f.nixLog)
	system, err := toolenv.NixSystem()
	require.NoError(t, err)
	f.manifest, err = json.Marshal(map[string]any{
		"name": "probe", "command": []string{"bin/probe"},
		"env":          map[string]string{"C2J_PASSTHROUGH_FIXTURE": "1"},
		"dependencies": []string{"uv:uv-probe==2", "pnpm:pnpm-probe@3"},
		"input_schema": map[string]any{"type": "object", "required": []string{"path", "outbox"}, "properties": map[string]any{
			"path": map[string]any{"type": "string"}, "outbox": map[string]any{"type": "string"}, "invalid_output": map[string]any{"type": "boolean"},
		}},
		"output_schema": map[string]any{"type": "object", "required": []string{"ok"}, "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}},
	})
	require.NoError(t, err)
	description, err := json.Marshal(toolenv.NixPackage{StorePath: "/nix/store/00000000000000000000000000000000-probe", System: system, Manifest: f.manifest})
	require.NoError(t, err)
	t.Setenv("C2J_PASSTHROUGH_DESCRIPTION", string(description))
	bin := filepath.Join(f.root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0700))
	write := func(name, script string) {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0700))
	}
	write("nix", `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$C2J_PASSTHROUGH_NIX_LOG"
case " $* " in
 *' eval '*) printf '%s' "$C2J_PASSTHROUGH_DESCRIPTION";;
 *) echo 'binary cache unavailable' >&2; exit 7;;
esac
`)
	for _, manager := range []string{"uv", "pnpm"} {
		write(manager, `#!/bin/sh
set -eu
for spec do :; done
printf '%s\n' "$spec" >> "$C2J_PASSTHROUGH_INSTALL_LOG"
case "${0##*/}" in
 uv) bin=$UV_TOOL_BIN_DIR; name=uv-probe;;
 pnpm) bin=$PWD/node_modules/.bin; name=pnpm-probe;;
esac
mkdir -p "$bin"
printf '#!/bin/sh\nprintf "%%s\\n" %s\n' "'$spec'" > "$bin/$name"
chmod +x "$bin/$name"
`)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f *nixPassthroughFixture) prepare(t *testing.T, ctx context.Context, req workerops.ToolSetupRequest) workerops.ToolSetupResult {
	t.Helper()
	require.NotNil(t, req.Extension.Nix)
	require.Equal(t, "/nix/store/00000000000000000000000000000000-probe", req.Extension.Nix.StorePath)
	// Model realization in a temp directory so fast tests need neither a writable
	// /nix/store nor Docker. Keep actual manifest execution and tool preparation.
	// Nix store paths are canonical. Match that here even when the temporary
	// directory has a symlinked parent (for example, /var on macOS).
	packageDir, err := filepath.EvalSymlinks(f.packageDir)
	require.NoError(t, err)
	prepared := *req.Extension
	nix := *prepared.Nix
	nix.StorePath = packageDir
	prepared.Nix = &nix
	prepared.ProjectRoot, prepared.OpDir = packageDir, packageDir
	prepared.SpecPath = filepath.Join(packageDir, "share/c2j/op.json")
	prepared.NixRoot = filepath.Join(f.root, "result")
	require.NoError(t, os.MkdirAll(filepath.Dir(prepared.SpecPath), 0700))
	require.NoError(t, os.WriteFile(prepared.SpecPath, f.manifest, 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(packageDir, "bin"), 0700))
	binary, err := os.Executable()
	require.NoError(t, err)
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\\''") + "' -test.run '^TestNixPassthroughFixtureProcess$'\n"
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "bin/probe"), []byte(script), 0700))
	require.NoError(t, os.Symlink(packageDir, prepared.NixRoot))
	result := workerops.PrepareTools(ctx, workerops.ToolSetupRequest{Scopes: req.Scopes})
	result.Extension = &prepared
	require.True(t, result.Ready(), "fixture setup must be ready before returning to the op")
	return result
}

const nixPassthroughRecipe = `id: nix-passthrough
version: "1.0.0"
input_schema: {}
execution: {packages: [uv:uv-probe==1]}
sequence:
  - id: seed
    op: command_execution
    inputs: {run: unused}
  - id: check
    op: nix:github:example/ops/main#probe
    artifacts:
      result.json: '${{ sequence.seed.artifacts["result.json"] }}'
    inputs:
      path: '${{ context.environment.op.inbox }}/result.json'
      outbox: '${{ context.environment.op.outbox }}'
outputs:
  ok: '${{ sequence.check.outputs.ok }}'
  recipe_tool: '${{ sequence.check.outputs.recipe_tool }}'
  op_tool: '${{ sequence.check.outputs.op_tool }}'
  node_tool: '${{ sequence.check.outputs.node_tool }}'
`

func nixPassthroughCase(mode, artifact string) Case {
	return Case{ID: "probe", Type: "recipe_case", Mocks: Mocks{Ops: []OpMock{
		{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "return", Artifacts: map[string]string{"result.json": artifact}}},
		{Match: OpMockMatch{Op: "extension_execution"}, Behavior: MockBehavior{Mode: mode, Outputs: map[string]any{"ok": true, "recipe_tool": "mock", "op_tool": "mock", "node_tool": "mock"}}},
	}}}
}

func TestNixPassthroughLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, mode, artifact, wantError           string
		invalidInput, invalidOutput, setupFailure bool
		symlinkPackageDir                         bool
		wantSetup, wantRuns, wantCalls            int
	}{
		{name: "passthrough", mode: "passthrough", artifact: `{"ok":true}`, wantSetup: 1, wantRuns: 1, wantCalls: 2},
		{name: "symlinked package directory", mode: "passthrough", artifact: `{"ok":true}`, symlinkPackageDir: true, wantSetup: 1, wantRuns: 1, wantCalls: 2},
		{name: "record passthrough", mode: "record_passthrough", artifact: `{"ok":true}`, wantSetup: 1, wantRuns: 1, wantCalls: 2},
		{name: "invalid input", mode: "passthrough", artifact: `{"ok":true}`, invalidInput: true, wantError: "failed to validate selector inputs", wantCalls: 1},
		{name: "rejected result artifact", mode: "passthrough", artifact: `{"ok":false}`, wantError: "result artifact failed schema validation", wantSetup: 1, wantRuns: 1, wantCalls: 2},
		{name: "invalid output", mode: "passthrough", artifact: `{"ok":true}`, invalidOutput: true, wantError: "output failed schema validation", wantSetup: 1, wantRuns: 1, wantCalls: 2},
		{name: "setup failure", mode: "passthrough", artifact: `{"ok":true}`, setupFailure: true, wantError: "tool setup failed: binary cache unavailable", wantSetup: 1, wantCalls: 1},
		{name: "mocked return", mode: "return", artifact: `{"ok":true}`, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNixPassthroughFixture(t)
			if tc.symlinkPackageDir {
				alias := filepath.Join(f.root, "package-alias")
				require.NoError(t, os.Symlink(f.packageDir, alias))
				f.packageDir = alias
			}
			c := nixPassthroughCase(tc.mode, tc.artifact)
			deps := coreops.NewServiceDepsBuilder().Build()
			j := newTestJobContext("test", c, TestPolicy{}, deps)
			j.operationPaths = recipeTestOperationPaths(filepath.Join(f.root, "work"))
			require.NoError(t, ensureRecipeTestOperationDirs(j.operationPaths))
			setups := 0
			j.prepareTools = func(ctx context.Context, req workerops.ToolSetupRequest) workerops.ToolSetupResult {
				setups++
				if tc.setupFailure {
					return workerops.ToolSetupResult{Diagnostics: toolenv.Diagnostics{Error: "binary cache unavailable"}}
				}
				return f.prepare(t, ctx, req)
			}
			source := nixPassthroughRecipe
			if tc.invalidInput {
				source = strings.Replace(source, "path: '${{ context.environment.op.inbox }}/result.json'", "path: 42", 1)
			}
			if tc.invalidOutput {
				source = strings.Replace(source, "outbox: '${{ context.environment.op.outbox }}'", "outbox: '${{ context.environment.op.outbox }}'\n      invalid_output: true", 1)
			}
			rec := mustLoadRecipe(t, source)
			out, _, err := compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil, contextual.JobContext{
				Environment: recipeTestEnvironment(j.operationPaths), Workflow: contextual.WorkflowContext{CellName: "recipe-tests", ProjectId: "test"},
			}, contextual.GitCommitContext{})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
				require.Equal(t, true, out["ok"])
				if tc.mode != "return" {
					require.Equal(t, "uv-probe==1", out["recipe_tool"])
					require.Equal(t, "uv-probe==2", out["op_tool"])
					require.Equal(t, "pnpm-probe@3", out["node_tool"])
					require.JSONEq(t, tc.artifact, string(j.artifactContents["checked.json"]))
				}
			}
			require.Equal(t, tc.wantSetup, setups)
			require.Len(t, j.calls, tc.wantCalls, "setup retries must not count as op calls")
			require.Len(t, j.mockHits, tc.wantCalls, "setup retries must not consume mocks")
			require.Len(t, j.consumedMockIdxs, tc.wantCalls)
			runs, _ := os.ReadFile(f.runLog)
			require.Equal(t, tc.wantRuns, strings.Count(string(runs), "run\n"))
			if tc.wantRuns == 0 {
				require.NoFileExists(t, f.installLog)
			}
			if tc.mode == "record_passthrough" {
				require.Len(t, j.recordings, 1)
			}
		})
	}
}

func TestRunCaseNixPassthroughRequestsRealSetup(t *testing.T) {
	for _, mode := range []string{"passthrough", "record_passthrough", "return"} {
		t.Run(mode, func(t *testing.T) {
			f := newNixPassthroughFixture(t)
			result := RunCase(context.Background(), HarnessOptions{WorkRoot: filepath.Join(f.root, "work")}, "test",
				TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: nixPassthroughRecipe}, nixPassthroughCase(mode, `{"ok":true}`), ExecutionOptions{})
			calls, err := os.ReadFile(f.nixLog)
			require.NoError(t, err)
			require.Contains(t, string(calls), " eval ")
			if mode == "return" {
				require.Equal(t, "passed", result.Status, result.FailureReason)
				require.NotContains(t, string(calls), " build ")
			} else {
				require.Equal(t, "failed", result.Status)
				require.Contains(t, result.FailureReason, "tool setup failed")
				require.Contains(t, result.FailureReason, "binary cache unavailable")
				require.Contains(t, string(calls), " build ")
				require.NotContains(t, result.FailureReason, "invoke a Nix extension directly")
			}
			require.NoFileExists(t, f.installLog)
			require.NoFileExists(t, f.runLog)
		})
	}
}

func TestNixRecordPassthroughThenReplaySkipsSetup(t *testing.T) {
	f := newNixPassthroughFixture(t)
	c := nixPassthroughCase("record_passthrough", `{"ok":true}`)
	c.Mocks.Ops[1].Behavior.CassetteKey = "gate"
	c.Mocks.Ops = append(c.Mocks.Ops, OpMock{
		Match:    OpMockMatch{Op: "extension_execution"},
		Behavior: MockBehavior{Mode: "replay", CassetteKey: "gate"},
	})
	deps := coreops.NewServiceDepsBuilder().Build()
	j := newTestJobContext("test", c, TestPolicy{}, deps)
	j.operationPaths = recipeTestOperationPaths(filepath.Join(f.root, "work"))
	require.NoError(t, ensureRecipeTestOperationDirs(j.operationPaths))
	setups := 0
	j.prepareTools = func(ctx context.Context, req workerops.ToolSetupRequest) workerops.ToolSetupResult {
		setups++
		return f.prepare(t, ctx, req)
	}
	source := strings.Replace(nixPassthroughRecipe, "\noutputs:", `
  - id: replayed
    op: nix:github:example/ops/main#probe
    inputs:
      path: unused
      outbox: unused
outputs:
  replayed: '${{ sequence.replayed.outputs.ok }}'`, 1)
	rec := mustLoadRecipe(t, source)
	out, _, err := compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil, contextual.JobContext{
		Environment: recipeTestEnvironment(j.operationPaths), Workflow: contextual.WorkflowContext{CellName: "recipe-tests", ProjectId: "test"},
	}, contextual.GitCommitContext{})
	require.NoError(t, err)
	require.Equal(t, true, out["replayed"])
	require.Equal(t, 1, setups, "cassette replay must not realize the pending Nix package")
	require.Len(t, j.calls, 3)
	require.Len(t, j.mockHits, 3)
	require.Len(t, j.consumedMockIdxs, 3)
	require.Len(t, j.recordings, 1)
	runs, err := os.ReadFile(f.runLog)
	require.NoError(t, err)
	require.Equal(t, "run\n", string(runs))
	installs, err := os.ReadFile(f.installLog)
	require.NoError(t, err)
	require.Equal(t, "uv-probe==1\nuv-probe==2\npnpm-probe@3\n", string(installs))
}

func TestRunCaseScopedToolsPrepareOnlyForPassthrough(t *testing.T) {
	for _, mode := range []string{"passthrough", "return"} {
		t.Run(mode, func(t *testing.T) {
			f := newNixPassthroughFixture(t)
			target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: `id: scoped-tools
execution: {packages: [uv:uv-probe==1]}
sequence:
  - id: work
    op: command_execution
    inputs: {run: uv-probe}
outputs:
  text: '${{ sequence.work.outputs.stdout }}'
`}
			c := Case{ID: "scoped-tools", Type: "recipe_case", Mocks: Mocks{Ops: []OpMock{{
				Match:    OpMockMatch{Op: "command_execution"},
				Behavior: MockBehavior{Mode: mode, Outputs: map[string]any{"stdout": "mock"}},
			}}}}
			result := RunCase(context.Background(), HarnessOptions{WorkRoot: filepath.Join(f.root, "work")}, "test", target, c, ExecutionOptions{})
			require.Equal(t, "passed", result.Status, result.FailureReason)
			require.Len(t, result.Diagnostics.Calls, 1)
			require.Len(t, result.Diagnostics.MockHits, 1)
			if mode == "passthrough" {
				require.Equal(t, "uv-probe==1", result.Outputs["text"])
				installs, err := os.ReadFile(f.installLog)
				require.NoError(t, err)
				require.Equal(t, "uv-probe==1\n", string(installs))
			} else {
				require.Equal(t, "mock", result.Outputs["text"])
				require.NoFileExists(t, f.installLog)
			}
		})
	}
}

func TestPassthroughSetupExcludesOpTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const opName = "passthrough_timeout_probe"
		invoked := 0
		op := coreops.NewActivityMappedOpV2[map[string]any, map[string]any](coreops.OpMetadata{Type: opName},
			func(_ coreops.OpDependencies, ctx context.Context, _ map[string]any) (map[string]any, error) {
				invoked++
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.Equal(t, time.Second, deadline.Sub(time.Now()), "op keeps its full budget after setup")
				return map[string]any{}, nil
			})
		withRegisteredCoreOps(t, op)
		deps := coreops.NewServiceDepsBuilder().Build()
		j := newTestJobContext("test", Case{Mocks: Mocks{Ops: []OpMock{{
			Match: OpMockMatch{Op: opName}, Behavior: MockBehavior{Mode: "passthrough"},
		}}}}, TestPolicy{}, deps)
		j.operationPaths = recipeTestOperationPaths(t.TempDir())
		setups := 0
		j.prepareTools = func(ctx context.Context, req workerops.ToolSetupRequest) workerops.ToolSetupResult {
			setups++
			require.NotEmpty(t, req.Scopes)
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Equal(t, time.Minute, deadline.Sub(time.Now()), "setup uses the enclosing budget, not the op budget")
			// synctest advances virtual time; this does not wait twenty seconds.
			time.Sleep(20 * time.Second)
			require.NoError(t, ctx.Err())
			return workerops.ToolSetupResult{Duration: 20 * time.Second}
		}
		rec := mustLoadRecipe(t, `id: setup-timeout
timeout: 1m
execution: {packages: [uv:probe==1]}
sequence:
  - id: work
    op: passthrough_timeout_probe
    timeout: 1s
`)
		_, _, err := compiler.ExecuteRecipe(workflow.Context{JobContext: j, ServiceDependencies2: deps}, *rec, nil,
			contextual.JobContext{Environment: recipeTestEnvironment(j.operationPaths)}, contextual.GitCommitContext{})
		require.NoError(t, err)
		require.Equal(t, 1, setups)
		require.Equal(t, 1, invoked)
		require.Len(t, j.calls, 1)
	})
}
