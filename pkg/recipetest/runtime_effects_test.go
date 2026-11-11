package recipetest

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type effectArtifactRuntime struct {
	jobdb.WorkflowRuntime
	data string
	fail bool
}

func (r effectArtifactRuntime) OpenArtifact(context.Context, jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	if r.fail {
		return nil, errors.New("artifact access denied")
	}
	return effectReader{r.data}, nil
}

type effectReader struct{ data string }

func (r effectReader) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(r.data)), nil
}
func (r effectReader) Size() int64  { return int64(len(r.data)) }
func (r effectReader) Name() string { return "checkpoint" }
func TestEffectArtifactEvidenceFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rt    effectArtifactRuntime
		size  int64
		limit int
	}{{"inaccessible", effectArtifactRuntime{fail: true}, 4, 100}, {"truncated", effectArtifactRuntime{data: "abc"}, 4, 100}, {"oversized", effectArtifactRuntime{data: "abcde"}, 5, 4}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readEffectArtifact(context.Background(), tc.rt, "native", jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 1, Name: "checkpoint", SizeBytes: tc.size}, tc.limit)
			require.Error(t, err)
		})
	}
}

type deniedChildListing struct{ jobdb.WorkflowRuntime }

func (r deniedChildListing) ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return jobdb.ListJobsResponse{}, errors.New("child listing denied")
}

func TestEffectSnapshotsConfinementCompletenessAndRedaction(t *testing.T) {
	for _, mode := range []string{"complete", "missing repository", "symlink file", "symlink parent", "missing selected file", "denied children"} {
		t.Run(mode, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			c := controlCase()
			c.Runtime.Secrets = map[string]string{"key": "synthetic-secret-value"}
			c.Runtime.Cells["root"] = CellFixture{Files: map[string]string{"candidate.txt": "synthetic-secret-value"}}
			f := &runtimeFixture{c: c, root: t.TempDir(), cells: map[string]string{}}
			f.initControls()
			require.NoError(t, f.seedCells(ctx, "root"))
			rt, closeRuntime, err := newCaseRuntime(ctx, t.TempDir(), "native", c.Runtime)
			require.NoError(t, err)
			defer closeRuntime()
			_, err = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "native", Name: "root", Repo: "example.test/root"})
			require.NoError(t, err)
			handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "native", Cell: "root", JobType: "retained", Data: jobdb.NewTaskDataOrPanic(map[string]any{})}})
			require.NoError(t, err)

			if mode == "complete" {
				leases, err := rt.PollWork(ctx, jobdb.PollWorkRequest{TenantId: "native", WorkerID: "seed", Routes: []jobdb.Route{{JobType: "retained"}}, Limit: 1})
				require.NoError(t, err)
				require.Len(t, leases, 1)
				child, err := leases[0].SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "native", Cell: "root", JobType: "unexecuted-child", Data: jobdb.NewTaskDataOrPanic(map[string]any{})}})
				require.NoError(t, err)
				baseline := f.snapshotEffects(ctx, rt, handle.JobKey)
				require.True(t, baseline.Complete, baseline.Errors)
				require.Len(t, baseline.Children, 1)
				require.Equal(t, child.JobKey.JobId, baseline.Children[0].JobID)
				require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: handle.JobKey, Reason: "snapshot preservation"}))
				final := f.snapshotEffects(ctx, rt, handle.JobKey)
				require.True(t, final.Complete, final.Errors)
				require.Equal(t, baseline.Children, final.Children)
			}
			var runtime jobdb.WorkflowRuntime = rt
			root := f.cells["root"]
			switch mode {
			case "missing repository":
				require.NoError(t, os.RemoveAll(root))
			case "symlink file":
				require.NoError(t, os.Remove(filepath.Join(root, "candidate.txt")))
				require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(root, "candidate.txt")))
			case "symlink parent":
				outside := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(outside, "candidate.txt"), []byte("private"), 0600))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
				c.Runtime.Controls.Snapshot.Files = []string{"escape/candidate.txt"}
			case "missing selected file":
				c.Runtime.Controls.Snapshot.Files = []string{"absent.txt"}
			case "denied children":
				runtime = deniedChildListing{rt}
			}
			snapshot := f.snapshotEffects(ctx, runtime, handle.JobKey)
			if mode == "complete" || mode == "missing selected file" {
				require.True(t, snapshot.Complete, snapshot.Errors)
				if mode == "complete" {
					content := snapshot.Repositories["fixture:root"].Files["candidate.txt"].Content
					require.True(t, content.Redacted)
					data, err := base64.StdEncoding.DecodeString(content.ContentBase64)
					require.NoError(t, err)
					require.NotContains(t, string(data), "synthetic-secret-value")
					require.Equal(t, contentDigest([]byte("synthetic-secret-value")), content.SHA256)
				} else {
					require.False(t, snapshot.Repositories["fixture:root"].Files["absent.txt"].Exists)
				}
			} else {
				require.False(t, snapshot.Complete)
				require.NotEmpty(t, snapshot.Errors)
			}
		})
	}
}
