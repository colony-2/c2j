package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type artifactFilePendingRuntime struct{ jobdb.ConsumerRuntime }

func (r artifactFilePendingRuntime) skipGitTask(name string) bool {
	return name == "extension_execution:extension_execution"
}

func TestArtifactFileCompactReplacementWorker(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	ctx := celltest.Context(context.Background())
	repo := t.TempDir()
	workspaceRepo(t, repo, "source")
	trace := filepath.Join(t.TempDir(), "trace")
	artifactFileManifest(t, repo, trace, "", nil)
	message := strings.Repeat("unrelated input λ\r\n", 1000)
	body := []byte("PRIVATE-DOCUMENT-BYTES\r\n\x00\xff")
	var requests atomic.Int32
	server := httptest.NewServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write(body) })))
	defer server.Close()
	reference := artifacts.NewExternalRef("document", server.URL, false)
	source := fmt.Sprintf(`id: artifact-files
input_schema:
 message: {type: string, required: true}
op: ./op
inputs:
 message: '${{ jq(inputs.message, ". * 10") }}'
 instructions_file: %s
`, fileJSON(t, reference))
	db := filepath.Join(t.TempDir(), "jobs.db")
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: db, Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			require.NoError(t, rt.Close(ctx))
		}
	}()
	key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", map[string]any{"message": message})
	out, err := runGitLifecycle(t, artifactFilePendingRuntime{rt}, key)
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	pending, err := engine.GetWaitingTask(ctx, key)
	require.NoError(t, err)
	require.Equal(t, "extension_execution:extension_execution", pending.TaskType())
	data, err := pending.Data()
	require.NoError(t, err)
	raw, err := data.GetData()
	require.NoError(t, err)
	require.Less(t, len(raw), 128*1024)
	var invocation workerops.ActivityInvocationRequest
	require.NoError(t, json.Unmarshal(raw, &invocation))
	require.NotEmpty(t, invocation.InputArtifact)
	attached, err := data.GetArtifacts()
	require.NoError(t, err)
	var compact []byte
	for _, a := range attached {
		if a.Name() == invocation.InputArtifact {
			compact, err = a.Bytes(ctx)
			require.NoError(t, err)
			key, err := a.ArtifactKey()
			require.NoError(t, err)
			require.Greater(t, key.SizeBytes, int64(128*1024))
		}
	}
	require.NotEmpty(t, compact)
	require.Contains(t, string(compact), server.URL)
	require.NotContains(t, string(compact), "artifact-files-")
	require.NotContains(t, string(compact), "PRIVATE-DOCUMENT-BYTES")
	require.Contains(t, string(compact), "unrelated input λ")
	require.Zero(t, requests.Load())
	assertFileLaunches(t, trace, 0)
	require.NoFileExists(t, trace)
	require.NoError(t, rt.Close(ctx))
	closed = true
	fresh, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: db, Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	defer fresh.Close(ctx)
	out, err = runGitLifecycle(t, fresh, key)
	require.NoError(t, err)
	result := gitLifecycleOutput(t, out)
	assertFileDigest(t, result, "instructions_file", body)
	message = strings.Repeat(message, 10)
	require.EqualValues(t, len(message), result["message_size"])
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(message))), result["message_sha256"])
	require.EqualValues(t, 1, requests.Load())
	assertFileCleanup(t, fileTrace(t, trace))
	durable := assertFileDurableValues(t, fresh, key, "artifact-files-", "PRIVATE-DOCUMENT-BYTES")
	require.Contains(t, durable, server.URL)
}

func TestArtifactFileExactIntegerKeysAcrossTransportAndDefaults(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprint("compact=", compact), func(t *testing.T) {
			repo := t.TempDir()
			workspaceRepo(t, repo, "source")
			trace := filepath.Join(t.TempDir(), "trace")
			rt := newTimeoutReproRuntime(t, "sqlite")
			submitArtifactFileRecipe(t, rt, repo, "id: artifact-files\nsequence: []\n", "file-tests", "large-key-producer", nil, jobdb.NewArtifactFromBytes("first", []byte("one")), jobdb.NewArtifactFromBytes("second", []byte("two")))
			key := jobdb.ArtifactKey{JobId: "large-key-producer", TaskOrdinal: 0, Name: "first", SizeBytes: 9223372036854775807}
			defaultRef := artifacts.NewStoredRef(key)
			secondKey := key
			secondKey.Name = "second"
			secondKey.SizeBytes = 9007199254740993
			secondRef := artifacts.NewStoredRef(secondKey)
			schema := map[string]any{"type": "object", "properties": map[string]any{
				"instructions_file":  map[string]any{"type": "object", "x-c2j-artifact-file": true, "default": defaultRef},
				"prompt_prefix_file": map[string]any{"type": "object", "x-c2j-artifact-file": true},
				"message":            map[string]any{"type": "string"},
			}}
			artifactFileManifest(t, repo, trace, "", map[string]any{"input_schema": schema})
			header := ""
			message := ""
			var inputs map[string]any
			if compact {
				header = "input_schema: {sample: {type: string, required: true}}\n"
				message = " message: '${{ jq(inputs.sample, \". * 10\") }}'\n"
				inputs = map[string]any{"sample": strings.Repeat("large unrelated", 2000)}
			}
			source := fmt.Sprintf("id: artifact-files\n%sop: ./op\ninputs:\n prompt_prefix_file: %s\n%s", header, fileJSON(t, secondRef), message)
			consumer := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", inputs)
			out, err := runGitLifecycle(t, rt, consumer)
			require.NoError(t, err)
			result := gitLifecycleOutput(t, out)
			assertFileDigest(t, result, "instructions_file", []byte("one"))
			assertFileDigest(t, result, "prompt_prefix_file", []byte("two"))
			records := fileTrace(t, trace)
			require.Len(t, records, 1)
			for name, want := range map[string]jobdb.ArtifactKey{"instructions_file": key, "prompt_prefix_file": secondKey} {
				got, err := artifacts.ParseFileRef(records[0][name].(map[string]any)["ref"])
				require.NoError(t, err)
				require.Equal(t, want, got.Stored.Key)
			}
			assertFileCleanup(t, records)
			durable := assertFileDurableValues(t, rt, consumer, "artifact-files-")
			require.Contains(t, durable, "9223372036854775807")
			require.Contains(t, durable, "9007199254740993")
			require.NotContains(t, durable, "9007199254740992")
		})
	}
}
