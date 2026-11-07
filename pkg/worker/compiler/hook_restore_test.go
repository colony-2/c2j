package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/permissions"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type hookRestoreControl struct {
	workflowctl.WorkflowControl
	calls    int
	tenant   string
	key      jobdb.ArtifactKey
	artifact jobdb.Artifact
}

func (c *hookRestoreControl) GetArtifactLazy(_ context.Context, tenant string, key jobdb.ArtifactKey) jobdb.Artifact {
	c.calls++
	c.tenant = tenant
	c.key = key
	return c.artifact
}

type unavailableHookArtifactGetter struct{ calls int }

func (g *unavailableHookArtifactGetter) GetArtifact(tenant string, key jobdb.ArtifactKey) (jobdb.Artifact, error) {
	g.calls++
	return nil, fmt.Errorf("restore bytes unavailable for tenant %s key %+v", tenant, key)
}

func TestHookManagedRestoreExactKey(t *testing.T) {
	ctx := celltest.Context(context.Background())
	repo := filepath.Join(t.TempDir(), "base")
	base := workspaceRepo(t, repo, "A")
	g := gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: "main", ResolvedBaseHash: base, ParentHash: base, PersistHash: base}
	task := &gitstate.GitTaskContext{GlobalGitTaskContext: &g, WorktreePath: filepath.Join(t.TempDir(), "candidate")}
	controller := gitstate.NewController(nil)
	require.NoError(t, controller.Restore(ctx, task, nil))
	require.NoError(t, os.WriteFile(filepath.Join(task.WorktreePath, "candidate-only"), []byte("candidate"), 0644))
	_, pack, err := controller.Persist(ctx, task)
	require.NoError(t, err)
	require.NotNil(t, pack)
	defer pack.Cleanup()
	bytes, err := pack.Bytes(ctx)
	require.NoError(t, err)
	key := jobdb.ArtifactKey{JobId: "submitted-child", TaskOrdinal: 0, Name: "__c2j_submit_workspace__", SizeBytes: int64(len(bytes))}
	exact := jobdb.NewArtifactFromBytes(key.Name, bytes)
	jobdb.AssignArtifactKey(exact, key)
	decoyKey := key
	decoyKey.JobId = "foreign-child"
	decoy := jobdb.NewArtifactFromBytes(key.Name, []byte("foreign-only"))
	jobdb.AssignArtifactKey(decoy, decoyKey)
	identity, err := hookpolicy.Identity(repo)
	require.NoError(t, err)
	req := workerops.ActivityInvocationRequest{WorkspaceManaged: true, RestoreArtifact: &key, GitTaskContext: g, Input: map[string]any{"repository": identity, "hook": ".c2/hooks/check.yaml", "phase": "pin", "source_hooks": "from_current"}}
	tc := jobworkflow.TaskContext{JobKey: jobdb.JobKey{TenantId: "current-tenant", JobId: "current-child"}}
	guarded := permissions.WithAccess(ctx, permissions.NewAccess(ctx, permissions.JobDB{}, nil, nil))
	for _, name := range []string{"exact", "exact-without-control", "lookup", "decoy", "duplicate", "missing-control", "unavailable-bytes", "decoy-unavailable"} {
		t.Run(name, func(t *testing.T) {
			control := &hookRestoreControl{artifact: exact}
			getter := &unavailableHookArtifactGetter{}
			w := hookResolveWorker{control: control}
			var arts []jobdb.Artifact
			switch name {
			case "exact":
				arts = []jobdb.Artifact{decoy, exact}
			case "exact-without-control":
				arts = []jobdb.Artifact{exact}
				w.control = nil
			case "decoy":
				arts = []jobdb.Artifact{decoy}
			case "duplicate":
				arts = []jobdb.Artifact{exact, exact}
			case "missing-control":
				w.control = nil
			case "unavailable-bytes", "decoy-unavailable":
				// Real lazy materialization fails only when Git restore asks for bytes.
				control.artifact = key.ToLazyArtifact(getter, "current-tenant")
				if name == "decoy-unavailable" {
					arts = []jobdb.Artifact{decoy}
				}
			}
			out, err := w.RunTask(guarded, tc, jobdb.NewTaskDataOrPanic(req, arts...))
			switch name {
			case "duplicate":
				require.ErrorContains(t, err, "multiple restore artifacts")
			case "missing-control":
				require.ErrorContains(t, err, "workflow control required for hook workspace snapshot")
			case "unavailable-bytes", "decoy-unavailable":
				require.Error(t, err)
				require.Contains(t, err.Error(), "restore bytes unavailable")
				require.Equal(t, 1, getter.calls)
			default:
				require.NoError(t, err)
				decoded, err := decodeTaskOutput(out.GetDataOrPanic())
				require.NoError(t, err)
				raw, err := json.Marshal(decoded.Activity.OpOutput)
				require.NoError(t, err)
				var result hookResolution
				require.NoError(t, json.Unmarshal(raw, &result))
				require.Equal(t, g.PersistHash, result.Candidate)
				require.NotEqual(t, base, result.Candidate)
				returned, err := out.GetArtifacts()
				require.NoError(t, err)
				require.Len(t, returned, 1)
				got, err := returned[0].ArtifactKey()
				require.NoError(t, err)
				require.Equal(t, key, got)
			}
			want := 0
			switch name {
			case "lookup", "decoy", "unavailable-bytes", "decoy-unavailable":
				want = 1
			}
			require.Equal(t, want, control.calls)
			if want != 0 {
				require.Equal(t, "current-tenant", control.tenant)
				require.Equal(t, key, control.key)
			}
			t.Logf("case=%s lookup_count=%d tenant=%s key=%+v error=%v", name, control.calls, control.tenant, control.key, err)
		})
	}
	_, err = (hookResolveWorker{}).RunTask(ctx, tc, jobdb.NewTaskDataOrPanic(req, exact))
	require.ErrorContains(t, err, "guarded task worker")
	t.Logf("base=%s candidate=%s", base, g.PersistHash)
}
