package extensions

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestArtifactFileStagingExactBytesAndCleanup(t *testing.T) {
	body := []byte("\x00\xff\r\n${{ untouched.value }}\nλ")
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write(body)
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(source, body, 0600))
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(source, link))
	op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: fileInputSchema()}})
	require.NoError(t, err)
	deps := ops.NewOpDependenciesBuilder().WithOperationPaths(ops.OperationPaths{Workdir: root}).Build()
	// Keep explicit expand:false and an unusual logical name in the accepted JSON.
	inputs := map[string]any{"instructions_file": map[string]any{"kind": "external", "name": "../../logical", "external": map[string]any{"url": "file://" + link, "expand": false}}, "prompt_prefix_file": artifacts.NewExternalRef("gzip", server.URL, false), "unrelated": "value"}
	before, err := json.Marshal(inputs)
	require.NoError(t, err)
	payload, cleanup, err := op.stageArtifactFiles(context.Background(), deps, inputs)
	require.NoError(t, err)
	for name, want := range map[string][]byte{"instructions_file": body, "prompt_prefix_file": compressed.Bytes()} {
		descriptor := payload[name].(map[string]any)
		require.Len(t, descriptor, 2)
		path := descriptor["path"].(string)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, want, data)
		require.Equal(t, inputs[name], descriptor["ref"])
		info, err := os.Lstat(path)
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular())
		require.EqualValues(t, 0600, info.Mode().Perm())
		info, err = os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		require.EqualValues(t, 0700, info.Mode().Perm())
	}
	after, err := json.Marshal(inputs)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoError(t, cleanup())
	require.NoError(t, cleanup())
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestArtifactFilePartialFailureAndSocketCleanup(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(t.TempDir(), "first")
	require.NoError(t, os.WriteFile(first, []byte("first bytes"), 0600))
	// Short path: Unix sockets have a platform-dependent length limit.
	socketRoot, err := os.MkdirTemp("/tmp", "af-socket-")
	require.NoError(t, err)
	defer os.RemoveAll(socketRoot)
	socket := filepath.Join(socketRoot, "socket")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	defer listener.Close()
	op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: fileInputSchema()}})
	require.NoError(t, err)
	deps := ops.NewOpDependenciesBuilder().WithOperationPaths(ops.OperationPaths{Workdir: root}).Build()
	for _, bad := range []string{socket, filepath.Join(t.TempDir(), "missing")} {
		_, _, err := op.stageArtifactFiles(context.Background(), deps, map[string]any{"instructions_file": artifacts.NewExternalRef("first", "file://"+first, false), "prompt_prefix_file": artifacts.NewExternalRef("second", "file://"+bad, false)})
		require.ErrorContains(t, err, "inputs.prompt_prefix_file")
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		require.Empty(t, entries, "partial staging must be removed without worker workdir cleanup")
	}
}

func TestArtifactFileFullKeyHandleSelection(t *testing.T) {
	root := t.TempDir()
	op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: fileInputSchema()}})
	require.NoError(t, err)
	key := jobdb.ArtifactKey{JobId: "first", TaskOrdinal: 0, Name: "same", SizeBytes: -1}
	second := key
	second.TaskOrdinal = 2
	third := key
	third.JobId = "third"
	var handles []jobdb.Artifact
	for _, k := range []jobdb.ArtifactKey{third, second, key} {
		a := jobdb.NewArtifactFromBytes("same", []byte(k.JobId))
		jobdb.AssignArtifactKey(a, k)
		handles = append(handles, a)
	}
	deps := ops.NewOpDependenciesBuilder().WithArtifacts(handles).WithOperationPaths(ops.OperationPaths{Workdir: root}).Build()
	payload, cleanup, err := op.stageArtifactFiles(context.Background(), deps, map[string]any{"instructions_file": artifacts.NewStoredRef(key), "prompt_prefix_file": artifacts.NewStoredRef(third)})
	require.NoError(t, err)
	defer cleanup()
	for name, want := range map[string]string{"instructions_file": "first", "prompt_prefix_file": "third"} {
		data, err := os.ReadFile(payload[name].(map[string]any)["path"].(string))
		require.NoError(t, err)
		require.Equal(t, want, string(data))
	}
}

func TestArtifactFileSourceSwapRejectsAndClosesHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	require.NoError(t, os.WriteFile(path, []byte("regular"), 0600))
	var opened int
	file, err := openArtifactFileSource(path, func(path string, flags int, mode uint32) (int, error) {
		// Stat saw a regular file. Replace it immediately before the actual open.
		require.NoError(t, os.Remove(path))
		require.NoError(t, unix.Mkfifo(path, 0600))
		fd, err := unix.Open(path, flags, mode)
		opened = fd
		return fd, err
	})
	require.Nil(t, file)
	require.ErrorContains(t, err, "regular file")
	_, err = unix.FcntlInt(uintptr(opened), unix.F_GETFD, 0)
	require.ErrorIs(t, err, unix.EBADF, "rejected source handle must close")
}

type fileResolverControl struct {
	workflowctl.WorkflowControl
	tenant   string
	key      jobdb.ArtifactKey
	artifact jobdb.Artifact
}

func (c *fileResolverControl) GetArtifactLazy(_ context.Context, tenant string, key jobdb.ArtifactKey) jobdb.Artifact {
	c.tenant = tenant
	c.key = key
	return c.artifact
}

type fileResolverJobTool struct{ ops.JobTool }

func (fileResolverJobTool) GetJobKey() jobdb.JobKey {
	return jobdb.JobKey{TenantId: "current-tenant", JobId: "consumer"}
}
func TestArtifactFileFallbackResolverContext(t *testing.T) {
	root := t.TempDir()
	op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: fileInputSchema()}})
	require.NoError(t, err)
	key := jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 2, Name: "same", SizeBytes: -1}
	input := map[string]any{"instructions_file": artifacts.NewStoredRef(key)}
	deps := ops.NewOpDependenciesBuilder().WithOperationPaths(ops.OperationPaths{Workdir: root}).Build()
	_, _, err = op.stageArtifactFiles(context.Background(), deps, input)
	require.ErrorContains(t, err, "tenant-scoped artifact resolution context is unavailable")
	ctl := &fileResolverControl{artifact: jobdb.NewArtifactFromBytes("same", []byte("bytes"))}
	deps = ops.NewOpDependenciesBuilder().WithOperationPaths(ops.OperationPaths{Workdir: root}).WithWorkflowControl(ctl).WithJobTool(fileResolverJobTool{}).Build()
	payload, cleanup, err := op.stageArtifactFiles(context.Background(), deps, input)
	require.NoError(t, err)
	defer cleanup()
	require.Equal(t, "current-tenant", ctl.tenant)
	require.Equal(t, key, ctl.key)
	body, err := os.ReadFile(payload["instructions_file"].(map[string]any)["path"].(string))
	require.NoError(t, err)
	require.Equal(t, "bytes", string(body))
}
