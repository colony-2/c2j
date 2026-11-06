package jobinspect

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestLatestSnapshotReportsUnrecordedCheckout(t *testing.T) {
	a := &attempt{task: Task{ID: "old-task", Attempt: 1, Status: "succeeded"}, order: 1}
	a.in.Git.BaseRepo = "https://example.test/old.git"
	a.in.Git.BaseRef = "main"
	h := &history{attempts: map[string][]*attempt{"old-task": {a}}}
	snap, err := (&Service{}).snapshot(context.Background(), h, Request{JobID: "job"}, nil)
	require.NoError(t, err)
	require.Len(t, snap.Workspaces, 1)
	require.Equal(t, "unavailable", snap.Workspaces[0].Availability)
	dir := filepath.Join(t.TempDir(), "export")
	_, err = Export(context.Background(), exportAPI{snapshot: snap}, Request{JobID: "job"}, dir)
	require.ErrorContains(t, err, "exact checkout was not recorded")
	require.NoDirExists(t, dir)
}

func TestSnapshotBoundariesKeepWorkspacesAndFailuresSeparate(t *testing.T) {
	rootBase := strings.Repeat("a", 40)
	foreignBase := strings.Repeat("b", 40)
	root := &attempt{task: Task{ID: "root-task", Attempt: 1, Status: "succeeded"}, order: 1, activity: activity{WorkspaceAfter: &coretask.WorkspaceState{Repository: "https://example.test/root.git", Cell: "root", ScopeID: "root", BaseCommit: rootBase, HeadCommit: rootBase}}}
	foreign := &attempt{task: Task{ID: "foreign-task", Attempt: 1, Status: "succeeded"}, order: 2, activity: activity{WorkspaceAfter: &coretask.WorkspaceState{Repository: "https://example.test/foreign.git", Cell: "foreign", ScopeID: "foreign", BaseCommit: foreignBase, HeadCommit: foreignBase}}}
	fail := &attempt{task: Task{ID: "failed-task", Attempt: 1, Status: "failed"}, order: 3, activity: activity{WorkspaceBefore: root.activity.WorkspaceAfter}}
	fail.in.Git.BaseRepo = root.activity.WorkspaceAfter.Repository
	fail.activity.Scope = "root"
	h := &history{attempts: map[string][]*attempt{"root": {root}, "foreign": {foreign}, "failure": {fail}}}
	s := &Service{}
	snap, e := s.snapshot(context.Background(), h, Request{JobID: "job"}, nil)
	require.NoError(t, e)
	require.Len(t, snap.Workspaces, 2)
	snap, e = s.snapshot(context.Background(), h, Request{JobID: "job", Workspace: "root"}, root)
	require.NoError(t, e)
	require.Len(t, snap.Workspaces, 1)
	require.Equal(t, rootBase, snap.Workspaces[0].HeadCommit)
	snap, e = s.snapshot(context.Background(), h, Request{JobID: "job", Workspace: "root", Boundary: "before"}, fail)
	require.NoError(t, e)
	require.Equal(t, rootBase, snap.Workspaces[0].HeadCommit)
	require.Equal(t, "incoming", snap.Workspaces[0].State)
	snap, e = s.snapshot(context.Background(), h, Request{JobID: "job", Workspace: "root"}, fail)
	require.NoError(t, e)
	require.Equal(t, "unavailable", snap.Workspaces[0].Availability)
}

type exportAPI struct {
	snapshot *Snapshot
	data     string
	err      error
}

func (a exportAPI) Inspect(context.Context, Request) (*Response, error) {
	return &Response{Version: 1, Snapshot: a.snapshot}, nil
}
func (a exportAPI) Download(context.Context, Request) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(a.data)), a.err
}

func TestExportRejectsUnsafePathsAndIncompleteBytes(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "artifacts/a/../../escape", "artifacts/back\\slash"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "export")
			api := exportAPI{snapshot: &Snapshot{Version: 1, Artifacts: []Artifact{{Name: "log", Path: name, Size: 3, Availability: "available"}}}, data: "abc"}
			_, e := Export(context.Background(), api, Request{JobID: "job"}, dir)
			require.ErrorContains(t, e, "unsafe snapshot path")
			require.NoDirExists(t, dir)
			require.NoFileExists(t, filepath.Join(root, "escape"))
		})
	}
	dir := filepath.Join(t.TempDir(), "export")
	api := exportAPI{snapshot: &Snapshot{Artifacts: []Artifact{{Name: "log", Path: "artifacts/log", Size: 5, Availability: "available"}}}, data: "abc"}
	_, e := Export(context.Background(), api, Request{JobID: "job"}, dir)
	require.ErrorContains(t, e, "size mismatch")
	require.NoDirExists(t, dir)
	api.snapshot = &Snapshot{Workspaces: []Workspace{{Availability: "unavailable", Reason: "expired"}}}
	_, e = Export(context.Background(), api, Request{JobID: "job"}, dir)
	require.ErrorContains(t, e, "expired")
	require.NoDirExists(t, dir)
}

func TestEmptyBaseExportNeedsNoRemote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "export")
	api := exportAPI{snapshot: &Snapshot{Version: 1, Workspaces: []Workspace{{WorkspaceState: coretask.WorkspaceState{Repository: "https://unavailable.invalid/empty.git", EmptyBase: true}, Directory: "workspaces/empty", Availability: "available"}}}}
	snap, e := Export(context.Background(), api, Request{JobID: "job"}, dir)
	require.NoError(t, e)
	require.DirExists(t, filepath.Join(dir, snap.Workspaces[0].Directory, ".git"))
	b, e := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	require.NoError(t, e)
	require.True(t, json.Valid(b))
}

func TestNonRecipeOutputKindIsOrdinaryData(t *testing.T) {
	raw := json.RawMessage(`{"kind":"sentiment","score":0}`)
	value, _, err := decodeOutput(raw)
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(value))
}

func TestSelfContainedBundleDoesNotRequireOrigin(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		b, e := cmd.CombinedOutput()
		require.NoError(t, e, string(b))
		return strings.TrimSpace(string(b))
	}
	git("init")
	git("commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "-m", "head")
	head := git("rev-parse", "HEAD")
	bundle := filepath.Join(t.TempDir(), "full.bundle")
	git("bundle", "create", bundle, "HEAD")
	data, e := os.ReadFile(bundle)
	require.NoError(t, e)
	dir := filepath.Join(t.TempDir(), "export")
	api := exportAPI{snapshot: &Snapshot{Version: 1, Workspaces: []Workspace{{WorkspaceState: coretask.WorkspaceState{Repository: filepath.Join(t.TempDir(), "missing-origin"), BaseCommit: base, HeadCommit: head}, Availability: "available", Directory: "worktree", Bundle: &Artifact{Reference: artifacts.NewStoredRef(jobdb.ArtifactKey{}), Size: int64(len(data))}}}}, data: string(data)}
	snap, e := Export(context.Background(), api, Request{JobID: "job"}, dir)
	require.NoError(t, e)
	require.DirExists(t, filepath.Join(dir, "worktree", ".git"))
	require.NotEmpty(t, snap.Workspaces[0].Bundle.Path)
	require.FileExists(t, filepath.Join(dir, snap.Workspaces[0].Bundle.Path))
	manifest, e := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	require.NoError(t, e)
	var saved Snapshot
	require.NoError(t, json.Unmarshal(manifest, &saved))
	require.Equal(t, snap.Workspaces[0].Bundle.Path, saved.Workspaces[0].Bundle.Path)
}
