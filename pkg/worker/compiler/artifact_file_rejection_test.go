package compiler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestArtifactFileRejectedBeforeLaunch(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	repo := t.TempDir()
	workspaceRepo(t, repo, "source")
	rt := newTimeoutReproRuntime(t, "sqlite")
	server := httptest.NewServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/failure":
			w.WriteHeader(404)
		case "/truncated":
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("short"))
		case "/loop":
			http.Redirect(w, r, "/loop", 302)
		case "/redirect":
			http.Redirect(w, r, "file:///tmp/x", 302)
		default:
			w.WriteHeader(500)
		}
	})))
	defer server.Close()
	tls := httptest.NewTLSServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("untrusted")) })))
	defer tls.Close()
	fifo := filepath.Join(t.TempDir(), "fifo")
	require.NoError(t, unix.Mkfifo(fifo, 0600))
	// A same-key artifact in a different tenant must not rescue a missing input.
	submitArtifactFileRecipe(t, rt, repo, "id: artifact-files\nsequence: []\n", "other", "foreign", nil, jobdb.NewArtifactFromBytes("same", []byte("private")))
	ext := func(u string) any { return artifacts.NewExternalRef("source", u, false) }
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"raw path", "/tmp/file", "inputs.instructions_file"},
		{"bare URL", server.URL, "inputs.instructions_file"},
		{"null", nil, "inputs.instructions_file"},
		{"empty object", map[string]any{}, "inputs.instructions_file"},
		{"descriptor", map[string]any{"ref": ext("file:///tmp/x"), "path": "/tmp/x"}, "inputs.instructions_file"},
		{"value", map[string]any{"kind": "value", "name": "x", "format": "text", "value": "body"}, "inputs.instructions_file"},
		{"attached", map[string]any{"kind": "attached", "name": "x"}, "inputs.instructions_file"},
		{"set", map[string]any{"kind": "set", "items": []any{}}, "inputs.instructions_file"},
		{"checkpoint", map[string]any{"kind": "object", "type": "example.session/v1", "artifact": map[string]any{}}, "inputs.instructions_file"},
		{"mixed payload", map[string]any{"kind": "external", "name": "x", "external": map[string]any{"url": server.URL}, "stored": map[string]any{}}, "inputs.instructions_file"},
		{"missing external name", map[string]any{"kind": "external", "external": map[string]any{"url": server.URL}}, "inputs.instructions_file"},
		{"missing URL", map[string]any{"kind": "external", "name": "x", "external": map[string]any{}}, "inputs.instructions_file"},
		{"missing key member", map[string]any{"kind": "stored", "stored": map[string]any{"key": map[string]any{"jobId": "foreign", "name": "same", "sizeBytes": -1}}}, "inputs.instructions_file"},
		{"fractional ordinal", map[string]any{"kind": "stored", "stored": map[string]any{"key": map[string]any{"jobId": "foreign", "taskOrdinal": 0.5, "name": "same", "sizeBytes": -1}}}, "inputs.instructions_file"},
		{"remote file host", ext("file://remote/tmp/x"), "inputs.instructions_file"},
		{"missing HTTP host", ext("https:///missing-host"), "inputs.instructions_file"},
		{"unknown member", map[string]any{"kind": "external", "name": "x", "external": map[string]any{"url": server.URL}, "extra": true}, "inputs.instructions_file"},
		{"expansion", artifacts.NewExternalRef("x", server.URL, true), "inputs.instructions_file"},
		{"unsupported source", ext("ftp://example.test/x"), "unsupported"},
		{"missing file", ext("file:///not-present-c2j-fixture"), "open external file"},
		{"directory", ext("file://" + t.TempDir()), "regular file"},
		{"device", ext("file:///dev/null"), "regular file"},
		{"fifo", ext("file://" + fifo), "regular file"},
		{"status", ext(server.URL + "/failure"), "HTTP status 404"},
		{"truncated", ext(server.URL + "/truncated"), "unexpected EOF"},
		{"redirect", ext(server.URL + "/redirect"), "redirect"},
		{"redirect loop", ext(server.URL + "/loop"), "too many redirects"},
		{"TLS untrusted", ext(tls.URL), "certificate"},
		{"unavailable stored", artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "missing", Name: "same", SizeBytes: -1}), "inputs.instructions_file"},
		{"foreign tenant only", artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "foreign", Name: "same", SizeBytes: -1}), "inputs.instructions_file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "trace")
			artifactFileManifest(t, repo, trace, "", nil)
			source := fmt.Sprintf("id: artifact-files\nop: ./op\nretry: {maximum_attempts: 1}\ninputs: {instructions_file: %s}\n", fileJSON(t, tc.value))
			key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", nil)
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunFailed, out.Status)
			require.ErrorContains(t, out.JobError, tc.want)
			assertFileLaunches(t, trace, 0)
			require.NoFileExists(t, trace)
			assertFileDurableValues(t, rt, key, "artifact-files-", "private")
		})
	}
}
