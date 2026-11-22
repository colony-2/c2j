package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Run real processes, save their recipe failures, then read them through the
// normal CLI in a fresh process. No final error is fabricated by this fixture.
func TestCompactFailureCLI(t *testing.T) {
	c2jops.Register()
	for _, op := range []string{"command", "extension"} {
		for _, scenario := range []string{"stderr", "stdout", "exit7", "catch"} {
			t.Run(op+"/"+scenario, func(t *testing.T) {
				root := t.TempDir()
				repo := filepath.Join(root, "repo")
				require.NoError(t, os.Mkdir(repo, 0700))
				runGit := func(args ...string) {
					c := exec.Command("git", args...)
					c.Dir = repo
					b, e := c.CombinedOutput()
					require.NoError(t, e, string(b))
				}
				runGit("init", "-b", "main")
				runGit("config", "user.email", "test@example.com")
				runGit("config", "user.name", "Test")
				exit := 1
				if scenario == "exit7" {
					exit = 7
				}
				text := "Required tool unavailable: go"
				stream := " >&2"
				if scenario == "stdout" {
					text = "Validation failed: missing package"
					stream = ""
				}
				counter := filepath.Join(root, "runs")
				script := fmt.Sprintf("printf 'run\\n' >> %q; printf '%%s\\n' '%s'%s; exit %d", counter, text, stream, exit)
				node := "   op: command_execution\n   inputs:\n     run: " + fmt.Sprintf("%q", script) + "\n"
				if op == "extension" {
					require.NoError(t, os.Mkdir(filepath.Join(repo, "op"), 0700))
					manifest := fmt.Sprintf("name: check\nshell: sh\nrun: %q\ninput_schema: {type: object}\noutput_schema: {type: object}\n", script)
					require.NoError(t, os.WriteFile(filepath.Join(repo, "op", "op.yaml"), []byte(manifest), 0600))
					node = "   op: ./op\n"
				}
				if scenario == "catch" {
					node += "   catch:\n    - when: true\n      fail: {message: custom failure}\n"
				}
				runGit("add", ".")
				runGit("commit", "--allow-empty", "-m", "fixture")
				yaml := "id: compact-cli\nversion: '1'\ninput_schema: {}\nsequence:\n - id: tools\n" + node + "outputs: {}\n"
				ctx := celltest.Context(context.Background())
				db, e := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(root, "jobs.db")})
				require.NoError(t, e)
				defer db.Close(ctx)
				cellRepo := celltest.Repo(repo)
				celltest.Create(t, db, "test", "source", cellRepo)
				start := workflowctl.StartJob{TenantId: "test", RecipeName: "compact-cli", JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}, GitRef: "main", Cell: "source", CellRepo: cellRepo}
				data := jobdb.NewTaskDataOrPanic(start, jobdb.NewArtifactFromBytes("compact-cli"+starter.RecipeArtifactSuffix, []byte(yaml)))
				h, e := db.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: "recipe", Data: data, Cell: "source", CellRepo: cellRepo, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
				require.NoError(t, e)
				reg, e := workerops.NewActivityRegistry()
				require.NoError(t, e)
				ws, e := compiler.NewRecipeWorker(ops.NewServiceDepsBuilder().Build(), reg)
				require.NoError(t, e)
				guarded := executionruntime.New(db, execution.Allocation{SchemaVersion: 1})
				guarded.Issuer = permissions.JobDBIssuer{}
				tasks := make([]jobworkflow.TaskWorker, 0, len(ws.TaskWorkers))
				for _, w := range ws.TaskWorkers {
					tasks = append(tasks, guarded.WrapTaskWorker(w))
				}
				r, e := jobworkflow.GetJobForRun(ctx, guarded, jobworkflow.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, e)
				outcome, e := r.Run(nil)
				require.NoError(t, e)
				require.Equal(t, jobworkflow.JobRunFailed, outcome.Status)
				want := fmt.Sprintf("%s [exit %d]", text, exit)
				if scenario != "catch" {
					message, ok := recipe.CompactErrorMessage(outcome.JobError)
					require.True(t, ok, "%v", outcome.JobError)
					require.Equal(t, want, message)
				}
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
				defer server.Close()
				for attempt := 0; attempt < 2; attempt++ {
					// Replay diagnostics do not need a health listener. Avoid the
					// default host port, which may belong to the verification worker.
					command := exec.Command(os.Args[0], "-test.run=^TestLeaseDiagnosticProcess$", "--", "run", "--job-id", h.JobKey.JobId, "--jobdb", server.URL+"/test", "--ci", "--health-port", "0")
					command.Env = append(os.Environ(), "C2J_TEST_LEASE_PROCESS=1")
					command.Dir = root
					var stdout, stderr bytes.Buffer
					command.Stdout = &stdout
					command.Stderr = &stderr
					err := command.Run()
					var exitErr *exec.ExitError
					require.ErrorAs(t, err, &exitErr)
					require.Equal(t, 1, exitErr.ExitCode())
					if attempt == 0 {
						t.Logf("Observed CLI stderr:\n%sObserved CLI exit: %d", stderr.String(), exitErr.ExitCode())
					}
					if scenario == "catch" {
						require.Equal(t, "warning: replay unavailable: [compact-cli.tools] custom failure\nError: [compact-cli.tools] custom failure\n", stderr.String())
					} else {
						require.Equal(t, want+"\n", stderr.String())
					}
				}
				runs, e := os.ReadFile(counter)
				require.NoError(t, e)
				require.Equal(t, "run\n", string(runs))
				history, e := jobdb.GetJobRun(ctx, db, jobdb.GetJobRunRequest{JobKey: h.JobKey, IncludeOutputs: true})
				require.NoError(t, e)
				raw, e := json.Marshal(history)
				require.NoError(t, e)
				require.Contains(t, string(raw), "output_exit_v1")
				require.NotContains(t, want, "execution log")
				require.False(t, strings.Contains(want, "Error:"))
			})
		}
	}
}
