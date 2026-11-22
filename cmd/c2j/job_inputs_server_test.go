package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const inputHistoryServerVersion = "v0.0.99-0.20261121210721-67cce3c7a795"

type originPolicy struct {
	DenyJob, FaultJob, Fault string
	Wrapped, OpenDenied      bool
}

func inputOriginBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("C2J_TEST_INPUT_HISTORY_SERVER_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "jobdb-server")
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".")
		cmd.Dir = "testdata/input-history-server"
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	info, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	found := false
	for _, dep := range info.Deps {
		require.Nil(t, dep.Replace, "published server must not use module replacements")
		if dep.Path == "github.com/colony-2/jobdb" {
			require.Equal(t, inputHistoryServerVersion, dep.Version)
			found = true
		}
	}
	require.True(t, found, "test server must link the published JobDB module")
	t.Logf("real JobDB server: github.com/colony-2/jobdb@%s", inputHistoryServerVersion)
	return binary
}

type originServer struct {
	URL    string
	input  *json.Encoder
	output *json.Decoder
}

func startInputOrigin(t *testing.T, binary string) *originServer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "C2J_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = in.Close()
		err := cmd.Wait()
		cancel()
		require.NoError(t, err, stderr.String())
	})
	s := &originServer{input: json.NewEncoder(in), output: json.NewDecoder(out)}
	var ready struct{ URL string }
	require.NoError(t, s.output.Decode(&ready))
	require.True(t, strings.HasPrefix(ready.URL, "http://127.0.0.1:"))
	s.URL = ready.URL
	return s
}

func (s *originServer) set(t *testing.T, p originPolicy) {
	t.Helper()
	require.NoError(t, s.input.Encode(p))
	var ack struct{ OK bool }
	require.NoError(t, s.output.Decode(&ack))
	require.True(t, ack.OK)
}
