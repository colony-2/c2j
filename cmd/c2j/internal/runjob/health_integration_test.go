package runjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

func healthTestListener(t *testing.T, opts *Options) (net.Listener, string) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	opts.HealthPort = 1234
	opts.healthListen = func(network, address string) (net.Listener, error) {
		require.Equal(t, "tcp4", network)
		require.Equal(t, "0.0.0.0:1234", address)
		return listener, nil
	}
	return listener, "http://" + listener.Addr().String() + "/health"
}

func readHealth(t *testing.T, url string) healthResponse {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(url)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	var snapshot healthResponse
	require.NoError(t, json.NewDecoder(response.Body).Decode(&snapshot))
	return snapshot
}

func TestHealthBeforeSetupAndPortConflicts(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "supplied"}[supplied], func(t *testing.T) {
			f := newSuppliedCLI(t, "id: health-setup\nop: command_execution\ninputs: {run: 'true'}\n", time.Minute)
			entered, release := make(chan struct{}), make(chan struct{})
			sentinel := errors.New("setup failure")
			f.opts.buildDependencies = func(ctx context.Context, opts Options) (*runnerDeps, func(), error) {
				close(entered)
				<-release
				return nil, nil, sentinel
			}
			_, url := healthTestListener(t, &f.opts)
			done := make(chan error, 1)
			go func() {
				if supplied {
					done <- f.run(t, false)
				} else {
					done <- Run(celltest.Context(context.Background()), f.opts)
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("setup not reached")
			}
			snapshot := readHealth(t, url)
			require.Equal(t, "framework", snapshot.TaskType)
			require.Equal(t, f.key.TenantId, snapshot.TenantID)
			require.Equal(t, f.key.JobId, snapshot.JobID)
			require.Nil(t, snapshot.LeaseExpiresAt)
			require.Equal(t, snapshot.StartedAt, snapshot.CurrentTaskStartedAt)
			close(release)
			require.ErrorIs(t, <-done, sentinel)
			require.Zero(t, f.renewals.Load())
			_, err := (&http.Client{Timeout: time.Second}).Get(url)
			require.Error(t, err)

			// An occupied real port is rejected before dependency construction or import.
			occupied, err := net.Listen("tcp4", "0.0.0.0:0")
			require.NoError(t, err)
			defer occupied.Close()
			f.opts.HealthPort = occupied.Addr().(*net.TCPAddr).Port
			f.opts.healthListen = nil
			f.opts.buildDependencies = func(context.Context, Options) (*runnerDeps, func(), error) {
				t.Fatal("setup after failed bind")
				return nil, nil, nil
			}
			if supplied {
				err = f.run(t, false)
			} else {
				err = Run(celltest.Context(context.Background()), f.opts)
			}
			require.ErrorContains(t, err, "listen for run health")
			require.Zero(t, f.renewals.Load())
		})
	}
}

func TestHealthLiveLeaseAndRunnerLifetime(t *testing.T) {
	for _, mode := range []string{"supplied-complete", "supplied-cancel", "ordinary-complete", "ordinary-cancel", "ordinary-serve-failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			entered, release := filepath.Join(dir, "entered"), filepath.Join(dir, "release")
			command := "touch " + shellcmd.QuotePOSIX(entered) + "; while [ ! -f " + shellcmd.QuotePOSIX(release) + " ]; do sleep 0.01; done"
			// Leave time for completion under the race detector while still
			// requiring an actual heartbeat and advancing expiry below.
			f := newSuppliedCLI(t, fmt.Sprintf("id: health-live\nop: command_execution\ninputs:\n  run: %q\n", command), 3*time.Second)
			supplied := mode == "supplied-complete" || mode == "supplied-cancel"
			if !supplied {
				// Return the fixture's dispatcher lease before exercising ordinary claiming.
				capability, err := remote.DecodeLeaseCapability(f.encoded)
				require.NoError(t, err)
				owner, err := f.client.ImportLease(celltest.Context(context.Background()), capability)
				require.NoError(t, err)
				require.NoError(t, owner.Reschedule(celltest.Context(context.Background()), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}))
				f.denyClaims.Store(false)
			}
			listener, url := healthTestListener(t, &f.opts)
			ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 8*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if supplied {
					f.opts.Stdin = bytes.NewReader(f.encoded)
					done <- RunWithLease(ctx, f.opts, "-")
				} else {
					done <- Run(ctx, f.opts)
				}
			}()
			require.Eventually(t, func() bool { _, err := os.Stat(entered); return err == nil }, 4*time.Second, 10*time.Millisecond)
			first := readHealth(t, url)
			require.Contains(t, first.TaskType, "command_execution:")
			require.Nil(t, first.ExtensionOp)
			require.NotNil(t, first.LeaseExpiresAt)
			require.True(t, first.CurrentTaskStartedAt.After(first.StartedAt))
			// Repeated probes preserve the same operation attempt timing.
			before := f.renewals.Load()
			backendBefore := f.nonHeartbeatRequests.Load()
			for i := 0; i < 5; i++ {
				next := readHealth(t, url)
				require.Equal(t, first.CurrentTaskStartedAt, next.CurrentTaskStartedAt)
			}
			require.Equal(t, backendBefore, f.nonHeartbeatRequests.Load(), "probes must not request backend data")
			if supplied {
				require.Eventually(t, func() bool {
					s := readHealth(t, url)
					return s.LeaseExpiresAt != nil && s.LeaseExpiresAt.After(*first.LeaseExpiresAt)
				}, 3*time.Second, 10*time.Millisecond)
				require.Greater(t, f.renewals.Load(), before)
			}
			switch mode {
			case "supplied-cancel", "ordinary-cancel":
				cancel()
			case "ordinary-serve-failure":
				require.NoError(t, listener.Close())
			default:
				require.NoError(t, os.WriteFile(release, nil, 0600))
			}
			select {
			case err := <-done:
				if mode == "ordinary-complete" || mode == "supplied-complete" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				if mode == "ordinary-serve-failure" {
					require.ErrorContains(t, err, "serve run health")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("runner did not stop")
			}
			_, err := (&http.Client{Timeout: time.Second}).Get(url)
			require.Error(t, err)
		})
	}
}

func TestHealthTerminalOutcomes(t *testing.T) {
	for _, scenario := range []struct {
		name, recipe string
		failure      bool
	}{
		{"input", "id: health-input\nop: input\ninputs: {form: {question: 'Answer?', type: short_answer}}\n", true},
		{"handoff", "id: health-handoff\nexecution: {resources: {memory: 16Gi}}\nop: command_execution\ninputs: {run: 'true'}\n", false},
		{"failure", "id: health-failure\nop: command_execution\ninputs: {run: 'exit 9'}\n", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newSuppliedCLI(t, scenario.recipe, time.Minute)
			if scenario.name == "handoff" {
				small := "2Gi"
				f.opts.ExecutionFlags.Memory = &small
			}
			_, url := healthTestListener(t, &f.opts)
			err := f.run(t, false)
			if scenario.failure {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Contains(t, f.stdout.String(), `"kind":"environment_required"`)
			}
			_, err = (&http.Client{Timeout: time.Second}).Get(url)
			require.Error(t, err)
		})
	}
}

type healthPromptBarrier struct {
	entered chan struct{}
	reader  *io.PipeReader
	once    sync.Once
}

func (b *healthPromptBarrier) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	return b.reader.Read(p)
}

func TestHealthPromptCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "serve-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newSuppliedCLI(t, "id: health-prompt\nop: input\ninputs: {form: {question: 'Answer?', type: short_answer}}\n", time.Minute)
			ctx, cancel := context.WithCancel(celltest.Context(context.Background()))
			capability, err := remote.DecodeLeaseCapability(f.encoded)
			require.NoError(t, err)
			owner, err := f.client.ImportLease(ctx, capability)
			require.NoError(t, err)
			require.NoError(t, owner.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}))
			f.denyClaims.Store(false)
			reader, writer := io.Pipe()
			barrier := &healthPromptBarrier{entered: make(chan struct{}), reader: reader}
			f.opts.InputMode = "prompt"
			f.opts.Stdin = barrier
			listener, url := healthTestListener(t, &f.opts)
			done := make(chan error, 1)
			finished := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				_ = reader.Close()
				_ = writer.Close()
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("runner did not stop after prompt cleanup")
				}
			})
			go func() {
				done <- Run(ctx, f.opts)
				close(finished)
			}()
			select {
			case <-barrier.entered:
			case err := <-done:
				t.Fatalf("runner before prompt: %v", err)
			case <-time.After(4 * time.Second):
				t.Fatal("prompt not reached")
			}
			// Keep a connection open so listener failure alone cannot satisfy the
			// shutdown check: existing health connections must also be closed.
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			response, err := client.Get(url)
			require.NoError(t, err)
			var snapshot healthResponse
			require.NoError(t, json.NewDecoder(response.Body).Decode(&snapshot))
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "framework", snapshot.TaskType)
			require.Nil(t, snapshot.LeaseExpiresAt)
			if mode == "cancel" {
				cancel()
			} else {
				require.NoError(t, listener.Close())
			}
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
				if mode == "serve-failure" {
					require.ErrorContains(t, err, "serve run health")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("runner must stop without further prompt input")
			}
			response, err = client.Get(url)
			if response != nil {
				_ = response.Body.Close()
			}
			require.Error(t, err, "health endpoint must stop with the runner")
			require.NotContains(t, f.stdout.String(), "input submitted")
		})
	}
}

func TestHealthFrameworkWaitAndReclaim(t *testing.T) {
	dir := t.TempDir()
	entered, release := filepath.Join(dir, "entered"), filepath.Join(dir, "release")
	command := "touch " + shellcmd.QuotePOSIX(entered) + "; while [ ! -f " + shellcmd.QuotePOSIX(release) + " ]; do sleep 0.01; done"
	f := newSuppliedCLI(t, fmt.Sprintf("id: health-reclaim\nsequence:\n  - id: question\n    op: input\n    inputs: {form: {question: 'Answer?', type: short_answer}}\n  - id: command\n    op: command_execution\n    inputs:\n      run: %q\n", command), time.Minute)
	capability, err := remote.DecodeLeaseCapability(f.encoded)
	require.NoError(t, err)
	owner, err := f.client.ImportLease(celltest.Context(context.Background()), capability)
	require.NoError(t, err)
	require.NoError(t, owner.Reschedule(celltest.Context(context.Background()), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}))
	f.denyClaims.Store(false)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	barrier := &healthPromptBarrier{entered: make(chan struct{}), reader: reader}
	f.opts.InputMode = "prompt"
	f.opts.Stdin = barrier
	_, url := healthTestListener(t, &f.opts)
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, f.opts) }()
	select {
	case <-barrier.entered:
	case err := <-done:
		t.Fatalf("runner before prompt: %v", err)
	case <-ctx.Done():
		t.Fatal("prompt not reached")
	}
	waiting := readHealth(t, url)
	require.Equal(t, "framework", waiting.TaskType)
	require.Nil(t, waiting.LeaseExpiresAt)
	require.Nil(t, waiting.ExtensionOp)
	require.Equal(t, waiting.CurrentTaskStartedAt, readHealth(t, url).CurrentTaskStartedAt)
	_, err = writer.Write([]byte("ok\n"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(entered); return err == nil }, 3*time.Second, 10*time.Millisecond)
	executing := readHealth(t, url)
	require.Equal(t, waiting.StartedAt, executing.StartedAt)
	require.Contains(t, executing.TaskType, "command_execution:")
	require.True(t, executing.CurrentTaskStartedAt.After(waiting.CurrentTaskStartedAt))
	require.NotNil(t, executing.LeaseExpiresAt)
	require.NoError(t, os.WriteFile(release, nil, 0600))
	require.NoError(t, <-done)
}
