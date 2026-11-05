package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/stretchr/testify/require"
)

func TestExecutionRequiredContract(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"execution_required":{}}`,
		`{"execution_required":{"base":null}}`, `{"execution_required":{"base":""}}`,
		`{"execution_required":{"base":"mac*","base":"linux*"}}`,
		`{"execution_required":{"base":"mac*"}} {}`,
		`{"execution_required":{"base":"mac*"},"output":{}}`,
		`{"execution_required":{"base":"mac*"},"artifact_refs":{}}`,
		`{"execution_required":{"packages":["uv:tool"]}}`,
		`{"execution_required":{"image":"x"}}`, `{"execution_required":{"platform":"linux/amd64"}}`,
		`{"execution_required":{"resources":{"memory":"-1Gi"}}}`,
		`{"execution_required":{"resources":{"unknown":"1"}}}`,
		`{"execution_required":{"capabilities":["Docker"]}}`,
	} {
		_, err := decodeExecutionRequired([]byte(raw))
		require.Error(t, err, raw)
	}
	r, err := decodeExecutionRequired([]byte(`{"execution_required":{"base":"macos*","resources":{"cpu":"4000m","memory":"8Gi"},"capabilities":["network","network"]}}`))
	require.NoError(t, err)
	require.Equal(t, "macos*", *r.Base)
	require.Equal(t, []string{"network"}, r.Capabilities)
}

func invokeNegotiatingExtension(t *testing.T, ctx context.Context, run string, env map[string]string) (map[string]any, error) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "op"), 0700))
	manifest := fmt.Sprintf("name: test\nshell: sh\nrun: |\n  %s\nenv:\n  C2J_EXECUTION_ALLOCATION: manifest-forgery\ninput_schema: {type: object}\noutput_schema: {type: object, required: [ready]}\n", run)
	require.NoError(t, os.WriteFile(filepath.Join(root, "op", "op.yaml"), []byte(manifest), 0600))
	deps := ops.NewOpDependenciesBuilder().WithWorktreePath(root).Build()
	return executeExtension(deps, ctx, ExecutionInput{Selector: "./op", Env: env})
}

func TestExecutionRequiredExitSemantics(t *testing.T) {
	ctx := execution.WithEnvironment(context.Background(), execution.Allocation{SchemaVersion: 2, Base: "ubuntu-26"})
	for _, tc := range []struct {
		name, response string
		code           int
		want           string
		request        bool
	}{
		{"handoff", `{"execution_required":{"base":"macos*","capabilities":["network"]}}`, 75, "", true},
		{"capability", `{"execution_required":{"capabilities":["docker"]}}`, 75, "", true},
		{"satisfied", `{"execution_required":{"base":"ubuntu*"}}`, 75, "already satisfied", false},
		{"wildcard", `{"execution_required":{"base":"*"}}`, 75, "already satisfied", false},
		{"invalid", `{"execution_required":{"packages":["uv:tool"]}}`, 75, "invalid extension execution_required", false},
		{"ordinary_failure", `{"execution_required":{"base":"macos*"}}`, 7, "failed", false},
		{"ordinary_success", `{"output":{"ready":true,"execution_required":{"base":"macos*"}}}`, 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := invokeNegotiatingExtension(t, ctx, fmt.Sprintf("printf '%%s' '%s'; exit %d", tc.response, tc.code), nil)
			if tc.request {
				require.True(t, execution.IsRequired(err))
				require.Nil(t, out)
			} else if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				require.False(t, execution.IsRequired(err))
			} else {
				require.NoError(t, err)
				require.Equal(t, true, out["ready"])
			}
		})
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err := invokeNegotiatingExtension(t, ctx, `printf '{"execution_required":{"base":"macos*"}}'; sleep 1; exit 75`, nil)
	require.Error(t, err)
	require.False(t, execution.IsRequired(err))
}

func TestExtensionAllocationIncludesAllPreparedPackagesAndCannotBeOverridden(t *testing.T) {
	t.Setenv(execution.AllocationEnv, "inherited-forgery")
	ctx := execution.WithEnvironment(context.Background(), execution.Allocation{SchemaVersion: 2, Base: "macos-123", Capabilities: []string{"network"}})
	ctx = (toolenv.Environment{Tools: []toolenv.Tool{{Reference: "uv:outer"}, {Reference: "pnpm:repo"}}}).Bind(ctx)
	ctx = (toolenv.Environment{Tools: []toolenv.Tool{{Reference: "uv:outer"}, {Reference: "nix:nixpkgs#inner"}}}).Bind(ctx)
	out, err := invokeNegotiatingExtension(t, ctx, `printf '{"output":{"ready":true,"env":%s}}' "$C2J_EXECUTION_ALLOCATION"`, map[string]string{execution.AllocationEnv: "invocation-forgery"})
	require.NoError(t, err)
	raw, err := json.Marshal(out["env"])
	require.NoError(t, err)
	var env execution.Environment
	require.NoError(t, json.Unmarshal(raw, &env))
	require.Equal(t, "macos-123", env.Base)
	require.Equal(t, []string{"network"}, env.Capabilities)
	require.Equal(t, []string{"uv:outer", "pnpm:repo", "nix:nixpkgs#inner"}, env.Packages)
}
