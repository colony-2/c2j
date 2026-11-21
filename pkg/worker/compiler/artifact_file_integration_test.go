package compiler

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"

	"github.com/stretchr/testify/require"
)

// This is an actual executable consumer. It never calls the extension runtime
// directly; the compiler/worker must deliver readable descriptors over stdin.
func TestArtifactFileProbeProcess(t *testing.T) {
	if os.Getenv("C2J_ARTIFACT_FILE_PROBE") != "1" {
		return
	}
	fail := func(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(91) }
	trace := os.Getenv("C2J_ARTIFACT_FILE_TRACE")
	// Record entry before decoding or inspecting inputs. Descriptor traces below
	// are success evidence and cannot prove that an invalid consumer never ran.
	entry, err := os.OpenFile(trace+".entries", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		fail(err)
	}
	if _, err := entry.WriteString("entry\n"); err != nil {
		fail(err)
	}
	if err := entry.Close(); err != nil {
		fail(err)
	}
	var input map[string]any
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		fail(err)
	}
	var prior []map[string]any
	if b, err := os.ReadFile(trace); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var v map[string]any
			if json.Unmarshal([]byte(line), &v) == nil {
				prior = append(prior, v)
			}
		}
	}
	output := map[string]any{}
	for _, old := range prior {
		for _, name := range []string{"instructions_file", "prompt_prefix_file"} {
			if d, ok := old[name].(map[string]any); ok {
				if _, err := os.Stat(d["path"].(string)); !os.IsNotExist(err) {
					fail(fmt.Errorf("previous attempt staging still exists"))
				}
			}
		}
	}
	for _, name := range []string{"instructions_file", "prompt_prefix_file"} {
		value, present := input[name]
		if !present {
			continue
		}
		d, ok := value.(map[string]any)
		if !ok || len(d) != 2 {
			fail(fmt.Errorf("%s is not a descriptor", name))
		}
		if _, err := artifacts.ParseFileRef(d["ref"]); err != nil {
			fail(err)
		}
		path, ok := d["path"].(string)
		if !ok || !filepath.IsAbs(path) {
			fail(fmt.Errorf("missing absolute path"))
		}
		info, err := os.Lstat(path)
		if err != nil {
			fail(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			fail(fmt.Errorf("not a private regular file"))
		}
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil {
			fail(err)
		}
		if parent.Mode().Perm() != 0700 || !strings.HasPrefix(filepath.Base(filepath.Dir(path)), "artifact-files-") {
			fail(fmt.Errorf("not private staging directory"))
		}
		body, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		output[name] = map[string]any{"sha256": fmt.Sprintf("%x", sha256.Sum256(body)), "size": len(body)}
	}
	if path, ok := input["legacy_path"].(string); ok {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "legacy content" {
			fail(fmt.Errorf("legacy explicit binding changed: %v", err))
		}
		output["legacy_ok"] = true
	}
	if os.Getenv("C2J_ARTIFACT_CHECK_TOKEN") == "1" && input["token"] != "token-private-value\n" {
		fail(fmt.Errorf("secret was not materialized"))
	}
	if snapshot, ok := input["checkpoint"].(map[string]any); ok {
		files, ok := snapshot["files"].(map[string]any)
		if !ok {
			fail(fmt.Errorf("checkpoint not hydrated"))
		}
		if _, err := os.ReadFile(files["state"].(string)); err != nil {
			fail(err)
		}
		output["checkpoint_ok"] = true
	}
	if v, ok := input["message"]; ok {
		if message, ok := v.(string); ok && len(message) > 256 {
			output["message_size"] = len(message)
			output["message_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(message)))
		} else {
			output["message"] = v
		}
	}
	// Trace stays outside JobDB. The consumer intentionally records descriptors
	// here so tests can inspect their lifetime without disclosing paths as output.
	f, err := os.OpenFile(trace, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		fail(err)
	}
	input["_probe_digests"] = output
	if err := json.NewEncoder(f).Encode(input); err != nil {
		fail(err)
	}
	if err := f.Close(); err != nil {
		fail(err)
	}
	switch os.Getenv("C2J_ARTIFACT_FILE_MODE") {
	case "fail":
		os.Exit(9)
	case "fail-once":
		if len(prior) == 0 {
			if path := os.Getenv("C2J_ARTIFACT_MUTATE_FILE"); path != "" {
				if err := os.WriteFile(path, []byte("attempt two bytes"), 0600); err != nil {
					fail(err)
				}
			}
			os.Exit(9)
		}
	case "wait":
		time.Sleep(time.Hour)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"output": output}); err != nil {
		fail(err)
	}
	os.Exit(0)
}

func artifactFileManifest(t *testing.T, repo, trace, mode string, extra map[string]any) {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	spec := map[string]any{
		"name": "direct-files", "command": []string{binary, "-test.run=^TestArtifactFileProbeProcess$"},
		"env": map[string]string{"C2J_ARTIFACT_FILE_PROBE": "1", "C2J_ARTIFACT_FILE_TRACE": trace, "C2J_ARTIFACT_FILE_MODE": mode},
		"input_schema": map[string]any{"type": "object", "properties": map[string]any{
			"instructions_file":  map[string]any{"type": "object", "x-c2j-artifact-file": true},
			"prompt_prefix_file": map[string]any{"type": "object", "x-c2j-artifact-file": true},
			"message":            map[string]any{"type": "string", "default": "default message"},
		}}, "output_schema": map[string]any{"type": "object"},
	}
	for k, v := range extra {
		spec[k] = v
	}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	commitHookFile(t, repo, "op/op.yaml", string(raw))
}

func submitArtifactFileRecipe(t *testing.T, rt jobdb.ConsumerRuntime, repo, source, tenant, id string, inputs map[string]any, attachments ...jobdb.Artifact) jobdb.JobKey {
	t.Helper()
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	attached := append([]jobdb.Artifact{jobdb.NewArtifactFromBytes("artifact-files"+starter.RecipeArtifactSuffix, []byte(source))}, attachments...)
	data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: tenant, RecipeName: "artifact-files", JobContext: job, GitRef: "main", Inputs: inputs, Cell: "test", CellRepo: celltest.Repo(repo)}, attached...)
	celltest.Create(t, rt, tenant, "test", celltest.Repo(repo))
	h, err := rt.SubmitJob(celltest.Context(context.Background()), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: tenant, JobID: id, JobType: "recipe", Data: data, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}, Cell: "test", CellRepo: celltest.Repo(repo)}})
	require.NoError(t, err)
	return h.JobKey
}

func fileTrace(t *testing.T, trace string) []map[string]any {
	t.Helper()
	f, err := os.Open(trace)
	require.NoError(t, err)
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 4<<20)
	var out []map[string]any
	for s.Scan() {
		var v map[string]any
		require.NoError(t, jsonexact.Unmarshal(s.Bytes(), &v))
		out = append(out, jsonexact.Native(v).(map[string]any))
	}
	require.NoError(t, s.Err())
	assertFileLaunches(t, trace, len(out))
	return out
}

func assertFileLaunches(t *testing.T, trace string, want int) {
	t.Helper()
	entries, err := os.ReadFile(trace + ".entries")
	if want == 0 {
		require.True(t, os.IsNotExist(err), "consumer must not launch: entries=%q error=%v", entries, err)
		return
	}
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("entry\n", want), string(entries), "independent consumer entry count")
}

func TestArtifactFileProbeEntryBeforeValidation(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	for _, input := range []string{"{", `{"instructions_file":"/invalid/raw/path"}`, `{"instructions_file":{"ref":{},"path":"/missing"}}`} {
		t.Run(input, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "trace")
			cmd := exec.Command(binary, "-test.run=^TestArtifactFileProbeProcess$")
			cmd.Env = append(os.Environ(), "C2J_ARTIFACT_FILE_PROBE=1", "C2J_ARTIFACT_FILE_TRACE="+trace)
			cmd.Stdin = strings.NewReader(input)
			output, err := cmd.CombinedOutput()
			require.Error(t, err, string(output))
			require.Equal(t, 91, cmd.ProcessState.ExitCode())
			assertFileLaunches(t, trace, 1)
			require.NoFileExists(t, trace, "failed validation must leave no descriptor trace")
		})
	}
}
func assertFileCleanup(t *testing.T, trace []map[string]any) {
	t.Helper()
	require.NotEmpty(t, trace)
	for _, entry := range trace {
		for _, name := range []string{"instructions_file", "prompt_prefix_file"} {
			if d, ok := entry[name].(map[string]any); ok {
				path := d["path"].(string)
				require.NoFileExists(t, path)
				require.NoDirExists(t, filepath.Dir(path))
			}
		}
	}
}
func assertFileDigest(t *testing.T, output map[string]any, name string, body []byte) {
	t.Helper()
	d := output[name].(map[string]any)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), d["sha256"])
	require.EqualValues(t, len(body), d["size"])
}
func fileJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestArtifactFileDirectConsumption(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	body := []byte("BINARY\x00\xff\r\n${{ do.not.evaluate }}\nλ\n")
	for _, route := range []string{"submitted", "file", "localhost file", "http", "http redirect", "https", "git"} {
		t.Run(route, func(t *testing.T) {
			repo := t.TempDir()
			workspaceRepo(t, repo, "source")
			trace := filepath.Join(t.TempDir(), "trace")
			artifactFileManifest(t, repo, trace, "", nil)
			selector := "./op"
			if route == "git" {
				selector = "git+file://" + filepath.ToSlash(repo) + "//op@HEAD"
			}
			input := "'${{ context.artifacts[\"document\"] }}'"
			prefixInput := "'${{ context.artifacts[\"empty\"] }}'"
			if route == "file" || route == "localhost file" {
				path := filepath.Join(t.TempDir(), "document")
				require.NoError(t, os.WriteFile(path, body, 0600))
				host := ""
				if route == "localhost file" {
					host = "localhost"
				}
				input = fileJSON(t, artifacts.NewExternalRef("../../label", "file://"+host+path, false))
				empty := filepath.Join(t.TempDir(), "empty")
				require.NoError(t, os.WriteFile(empty, nil, 0600))
				prefixInput = fileJSON(t, artifacts.NewExternalRef("empty", "file://"+host+empty, false))
			}
			if route == "http" || route == "http redirect" || route == "https" {
				handler := celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/redirect" {
						http.Redirect(w, r, "/document", 302)
						return
					}
					if r.URL.Path == "/empty" {
						w.WriteHeader(200)
						return
					}
					_, _ = w.Write(body)
				}))
				var server *httptest.Server
				if route == "http" || route == "http redirect" {
					server = httptest.NewServer(handler)
				} else {
					server = httptest.NewTLSServer(handler)
					pool := x509.NewCertPool()
					pool.AddCert(server.Certificate())
					old := http.DefaultTransport
					tr := http.DefaultTransport.(*http.Transport).Clone()
					tr.TLSClientConfig = &tls.Config{RootCAs: pool}
					http.DefaultTransport = tr
					t.Cleanup(func() { http.DefaultTransport = old; tr.CloseIdleConnections() })
				}
				t.Cleanup(server.Close)
				suffix := "/document"
				if route == "http redirect" {
					suffix = "/redirect"
				}
				input = fileJSON(t, artifacts.NewExternalRef("document", server.URL+suffix, false))
				prefixInput = fileJSON(t, artifacts.NewExternalRef("empty", server.URL+"/empty", false))
			}
			rt := newTimeoutReproRuntime(t, "sqlite")
			source := fmt.Sprintf(`id: artifact-files
sequence:
 - id: consume
   op: %s
   retry: {maximum_attempts: 1}
   inputs:
    instructions_file: %s
    prompt_prefix_file: %s
outputs: {result: '${{ sequence.consume.outputs }}'}
`, selector, input, prefixInput)
			key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", nil, jobdb.NewArtifactFromBytes("document", body), jobdb.NewArtifactFromBytes("empty", nil))
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			result := gitLifecycleOutput(t, out)["result"].(map[string]any)
			assertFileDigest(t, result, "instructions_file", body)
			assertFileDigest(t, result, "prompt_prefix_file", nil)
			require.Equal(t, "default message", result["message"])
			records := fileTrace(t, trace)
			require.Len(t, records, 1)
			assertFileCleanup(t, records)
			if route == "submitted" {
				ref, err := artifacts.ParseFileRef(records[0]["instructions_file"].(map[string]any)["ref"])
				require.NoError(t, err)
				require.Equal(t, key.JobId, ref.Stored.Key.JobId)
				require.Zero(t, ref.Stored.Key.TaskOrdinal)
			}
		})
	}
}

func TestArtifactFileProducerAndTenantIdentity(t *testing.T) {
	emit := coreops.NewActivityMappedOpV2[struct {
		Text string `json:"text"`
	}, map[string]any](coreops.OpMetadata{Type: "file_emit"}, func(deps coreops.OpDependencies, _ context.Context, in struct {
		Text string `json:"text"`
	}) (map[string]any, error) {
		return map[string]any{}, deps.AddOutputArtifact(jobdb.NewArtifactFromBytes("same", []byte(in.Text)))
	})
	withRegisteredOps(t, extops.GetExecutionOp(), emit)
	repo := t.TempDir()
	workspaceRepo(t, repo, "source")
	trace := filepath.Join(t.TempDir(), "trace")
	artifactFileManifest(t, repo, trace, "", nil)
	rt := newTimeoutReproRuntime(t, "sqlite")
	source := `id: artifact-files
sequence:
 - id: a
   op: file_emit
   inputs: {text: first}
 - id: b
   op: file_emit
   inputs: {text: second}
 - id: consume
   op: ./op
   inputs:
    instructions_file: '${{ sequence.a.artifacts["same"] }}'
    prompt_prefix_file: '${{ sequence.b.artifacts["same"] }}'
outputs: {result: '${{ sequence.consume.outputs }}'}
`
	key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", nil)
	out, err := runGitLifecycle(t, rt, key)
	require.NoError(t, err)
	result := gitLifecycleOutput(t, out)["result"].(map[string]any)
	assertFileDigest(t, result, "instructions_file", []byte("first"))
	assertFileDigest(t, result, "prompt_prefix_file", []byte("second"))
	// Store identical submission identities in two tenants and two producer jobs.
	// Only the consuming job's tenant may supply the bytes for the same full key.
	for _, tenant := range []string{"file-tests", "other-tenant"} {
		for _, id := range []string{"producer-a", "producer-b"} {
			text := tenant + "/" + id
			submitArtifactFileRecipe(t, rt, repo, "id: artifact-files\nsequence: []\n", tenant, id, nil, jobdb.NewArtifactFromBytes("same", []byte(text)))
		}
	}
	a := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "producer-a", TaskOrdinal: 0, Name: "same", SizeBytes: -1})
	a.Name = "same outer label"
	b := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "producer-b", TaskOrdinal: 0, Name: "same", SizeBytes: -1})
	b.Name = a.Name
	source = fmt.Sprintf("id: artifact-files\nop: ./op\ninputs: {instructions_file: %s, prompt_prefix_file: %s}\n", fileJSON(t, a), fileJSON(t, b))
	// A new trace avoids interpreting successful earlier invocations as retries.
	trace2 := filepath.Join(t.TempDir(), "trace")
	artifactFileManifest(t, repo, trace2, "", nil)
	key = submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", nil)
	out, err = runGitLifecycle(t, rt, key)
	require.NoError(t, err)
	result = gitLifecycleOutput(t, out)
	assertFileDigest(t, result, "instructions_file", []byte("file-tests/producer-a"))
	assertFileDigest(t, result, "prompt_prefix_file", []byte("file-tests/producer-b"))
	for _, f := range []string{trace, trace2} {
		assertFileCleanup(t, fileTrace(t, f))
	}
}

// Keep the job history and automatic diagnostic/snapshot artifact contents in
// the assertion, rather than checking only an in-memory invocation struct.
func assertFileDurableValues(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey, forbidden ...string) string {
	t.Helper()
	ctx := celltest.Context(context.Background())
	history, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true})
	require.NoError(t, err)
	raw := fileJSON(t, history)
	for _, value := range forbidden {
		require.NotContains(t, raw, value)
	}
	for _, entry := range history.Execution {
		for _, data := range []*jobdb.ExecutionSnapshot{entry.Record.Input, entry.Record.Output} {
			if data == nil {
				continue
			}
			for _, a := range data.Artifacts {
				if a.Key == nil {
					continue
				}
				// Reading all consumer diagnostics is deliberate; producer document bytes
				// and source archives are legitimate artifact content and are excluded.
				if !strings.Contains(a.Name, "input") && !strings.Contains(a.Name, "diagnostic") && !strings.Contains(a.Name, "execution") {
					continue
				}
				reader, err := rt.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: key.TenantId, JobId: a.Key.JobId}, Ordinal: a.Key.TaskOrdinal, Name: a.Key.Name})
				require.NoError(t, err)
				stream, err := reader.Open()
				require.NoError(t, err)
				body, err := io.ReadAll(stream)
				require.NoError(t, err)
				require.NoError(t, stream.Close())
				for _, value := range forbidden {
					require.NotContains(t, string(body), value, "artifact %s", a.Name)
				}
			}
		}
	}
	return raw
}
