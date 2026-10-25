package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestRuntimeExtensionSecretAndConfigurationDefaults(t *testing.T) {
	withRegisteredCoreOps(t, append(recipeops.GetOps(), extops.GetExecutionOp())...)
	root := t.TempDir()
	marker := filepath.Join(root, "model-ran")
	for _, name := range []string{"optional", "required"} {
		binding := "${{ context.secrets.?api_key.orValue(null) }}"
		if name == "required" {
			binding = "${{ context.secrets.api_key }}"
		}
		manifest := fmt.Sprintf(`name: defaults
input_schema:
  type: object
  properties:
    api_key:
      type: string
      x-secret: true
      default: %q
    base_url:
      type: [string, "null"]
      default: '${{ context.config.?base_url.orValue(null) }}'
output_schema: {type: object}
run: %q
`, binding, "touch "+marker+"; exit 99")
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, name, "op.yaml"), []byte(manifest), 0600))
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "-m", "fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		b, e := cmd.CombinedOutput()
		require.NoError(t, e, string(b))
	}
	// Run a populated case before empty cases to check per-case isolation.
	for _, tc := range []struct {
		name, op, wantErr string
		present, child    bool
	}{
		{name: "present", op: "optional", present: true},
		{name: "missing_optional", op: "optional"},
		{name: "missing_in_child", op: "optional", child: true},
		{name: "present_in_child", op: "required", present: true, child: true},
		{name: "missing_required", op: "required", wantErr: "required secret unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector := "git+file://" + filepath.ToSlash(root) + "//" + tc.op + "@HEAD"
			yaml := "id: probe\nperms: {jobdb: {secrets: {resolve: [api_key]}}}\nop: " + selector + "\n"
			runtime := &RuntimeCase{}
			if tc.present {
				runtime.Secrets = map[string]string{"api_key": "fixture-private-value"}
				runtime.Config = map[string]any{"base_url": "https://fixture.invalid"}
			}
			if tc.child {
				runtime.Cells = map[string]CellFixture{"root": {Files: map[string]string{".c2/recipes/child.yaml": yaml}}}
				yaml = "id: parent\nop: recipe.run_and_get_result\ninputs: {name: child, cell_name: root, git_ref: main}\n"
			}
			c := Case{ID: tc.name, Type: "integration_case", Runtime: runtime, Mocks: Mocks{Ops: []OpMock{{Repeat: true, Match: OpMockMatch{Selector: selector}, Behavior: MockBehavior{Mode: "return", Outputs: map[string]any{"ok": true}}}}}}
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "runtime-secrets", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: yaml}, c, ExecutionOptions{})
			require.NoFileExists(t, marker, "a mocked model must never execute")
			if tc.wantErr != "" {
				require.Equal(t, "failed", result.Status)
				require.Contains(t, result.FailureReason, tc.wantErr)
				return
			}
			require.Equal(t, "passed", result.Status, result.FailureReason)
			count := 0
			for _, call := range result.Runtime.Calls {
				if call.Op != "extension_execution" {
					continue
				}
				count++
				inputs := call.Inputs["inputs"].(map[string]any)
				if tc.present {
					require.Equal(t, "api_key", inputs["api_key"].(map[string]any)["$c2j_secret"])
					require.Equal(t, "https://fixture.invalid", inputs["base_url"])
				} else {
					require.Equal(t, true, inputs["api_key"].(map[string]any)["omit_if_unavailable"])
					require.Contains(t, inputs, "base_url")
					require.Nil(t, inputs["base_url"])
				}
			}
			require.Equal(t, 1, count, "the mock must be reached after resolving defaults")
			raw, e := json.Marshal(result)
			require.NoError(t, e)
			require.NotContains(t, string(raw), "fixture-private-value")
		})
	}
}

func TestRuntimeSecretProviderIsIsolated(t *testing.T) {
	p := runtimeSecretProvider{tenant: "fixture", values: map[string]string{"key": "value", "other": "unused"}}
	got, err := p.ResolveSecrets(context.Background(), jobdb.SecretResolveRequest{TenantID: "fixture", Names: []string{"key", "missing"}})
	require.NoError(t, err)
	require.Equal(t, map[string]jobdb.SecretValue{"key": {Value: "value"}}, got.Secrets)
	_, err = p.ResolveSecrets(context.Background(), jobdb.SecretResolveRequest{TenantID: "other", Names: []string{"key"}})
	require.ErrorIs(t, err, jobdb.ErrSecretResolutionFailed)
}

func TestRuntimeNativeSecretFixturesAreMaterializedButNotReported(t *testing.T) {
	type input struct {
		Token string `json:"token" secret:"true" default:"${{ context.secrets.?api_key.orValue(null) }}"`
	}
	received := []string{}
	op := coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "secret_fixture_probe"}, func(_ coreops.OpDependencies, _ context.Context, in input) (map[string]any, error) {
		received = append(received, in.Token)
		return map[string]any{"ok": true}, nil
	})
	withRegisteredCoreOps(t, op)
	for _, value := range []string{"synthetic-credential", ""} {
		runtime := &RuntimeCase{}
		if value != "" {
			runtime.Secrets = map[string]string{"api_key": value}
		}
		result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "fixture", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: test\nperms: {jobdb: {secrets: {resolve: [api_key]}}}\nop: secret_fixture_probe\n"}, Case{ID: "native-secret", Type: "integration_case", Runtime: runtime}, ExecutionOptions{})
		require.Equal(t, "passed", result.Status, result.FailureReason)
		require.Equal(t, value, received[len(received)-1])
		require.Len(t, result.Runtime.Calls, 1)
		if value != "" {
			require.Equal(t, "[REDACTED]", result.Runtime.Calls[0].Inputs["token"])
		} else {
			require.NotContains(t, result.Runtime.Calls[0].Inputs, "token")
		}
		raw, err := json.Marshal(result)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "synthetic-credential")
	}
	require.Equal(t, []string{"synthetic-credential", ""}, received)
}
