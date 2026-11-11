package recipetest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/secrets"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/colony-2/c2j/internal/dispatchtest"
	"github.com/colony-2/c2j/internal/preparedtest"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestPreparedObjectCompletionConfinement(t *testing.T) {
	for _, mode := range []string{"escaping part", "symlink parent", "replaced root"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			spec := PreparedBarrier{Stdout: `{"output":{"session":{"$object":"next"}}}`, Objects: map[string]ObjectFixture{"next": {Type: "neutral/v1", Files: map[string]string{"checkpoint": "retained"}}}}
			switch mode {
			case "escaping part":
				spec.Objects["next"] = ObjectFixture{Type: "neutral/v1", Files: map[string]string{"../../escape": "bad"}}
			case "symlink parent":
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "next")))
			case "replaced root":
				require.NoError(t, os.Remove(root))
				require.NoError(t, os.Symlink(outside, root))
			}
			_, err := preparedObjectCompletion(preparedtest.Invocation{Env: map[string]string{"C2J_OBJECT_OUTBOX": root}}, spec)
			require.Error(t, err)
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestPreparedMissingEvidenceFails(t *testing.T) {
	_, err := capturePreparedEvidence(context.Background(), ops.NewOpDependenciesBuilder().Build(), preparedtest.Invocation{}, []string{"no-such-neutral-tool-6f4d"}, 1024)
	require.ErrorContains(t, err, "prepared tool probe")
}

func TestDispatchAndCompletionContextsRemainIsolated(t *testing.T) {
	var wg sync.WaitGroup
	for _, id := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := controlCase()
			c.Runtime.Controls.Dispatch = nil
			f := &runtimeFixture{c: c}
			f.initControls()
			ctx := dispatchtest.WithObserver(context.Background(), f.beginDispatch)
			ctx = dispatchtest.WithIdentity(ctx, dispatchtest.Attempt{JobID: id, InvocationID: id, NodePath: id, Attempt: 1})
			access := permissions.NewAccess(ctx, permissions.JobDB{}, nil, nil)
			_, err := access.Submit(ctx, jobdb.SubmitJob{Cell: id})
			require.Error(t, err)
			require.Len(t, f.report.Controls.Dispatch, 1)
			require.Equal(t, id, f.report.Controls.Dispatch[0].JobID)
			ctx = preparedtest.WithCompletion(ctx, func(context.Context, preparedtest.Invocation) ([]byte, bool, error) { return []byte(id), true, nil })
			stdout, selected, err := preparedtest.Complete(ctx, preparedtest.Invocation{})
			require.NoError(t, err)
			require.True(t, selected)
			require.Equal(t, id, string(stdout))
			raw, err := json.Marshal(f.report)
			require.NoError(t, err)
			require.NotEmpty(t, raw)
		}()
	}
	wg.Wait()
	_, selected, err := preparedtest.Complete(context.Background(), preparedtest.Invocation{})
	require.NoError(t, err)
	require.False(t, selected)
	finish, err := dispatchtest.Begin(context.Background(), dispatchtest.Attempt{})
	require.NoError(t, err)
	finish(nil)
}

func TestPreparedContentIsRedactedBeforeEncoding(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "checkpoint")
	require.NoError(t, os.WriteFile(path, []byte("private-neutral-secret"), 0600))
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	require.NoError(t, redactor.Register("private-neutral-secret"))
	ctx := secrets.WithRedactor(context.Background(), redactor)
	evidence, err := capturePreparedEvidence(ctx, ops.NewOpDependenciesBuilder().Build(), preparedtest.Invocation{Inputs: map[string]any{"session": &objects.Snapshot{Files: map[string]string{"checkpoint": path}}}}, nil, 1024)
	require.NoError(t, err)
	content := evidence.Objects["session"]["checkpoint"]
	require.True(t, content.Redacted)
	data, err := base64.StdEncoding.DecodeString(content.ContentBase64)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-neutral-secret")
	require.Equal(t, contentDigest([]byte("private-neutral-secret")), content.SHA256)
}
