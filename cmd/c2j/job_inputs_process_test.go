package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/consumerfixture"
	"github.com/colony-2/c2j/internal/inputhistorytest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/input/historybridge"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func inputHistoryBinary(t *testing.T) string {
	t.Helper()
	if binary := os.Getenv("C2J_TEST_INPUT_HISTORY_BINARY"); binary != "" {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "c2j")
	command := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".")
	b, e := command.CombinedOutput()
	require.NoError(t, e, string(b))
	return binary
}
func inputProcess(t *testing.T, binary string, env map[string]string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, append([]string{"job", "inputs"}, args...)...)
	command.Dir = t.TempDir()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "C2J_") {
			command.Env = append(command.Env, entry)
		}
	}
	for k, v := range env {
		command.Env = append(command.Env, k+"="+v)
	}
	var out, diagnostics bytes.Buffer
	command.Stdout = &out
	command.Stderr = &diagnostics
	e := command.Run()
	code := 0
	if e != nil {
		var exited *exec.ExitError
		require.ErrorAs(t, e, &exited, "process did not exit: %s", diagnostics.String())
		code = exited.ExitCode()
	}
	return out.Bytes(), diagnostics.Bytes(), code
}
func inputError(t *testing.T, out, diag []byte, code int, want string) {
	t.Helper()
	require.NotZero(t, code, string(diag))
	require.Empty(t, out)
	d, e := jobinputs.DecodeError(diag)
	require.NoError(t, e, string(diag))
	require.Equal(t, want, d.Code, string(diag))
}
func inputAuthority() jobinputs.Authority {
	return jobinputs.Authority{Tenant: inputhistorytest.Tenant, Guard: func(ctx context.Context, _ string) error { return context.Cause(ctx) }, Bind: func(ctx context.Context) (context.Context, context.CancelFunc) { return context.WithCancel(ctx) }}
}
func inputServer(t *testing.T, ctx context.Context, rt historybridge.Runtime, f *inputhistorytest.Fixture) *childbroker.Server {
	t.Helper()
	ctx = jobinputs.WithFactory(ctx, func() (jobinputs.API, error) {
		return historybridge.New(rt, f.Control, inputAuthority(), input.HistoryConfig{})
	})
	s, e := childbroker.Start(ctx, childbroker.Options{Current: jobcontext.Current{TenantID: inputhistorytest.Tenant, JobID: f.Job.JobId, GoalID: "fixture"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, e)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}
func TestInputHistoryCLIProcessRetainedRecords(t *testing.T) {
	binary := inputHistoryBinary(t)
	serverBinary := inputOriginBinary(t)
	var ambientCalls atomic.Int32
	ambient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { ambientCalls.Add(1); http.Error(w, "ambient trap", 500) }))
	defer ambient.Close()
	for _, backend := range []string{"sqlite", "http", "published-http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "retained.db"), MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
			require.NoError(t, e)
			defer db.Close(ctx)
			var rt jobdb.ConsumerRuntime = db
			if backend == "http" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
				defer server.Close()
				rt, e = remote.New(server.URL, server.Client())
				require.NoError(t, e)
			} else if backend == "published-http" {
				origin := startInputOrigin(t, serverBinary)
				rt, e = remote.New(origin.URL, http.DefaultClient)
				require.NoError(t, e)
			}
			f, e := inputhistorytest.Build(ctx, rt)
			require.NoError(t, e)
			require.NoError(t, f.Advance(ctx, 0))
			policy := &inputhistorytest.Policy{ConsumerRuntime: rt}
			s := inputServer(t, ctx, policy, f)
			env := s.Env()
			env["C2J_JOBDB"] = ambient.URL
			// Forged ambient identity never controls self or the helper tenant.
			for k, v := range jobcontext.EnvForCurrent(jobcontext.Current{TenantID: "forged", JobID: "forged-job", GoalID: "forged"}) {
				env[k] = v
			}
			run := func(args ...string) ([]byte, []byte, int) { return inputProcess(t, binary, env, args...) }
			out, diag, code := run("list", "self", "--json", "--limit", "1")
			require.Zero(t, code, string(diag))
			require.Empty(t, diag)
			envelope, e := jobinputs.DecodeResponse(bytes.NewReader(out), "list")
			require.NoError(t, e)
			var page input.JobInputStoryPage
			require.NoError(t, json.Unmarshal(envelope.Page, &page))
			require.Equal(t, f.Job.JobId, page.JobID)
			require.Equal(t, inputhistorytest.Tenant, page.TenantID)
			require.Len(t, page.Entries, 1)
			require.NotEmpty(t, page.NextPageToken)
			show := []string{"show", "self", "--entry", page.Entries[0].EntryID, "--snapshot", page.SnapshotToken, "--json"}
			out, diag, code = run(show...)
			require.Zero(t, code, string(diag))
			envelope, e = jobinputs.DecodeResponse(bytes.NewReader(out), "show")
			require.NoError(t, e)
			var detail input.JobInputStoryEntryDetail
			require.NoError(t, json.Unmarshal(envelope.Entry, &detail))
			require.Contains(t, string(detail.RawForm), "9007199254740993")
			require.Contains(t, string(detail.RawForm), `"null":null`)

			require.Equal(t, "submission_receipt", detail.Actor.Provenance)
			require.Contains(t, string(detail.Actor.Receipt), "authenticated-human")
			require.Equal(t, "complete", detail.Completeness)
			native, e := input.NewHistoryService(input.HistoryConfig{Reader: f.Control})
			require.NoError(t, e)
			nativeDetail, e := native.GetJobInputStoryEntry(ctx, input.GetJobInputStoryEntryRequest{TenantID: inputhistorytest.Tenant, JobID: f.Job.JobId, EntryID: detail.EntryID})
			require.NoError(t, e)
			require.Equal(t, nativeDetail.RawForm, detail.RawForm)
			require.Equal(t, nativeDetail.RawAcceptedOutput, detail.RawAcceptedOutput)
			require.Equal(t, nativeDetail.RequestManifest, detail.RequestManifest)
			require.Equal(t, nativeDetail.ResponseManifest, detail.ResponseManifest)
			require.Equal(t, nativeDetail.Actor, detail.Actor)
			policy.Fault(func(_ context.Context, ref jobdb.ArtifactRef, a jobdb.ArtifactReader) (jobdb.ArtifactReader, error) {
				if ref.JobKey.JobId == f.Agent.JobId {
					return processDescriptor{ArtifactReader: a, kind: "chunk"}, nil
				}
				return a, nil
			})
			var source input.HistoryArtifactMember
			nested := false
			for _, member := range detail.RequestManifest {
				key, ok := member.Ref.StoredKey()
				require.True(t, ok)
				require.NotContains(t, key.Name, "c2j-invocation-input")
				require.NotContains(t, key.Name, "__c2j_objects__")
				var expected []byte
				if key.JobId == f.Source.JobId && key.Name == f.Empty.Name {
					expected = []byte{}
				} else if key.JobId == f.Source.JobId {
					source = member
					expected = f.Binary
					found := false
					for _, a := range member.Associations {
						if a.Source == "/request/previous/source" {
							found = true
						}
					}
					require.True(t, found)
				} else {
					require.Equal(t, f.Agent.JobId, key.JobId)
					require.Equal(t, f.Agent.TaskOrdinal, key.TaskOrdinal)
					expected = f.Text
					if member.OriginalRef != nil {
						nested = true
						require.NotEmpty(t, member.ResolutionSource)
					}
				}
				destination := filepath.Join(t.TempDir(), "complete.bin")
				out, diag, code = run("artifact", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken, "--member", member.ArtifactID, "--output", destination)
				require.Zero(t, code, string(diag))
				require.Empty(t, out)
				b, e := os.ReadFile(destination)
				require.NoError(t, e)
				require.Equal(t, expected, b)
				// Occupied regular files and symlinks retain their original bytes.
				out, diag, code = run("artifact", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken, "--member", member.ArtifactID, "--output", destination)
				inputError(t, out, diag, code, "write")
				b, e = os.ReadFile(destination)
				require.NoError(t, e)
				require.Equal(t, expected, b)
				link := filepath.Join(t.TempDir(), "link")
				require.NoError(t, os.Symlink(destination, link))
				out, diag, code = run("artifact", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken, "--member", member.ArtifactID, "--output", link)
				inputError(t, out, diag, code, "write")
			}
			require.True(t, nested)
			require.NotEmpty(t, source.ArtifactID)
			for _, member := range detail.ResponseManifest {
				destination := filepath.Join(t.TempDir(), "upload")
				out, diag, code = run("artifact", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken, "--member", member.ArtifactID, "--output", destination)
				require.Zero(t, code, string(diag))
				b, e := os.ReadFile(destination)
				require.NoError(t, e)
				require.Equal(t, []byte("accepted upload"), b)
			}
			artifact := []string{"artifact", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken, "--member", source.ArtifactID, "--output", filepath.Join(t.TempDir(), "download")}
			continuation := []string{"list", "self", "--limit", "1", "--cursor", page.NextPageToken, "--json"}
			policy.Deny(f.Job.JobId, true)
			for _, args := range [][]string{{"list", "self", "--json"}, continuation, show, artifact} {
				out, diag, code = run(args...)
				inputError(t, out, diag, code, "denied")
			}
			policy.Deny(f.Job.JobId, false)
			policy.Deny(f.Source.JobId, true)
			out, diag, code = run(artifact...)
			inputError(t, out, diag, code, "denied")
			policy.Deny(f.Source.JobId, false)
			// Neither a forged occurrence nor member can cause a producer open.
			count := len(policy.Opened())
			for _, selector := range []string{"--entry", "--member"} {
				args := append([]string(nil), artifact...)
				for i := range args {
					if args[i] == selector {
						args[i+1] = "forged-key"
					}
				}
				out, diag, code = run(args...)
				want := "entry_not_found"
				if selector == "--member" {
					want = "nonmember"
				}
				inputError(t, out, diag, code, want)
			}
			require.Len(t, policy.Opened(), count)
			second := inputServer(t, ctx, policy, f)
			out, diag, code = inputProcess(t, binary, second.Env(), show...)
			inputError(t, out, diag, code, "snapshot_expired")
			require.NoError(t, f.Advance(ctx, 1))
			out, diag, code = run(continuation...)
			require.Zero(t, code, string(diag))
			envelope, e = jobinputs.DecodeResponse(bytes.NewReader(out), "list")
			require.NoError(t, e)
			var next input.JobInputStoryPage
			require.NoError(t, json.Unmarshal(envelope.Page, &next))
			require.Len(t, next.Entries, 1)
			require.Equal(t, "pending", next.Entries[0].State)
			require.Empty(t, next.NextPageToken)
			require.NotEqual(t, page.Entries[0].EntryID, next.Entries[0].EntryID)
			out, diag, code = run("list", "self", "--json")
			require.Zero(t, code, string(diag))
			envelope, e = jobinputs.DecodeResponse(bytes.NewReader(out), "list")
			require.NoError(t, e)
			var fresh input.JobInputStoryPage
			require.NoError(t, json.Unmarshal(envelope.Page, &fresh))
			require.Len(t, fresh.Entries, 3)
			require.Equal(t, "answered", fresh.Entries[1].State)
			require.Contains(t, string(envelope.Page), "escaped")
			require.Equal(t, "complete", fresh.Completeness)
			for _, kind := range []string{"read", "close", "short", "excess", "missing", "denied"} {
				t.Run(kind, func(t *testing.T) {
					policy.Fault(func(_ context.Context, ref jobdb.ArtifactRef, a jobdb.ArtifactReader) (jobdb.ArtifactReader, error) {
						if ref.JobKey.JobId != f.Source.JobId {
							return a, nil
						}
						if kind == "missing" {
							return nil, fs.ErrNotExist
						}
						if kind == "denied" {
							return nil, jobdb.ErrLeasePermissionDenied
						}
						return processDescriptor{ArtifactReader: a, kind: kind}, nil
					})
					destination := filepath.Join(t.TempDir(), "failed")
					args := append([]string(nil), artifact...)
					args[len(args)-1] = destination
					out, diag, code = run(args...)
					require.NotZero(t, code, string(diag))
					require.Empty(t, out)
					_, e := jobinputs.DecodeError(diag)
					require.NoError(t, e)
					_, e = os.Lstat(destination)
					require.True(t, os.IsNotExist(e))
					files, e := os.ReadDir(filepath.Dir(destination))
					require.NoError(t, e)
					require.Empty(t, files)
				})
			}
			policy.Fault(nil)
			// Private restoration failures must not become successful partial metadata.
			policy.Fault(func(_ context.Context, ref jobdb.ArtifactRef, a jobdb.ArtifactReader) (jobdb.ArtifactReader, error) {
				if strings.HasSuffix(ref.Name, "c2j-invocation-input.json") {
					return nil, jobdb.ErrLeasePermissionDenied
				}
				return a, nil
			})
			out, diag, code = run("list", "self", "--json")
			inputError(t, out, diag, code, "denied")
			policy.Fault(nil)
			for _, args := range [][]string{{"list", "self", "--limit", "0"}, {"list", "self", "--limit", "501"}, {"show", "self"}, {"list", "self", "--jobdb", ambient.URL}, {"list", "self", "--embed"}, {"list", "self", "--tenant", "forged"}, {"artifact", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken, "--member", source.ArtifactID, "--output", "-"}} {
				out, diag, code = run(args...)
				inputError(t, out, diag, code, "invalid_request")
			}
			for _, op := range []string{"list", "show", "artifact"} {
				out, diag, code = run(op, "--help")
				require.Zero(t, code, string(diag))
				require.Contains(t, string(out), "Usage:")
			}
			// Empty history is a complete native page only after successful authorization.
			out, diag, code = run("list", f.Agent.JobId, "--json")
			require.Zero(t, code, string(diag))
			envelope, e = jobinputs.DecodeResponse(bytes.NewReader(out), "list")
			require.NoError(t, e)
			require.Contains(t, string(envelope.Page), `"entries":[]`)
			require.Contains(t, string(envelope.Page), `"completeness":"complete"`)
		})
	}
	out, diag, code := inputProcess(t, binary, map[string]string{"C2J_JOBDB": ambient.URL}, "list", "self", "--json")
	inputError(t, out, diag, code, "unsupported")
	require.Zero(t, ambientCalls.Load())
}

type processDescriptor struct {
	jobdb.ArtifactReader
	kind string
}

func (a processDescriptor) Open() (io.ReadCloser, error) {
	r, e := a.ArtifactReader.Open()
	if e != nil {
		return nil, e
	}
	b, e := io.ReadAll(r)
	e = errors.Join(e, r.Close())
	if e != nil {
		return nil, e
	}
	if a.kind == "short" {
		b = b[:len(b)-1]
	}
	if a.kind == "excess" {
		b = append(b, 1)
	}
	return &processFault{Reader: bytes.NewReader(b), kind: a.kind}, nil
}

type processFault struct {
	io.Reader
	kind string
}

func (r *processFault) Read(p []byte) (int, error) {
	if r.kind == "chunk" && len(p) > 1 {
		p = p[:1]
	}
	n, e := r.Reader.Read(p)
	if n > 0 && r.kind == "read" {
		return n, errors.New("read fault")
	}
	return n, e
}
func (r *processFault) Close() error {
	if r.kind == "close" {
		return errors.New("close fault")
	}
	return nil
}

func TestInputHistoryCLIProcessTransport(t *testing.T) {
	binary := inputHistoryBinary(t)
	for _, kind := range []string{"control-no-marker", "control-content-length", "missing-size", "short", "excess", "failed", "malformed", "version", "truncated", "old", "payload-version"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "old" {
					http.NotFound(w, r)
					return
				}
				if kind == "payload-version" {
					io.WriteString(w, `{"version":99,"page":{}}`)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("X-C2J-Download-Version", "1")
				w.Header().Set("X-C2J-Download-Size", "3")
				if kind == "missing-size" {
					w.Header().Del("X-C2J-Download-Size")
				}
				if kind == "short" {
					w.Header().Set("X-C2J-Download-Size", "4")
				}
				if kind == "excess" {
					w.Header().Set("X-C2J-Download-Size", "2")
				}
				if kind == "version" {
					w.Header().Set("X-C2J-Download-Version", "2")
				}
				w.Header().Set("Trailer", "X-C2J-Download-Error")
				if kind == "control-content-length" {
					w.Header().Del("Trailer")
					w.Header().Set("Content-Length", "3")
				}
				if kind == "truncated" {
					w.Header().Set("Content-Length", "100")
				}
				_, _ = w.Write([]byte{0, 255, 10})
				if kind == "failed" {
					raw, _ := json.Marshal(jobinputs.Diagnostic("lease_lost", "lease ended", jobdb.ErrExecutionLeaseLost))
					w.Header().Set("X-C2J-Download-Error", base64.RawURLEncoding.EncodeToString(raw))
				}
				if kind == "malformed" {
					w.Header().Set("X-C2J-Download-Error", "not-json")
				}
			}))
			defer server.Close()
			env := jobcontext.EnvForChildJobBroker(jobcontext.ChildJobBroker{Endpoint: server.URL, Token: "fixture", SessionID: "fixture"})
			destination := filepath.Join(t.TempDir(), "result")
			args := []string{"artifact", "self", "--entry", "e", "--snapshot", "s", "--member", "m", "--output", destination}
			if kind == "payload-version" {
				args = []string{"list", "self", "--json"}
			}
			out, diag, code := inputProcess(t, binary, env, args...)
			if strings.HasPrefix(kind, "control-") {
				require.Zero(t, code, string(diag))
				b, e := os.ReadFile(destination)
				require.NoError(t, e)
				require.Equal(t, []byte{0, 255, 10}, b)
			} else {
				require.NotZero(t, code, string(diag))
				require.Empty(t, out)
				_, e := jobinputs.DecodeError(diag)
				require.NoError(t, e)
				_, e = os.Lstat(destination)
				require.True(t, os.IsNotExist(e))
			}
		})
	}
}

type processBlocked struct {
	data            []byte
	started, closed chan struct{}
	once            sync.Once
	closes          atomic.Int32
}

func (b *processBlocked) Read(p []byte) (int, error) {
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		return n, nil
	}
	close(b.started)
	<-b.closed
	return 0, errors.New("closed at completion boundary")
}
func (b *processBlocked) Close() error {
	b.closes.Add(1)
	b.once.Do(func() { close(b.closed) })
	return nil
}

type processBlockedDescriptor struct {
	jobdb.ArtifactReader
	source *processBlocked
}

func (d processBlockedDescriptor) Open() (io.ReadCloser, error) { return d.source, nil }
func TestInputHistoryCLIProcessAuthorityBeforeCompletion(t *testing.T) {
	binary := inputHistoryBinary(t)
	for _, cause := range []error{context.Canceled, jobdb.ErrExecutionLeaseLost} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "blocked.db")})
			require.NoError(t, e)
			defer db.Close(ctx)
			f, e := inputhistorytest.Build(ctx, db)
			require.NoError(t, e)
			policy := &inputhistorytest.Policy{ConsumerRuntime: db}
			life, stop := context.WithCancelCause(ctx)
			defer stop(nil)
			authority := inputAuthority()
			authority.Guard = func(call context.Context, _ string) error {
				if e := context.Cause(life); e != nil {
					return e
				}
				return context.Cause(call)
			}
			authority.Bind = func(call context.Context) (context.Context, context.CancelFunc) {
				bound, cancel := context.WithCancelCause(call)
				watch := context.AfterFunc(life, func() { cancel(context.Cause(life)) })
				return bound, func() { watch(); cancel(context.Canceled) }
			}
			server, e := childbroker.Start(jobinputs.WithFactory(ctx, func() (jobinputs.API, error) {
				return historybridge.New(policy, f.Control, authority, input.HistoryConfig{})
			}), childbroker.Options{Current: jobcontext.Current{TenantID: inputhistorytest.Tenant, JobID: f.Job.JobId, GoalID: "fixture"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, e)
			defer server.Close()
			out, diag, code := inputProcess(t, binary, server.Env(), "list", "self", "--json")
			require.Zero(t, code, string(diag))
			envelope, e := jobinputs.DecodeResponse(bytes.NewReader(out), "list")
			require.NoError(t, e)
			var page input.JobInputStoryPage
			require.NoError(t, json.Unmarshal(envelope.Page, &page))
			out, diag, code = inputProcess(t, binary, server.Env(), "show", "self", "--entry", page.Entries[0].EntryID, "--snapshot", page.SnapshotToken)
			require.Zero(t, code, string(diag))
			envelope, e = jobinputs.DecodeResponse(bytes.NewReader(out), "show")
			require.NoError(t, e)
			var detail input.JobInputStoryEntryDetail
			require.NoError(t, json.Unmarshal(envelope.Entry, &detail))
			var member string
			for _, m := range detail.RequestManifest {
				if k, ok := m.Ref.StoredKey(); ok && k == f.Source {
					member = m.ArtifactID
				}
			}
			require.NotEmpty(t, member)
			source := &processBlocked{data: append([]byte(nil), f.Binary...), started: make(chan struct{}), closed: make(chan struct{})}
			policy.Fault(func(_ context.Context, ref jobdb.ArtifactRef, a jobdb.ArtifactReader) (jobdb.ArtifactReader, error) {
				if ref.JobKey.JobId == f.Source.JobId {
					return processBlockedDescriptor{a, source}, nil
				}
				return a, nil
			})
			destination := filepath.Join(t.TempDir(), "result")
			command := exec.Command(binary, "job", "inputs", "artifact", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken, "--member", member, "--output", destination)
			for _, v := range os.Environ() {
				k, _, _ := strings.Cut(v, "=")
				if !strings.HasPrefix(k, "C2J_") {
					command.Env = append(command.Env, v)
				}
			}
			for k, v := range server.Env() {
				command.Env = append(command.Env, k+"="+v)
			}
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			require.NoError(t, command.Start())
			defer func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case <-source.started:
			case e := <-done:
				t.Fatalf("process ended before source barrier: %v %s", e, stderr.String())
			case <-time.After(5 * time.Second):
				t.Fatal("producer never reached completion barrier")
			}
			_, e = os.Stat(destination)
			require.True(t, os.IsNotExist(e))
			stop(cause)
			select {
			case e := <-done:
				require.Error(t, e)
			case <-time.After(5 * time.Second):
				t.Fatal("canceled process stream did not finish")
			}
			want := "canceled"
			if cause == jobdb.ErrExecutionLeaseLost {
				want = "lease_lost"
			}
			inputError(t, stdout.Bytes(), stderr.Bytes(), 1, want)
			_, e = os.Lstat(destination)
			require.True(t, os.IsNotExist(e))
			files, e := os.ReadDir(filepath.Dir(destination))
			require.NoError(t, e)
			require.Empty(t, files)
			require.Equal(t, int32(1), source.closes.Load())
		})
	}
}

// The source faults are behind the real JobDB HTTP server. Per the human
// cancellation of job 181eCbNT6gcAF1NrTy4GQ5omdHN, complete immutable bytes need
// no additional success marker or upstream source-cleanup guarantee. Returned
// errors, short bytes and authorization failures still prevent file publication.
func TestInputHistoryCLIProcessRemoteOrigin(t *testing.T) {
	binary := inputHistoryBinary(t)
	serverBinary := inputOriginBinary(t)
	for _, kind := range []string{"control", "denied", "wrapped-denied", "producer-denied", "producer-open-denied", "read", "close", "short"} {
		t.Run(kind, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			origin := startInputOrigin(t, serverBinary)
			rt, e := remote.New(origin.URL, http.DefaultClient)
			require.NoError(t, e)
			f, e := inputhistorytest.Build(ctx, rt)
			require.NoError(t, e)
			require.NoError(t, f.Advance(ctx, 0))
			helper := inputServer(t, ctx, rt, f)
			out, diag, code := inputProcess(t, binary, helper.Env(), "list", "self", "--json", "--limit", "1")
			require.Zero(t, code, string(diag))
			response, e := jobinputs.DecodeResponse(bytes.NewReader(out), "list")
			require.NoError(t, e)
			var page input.JobInputStoryPage
			require.NoError(t, json.Unmarshal(response.Page, &page))
			require.NotEmpty(t, page.NextPageToken)
			out, diag, code = inputProcess(t, binary, helper.Env(), "show", "self", "--entry", page.Entries[0].EntryID, "--snapshot", page.SnapshotToken)
			require.Zero(t, code, string(diag))
			response, e = jobinputs.DecodeResponse(bytes.NewReader(out), "show")
			require.NoError(t, e)
			var detail input.JobInputStoryEntryDetail
			require.NoError(t, json.Unmarshal(response.Entry, &detail))
			var member string
			for _, m := range detail.RequestManifest {
				if k, ok := m.Ref.StoredKey(); ok && k == f.Source {
					member = m.ArtifactID
				}
			}
			require.NotEmpty(t, member)
			destination := filepath.Join(t.TempDir(), "result")
			artifact := []string{"artifact", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken, "--member", member, "--output", destination}
			if kind == "denied" || kind == "wrapped-denied" {
				origin.set(t, originPolicy{DenyJob: f.Job.JobId, Wrapped: kind == "wrapped-denied"})
				for _, args := range [][]string{{"list", "self", "--json"}, {"list", "self", "--cursor", page.NextPageToken, "--json"}, {"show", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken}, artifact} {
					out, diag, code = inputProcess(t, binary, helper.Env(), args...)
					inputError(t, out, diag, code, "denied")
					require.NotContains(t, string(diag), "private authorization detail")
				}
				_, e = os.Lstat(destination)
				require.True(t, os.IsNotExist(e))
				origin.set(t, originPolicy{})
				out, diag, code = inputProcess(t, binary, helper.Env(), "show", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken)
				require.Zero(t, code, string(diag))
				return
			}
			if kind == "producer-denied" || kind == "producer-open-denied" {
				origin.set(t, originPolicy{DenyJob: f.Source.JobId, OpenDenied: kind == "producer-open-denied", Wrapped: true})
				out, diag, code = inputProcess(t, binary, helper.Env(), "show", "self", "--entry", detail.EntryID, "--snapshot", page.SnapshotToken)
				require.Zero(t, code, string(diag), "selected cached metadata remains authorized")
				out, diag, code = inputProcess(t, binary, helper.Env(), artifact...)
				inputError(t, out, diag, code, "denied")
				_, e = os.Lstat(destination)
				require.True(t, os.IsNotExist(e))
				origin.set(t, originPolicy{})
			} else if kind != "control" {
				origin.set(t, originPolicy{FaultJob: f.Source.JobId, Fault: kind})
			}
			out, diag, code = inputProcess(t, binary, helper.Env(), artifact...)
			if kind != "short" {
				require.Zero(t, code, string(diag))
				require.Empty(t, out)
				require.Empty(t, diag)
				b, e := os.ReadFile(destination)
				require.NoError(t, e)
				require.Equal(t, f.Binary, b)
			} else {
				require.NotZero(t, code, string(diag))
				require.Empty(t, out)
				_, e := jobinputs.DecodeError(diag)
				require.NoError(t, e)
				_, e = os.Lstat(destination)
				require.True(t, os.IsNotExist(e), "failed remote-origin stream committed destination")
				files, e := os.ReadDir(filepath.Dir(destination))
				require.NoError(t, e)
				require.Empty(t, files)
			}
		})
	}
	// The old server's denial remains a documented compatibility failure. It must
	// reject access without retry or credential substitution, never imply success.
	t.Run("legacy-server-denial", func(t *testing.T) {
		ctx := celltest.Context(context.Background())
		db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "legacy.db")})
		require.NoError(t, e)
		defer db.Close(ctx)
		// Freeze the pre-semantic-error wire response. Linking today's server
		// would silently turn this legacy fixture into a typed-denial server.
		var deniedPath atomic.Value
		deniedPath.Store("")
		var denials atomic.Int32
		current := remote.NewConsumerServer(consumerfixture.New(db))
		transport := httptest.NewServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == deniedPath.Load().(string) {
				denials.Add(1)
				http.Error(w, "lease permission denied", http.StatusInternalServerError)
				return
			}
			current.ServeHTTP(w, r)
		})))
		defer transport.Close()
		rt, e := remote.New(transport.URL, transport.Client())
		require.NoError(t, e)
		f, e := inputhistorytest.Build(ctx, rt)
		require.NoError(t, e)
		helper := inputServer(t, ctx, rt, f)
		out, diag, code := inputProcess(t, binary, helper.Env(), "list", "self", "--json")
		require.Zero(t, code, string(diag))
		deniedPath.Store("/v1/tenants/" + f.Job.TenantId + "/jobs/" + f.Job.JobId)
		out, diag, code = inputProcess(t, binary, helper.Env(), "list", "self", "--json")
		inputError(t, out, diag, code, "storage")
		require.Equal(t, int32(1), denials.Load(), "legacy denial must not retry")
		require.NotContains(t, string(diag), "lease permission denied")
	})
}

type originRuntime struct {
	consumerfixture.Runtime
	policy *inputhistorytest.Policy
}

func (r originRuntime) GetJob(ctx context.Context, key jobdb.JobKey) (jobdb.JobInfo, error) {
	return r.policy.GetJob(ctx, key)
}
func (r originRuntime) OpenArtifact(ctx context.Context, ref jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	return r.policy.OpenArtifact(ctx, ref)
}
