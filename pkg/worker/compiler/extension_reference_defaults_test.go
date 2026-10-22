package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type extensionDefaultProvider struct {
	calls  int
	values map[string]jobdb.SecretValue
	err    error
}

func (p *extensionDefaultProvider) ResolveSecrets(_ context.Context, req jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	p.calls++
	if req.TenantID != "extension-defaults" || req.JobID == "" {
		return jobdb.SecretResolveResult{}, fmt.Errorf("missing job identity")
	}
	return jobdb.SecretResolveResult{Secrets: p.values}, p.err
}

func TestExtensionReferenceDefaultsThroughWorkerAndReplay(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	for _, tc := range []struct {
		name, inputs, secret, config, want, wantErr string
		noProvider, outage                          bool
		calls                                       int
	}{
		{name: "both", secret: "private-credential", config: `"https://example.test"`, want: `{"api":{"api_key":"private-credential","base_url":"https://example.test"}}`, calls: 1},
		{name: "missing", want: `{"api":{"base_url":null}}`, calls: 1},
		{name: "url only", config: `"https://example.test"`, want: `{"api":{"base_url":"https://example.test"}}`, calls: 1},
		{name: "key only", secret: "private-credential", want: `{"api":{"api_key":"private-credential","base_url":null}}`, calls: 1},
		{name: "null opt out", inputs: `inputs: {api: null}`, want: `{"api":null}`, noProvider: true},
		{name: "partial override", inputs: `inputs: {api: {base_url: override}}`, want: `{"api":{"base_url":"override"}}`, calls: 1},
		{name: "explicit recipe bindings", inputs: `inputs:
  api:
    base_url: '${{ context.config.?base_url.orValue(null) }}'
    api_key: '${{ context.secrets.?api_key.orValue(null) }}'`, secret: "private-credential", config: `"https://example.test"`, want: `{"api":{"api_key":"private-credential","base_url":"https://example.test"}}`, calls: 1},
		{name: "caller secret no fallback", inputs: `inputs:
  api:
    api_key: '${{ context.secrets.caller }}'`, secret: "private-credential", wantErr: "secret resolution unavailable", calls: 1},
		{name: "no provider", noProvider: true, wantErr: "no secret provider configured"},
		{name: "outage", outage: true, wantErr: "secret resolution unavailable", calls: 1},
		{name: "invalid config", config: `false`, wantErr: "failed to validate selector inputs"},
		{name: "stderr redaction", secret: "private-credential", wantErr: "extension op", calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			marker := filepath.Join(root, "executed")
			t.Setenv("C2J_DEFAULT_EXPECTED", tc.want)
			// Compare actual stdin, not task metadata. The child emits no secret output
			// on success; the failure case deliberately includes it in stderr.
			script := fmt.Sprintf("IFS= read -r payload\nprintf ran > %q\n[ \"$payload\" = \"$C2J_DEFAULT_EXPECTED\" ] || exit 9\nprintf '{\"output\":{\"ok\":true}}'", marker)
			if tc.name == "stderr redaction" {
				script = fmt.Sprintf("IFS= read -r payload\nprintf ran > %q\nprintf '%%s' \"$payload\" >&2\nexit 9", marker)
			}
			manifest := `name: reference-defaults
input_schema:
  type: object
  properties:
    api:
      type: [object, "null"]
      additionalProperties: false
      properties:
        base_url:
          type: [string, "null"]
          default: '${{ context.config.?base_url.orValue(null) }}'
        api_key:
          type: string
          x-secret: true
          minLength: 1
          default: '${{ context.secrets.?api_key.orValue(null) }}'
output_schema: {type: object}
run: ` + fmt.Sprintf("%q\n", script)
			require.NoError(t, os.Mkdir(filepath.Join(root, "op"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "op", "op.yaml"), []byte(manifest), 0600))
			require.NoError(t, runGit(root, "git", "init", "--quiet"))
			require.NoError(t, runGit(root, "git", "add", "op"))
			require.NoError(t, runGit(root, "git", "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--quiet", "-m", "test manifest"))
			provider := &extensionDefaultProvider{values: map[string]jobdb.SecretValue{}}
			if tc.secret != "" {
				provider.values["api_key"] = jobdb.SecretValue{Value: tc.secret}
			}
			if tc.outage {
				provider.err = fmt.Errorf("outage")
			}
			rt := toyruntime.New(toyruntime.WithSecretProvider(provider))
			if tc.noProvider {
				rt = toyruntime.New()
			}
			if tc.config != "" {
				_, err := rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "extension-defaults", Name: "base_url", Value: json.RawMessage(tc.config)})
				require.NoError(t, err)
			}
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
			require.NoError(t, err)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("extension-defaults").PlusWorkers(ws.JobWorker).BuildEngine()
			require.NoError(t, err)
			engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}
			rec, err := recipe.LoadRecipeFromString([]byte("id: reference-defaults\nop: git+file://" + filepath.ToSlash(root) + "//op@HEAD\n" + tc.inputs + "\n"))
			require.NoError(t, err)
			job, git := GenerateTestContext()
			job.RecipeSource.Repo = root
			job.RecipeSource.Ref = "HEAD"
			key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "extension-defaults", RecipeName: "reference-defaults", JobContext: job, GitRef: git.ParentRef}, engine, *rec)
			require.NoError(t, err)
			require.Zero(t, provider.calls, "submission must not retrieve defaults")
			var logs secretTestLogBuffer
			previous := slog.Default()
			previousWriter, previousFlags := log.Writer(), log.Flags()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			defer func() { slog.SetDefault(previous); log.SetOutput(previousWriter); log.SetFlags(previousFlags) }()
			var workers []jobworkflow.TaskWorker
			for _, w := range ws.TaskWorkers {
				workers = append(workers, w)
			}
			runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: workers, WorkerID: "test", LeaseDuration: time.Minute})
			require.NoError(t, err)
			_, err = runnable.Run(nil)
			require.NoError(t, err)
			_, err = swfutil.JobResult(ctx, engine, key)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tc.wantErr != "" && tc.calls > 0 {
				require.GreaterOrEqual(t, provider.calls, tc.calls) // Failed task attempts may retry.
			} else {
				require.Equal(t, tc.calls, provider.calls)
			}
			if tc.wantErr == "" || tc.name == "stderr redaction" {
				require.FileExists(t, marker)
			} else {
				require.NoFileExists(t, marker)
			}
			history, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			raw, err := json.Marshal(history)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-credential")
			require.NotContains(t, logs.String(), "private-credential")
			if tc.name == "stderr redaction" {
				require.Contains(t, logs.String(), "[REDACTED]")
			}
			if tc.wantErr == "" {
				require.Contains(t, string(raw), "context.secrets.?api_key", "durable manifest retains the reference expression")
				require.NoError(t, os.RemoveAll(root))
				provider.err = fmt.Errorf("replay must not access provider")
				_, err := rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "extension-defaults", Name: "base_url", Value: json.RawMessage(`false`)})
				require.NoError(t, err)
				_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				require.Equal(t, tc.calls, provider.calls, "completed replay must not read secrets")
			}
		})
	}
}
