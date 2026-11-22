package runjob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/inputhistorytest"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestInputHistoryRunnerComposition(t *testing.T) {
	binary := os.Getenv("C2J_TEST_INPUT_HISTORY_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "c2j")
		command := exec.Command("go", "build", "-mod=readonly", "-o", binary, "../..")
		b, e := command.CombinedOutput()
		require.NoError(t, e, string(b))
	}
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
	defer cancel()
	db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "runner.db")})
	require.NoError(t, e)
	defer db.Close(context.Background())
	f, e := inputhistorytest.Build(ctx, db)
	require.NoError(t, e)
	engine, e := wf.NewEngineBuilder().WithRuntime(db).BuildEngine()
	require.NoError(t, e)
	body := fmt.Sprintf("id: input-history-runner\nsequence:\n  - id: inspect\n    op: command_execution\n    inputs:\n      run: '%s job inputs list %s --json'\noutputs:\n  retained: '${{ sequence.inspect.outputs.stdout }}'\n", binary, f.Job.JobId)
	rec, e := recipe.LoadRecipeFromString([]byte(body))
	require.NoError(t, e)
	jc, git := compiler.GenerateTestContext()
	celltest.Create(t, db, inputhistorytest.Tenant, "runner", celltest.Repo(jc.GitBase.BaseRepo))
	h, e := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: inputhistorytest.Tenant, RecipeName: "input-history-runner", JobContext: jc, GitRef: git.ParentRef, Cell: "runner", CellRepo: celltest.Repo(jc.GitBase.BaseRepo)}, jobdbschema.WorkflowEngine{Engine: engine, Registry: db}, *rec)
	require.NoError(t, e)
	server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	e = Run(ctx, Options{JobID: h.JobID, JobDBURI: server.URL + "/" + inputhistorytest.Tenant, InputMode: "fail", CI: true, Stdout: &stdout, Stderr: &stderr, Stdin: bytes.NewReader(nil)})
	require.NoError(t, e, stderr.String())
	run, e := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: h.Key(), IncludeOutputs: true})
	require.NoError(t, e)
	out, e := run.GetOutput(engine, inputhistorytest.Tenant)
	require.NoError(t, e)
	raw, e := out.GetData()
	require.NoError(t, e)
	var result struct {
		Retained string `json:"retained"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	page, e := jobinputs.DecodeResponse(bytes.NewBufferString(result.Retained), "list")
	require.NoError(t, e, string(raw))
	require.Contains(t, string(page.Page), f.Forms[0].RequestID)
}
