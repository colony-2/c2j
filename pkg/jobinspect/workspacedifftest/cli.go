package workspacedifftest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
)

var cliBuild struct {
	sync.Mutex
	path string
}

// PrepareCLI builds the real process adapter once while its owning test lives.
// Call before replacing PATH with a Git trap. The binary remains in test scratch.
func PrepareCLI(t *testing.T) string {
	t.Helper()
	cliBuild.Lock()
	defer cliBuild.Unlock()
	if _, err := os.Stat(cliBuild.path); err == nil {
		return cliBuild.path
	}
	_, file, _, _ := runtime.Caller(0)
	target := filepath.Join(t.TempDir(), "c2j")
	command := exec.Command("go", "build", "-mod=readonly", "-o", target, "./cmd/c2j")
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, out)
	}
	cliBuild.path = target
	return target
}

// CLI follows only the supplied authenticated helper environment. Its byte
// result comes from an actual raw CLI process, independent of metadata decoding.
type CLI struct {
	Binary string
	Env    map[string]string
}

func (c CLI) run(ctx context.Context, q jobinspect.WorkspaceDiffRequest, metadata bool) ([]byte, error) {
	args := []string{"job", "diff", q.JobID}
	for _, selector := range []struct {
		name  string
		value *string
	}{{"task", q.Task}, {"scope", q.Scope}, {"workspace", q.Workspace}, {"boundary", q.Boundary}} {
		if selector.value != nil {
			args = append(args, "--"+selector.name, *selector.value)
		}
	}
	if q.Attempt != nil {
		args = append(args, "--attempt", strconv.Itoa(*q.Attempt))
	}
	if metadata {
		args = append(args, "--json")
	}
	command := exec.CommandContext(ctx, c.Binary, args...)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "C2J_") {
			command.Env = append(command.Env, entry)
		}
	}
	for k, v := range c.Env {
		command.Env = append(command.Env, k+"="+v)
	}
	// A helper regression must fail instead of reading ambient backend credentials.
	command.Env = append(command.Env, "C2J_JOBDB=http://127.0.0.1:1")
	var out, diag bytes.Buffer
	command.Stdout, command.Stderr = &out, &diag
	err := command.Run()
	if err != nil {
		var diagnostic jobinspect.WorkspaceDiffError
		if json.Unmarshal(bytes.TrimSpace(diag.Bytes()), &diagnostic) == nil && diagnostic.Code != "" {
			return out.Bytes(), &diagnostic
		}
		return out.Bytes(), errors.Join(err, errors.New(diag.String()))
	}
	if diag.Len() != 0 {
		return out.Bytes(), errors.New(diag.String())
	}
	return out.Bytes(), nil
}

func (c CLI) OpenWorkspaceDiff(ctx context.Context, q jobinspect.WorkspaceDiffRequest) (*jobinspect.WorkspaceDiff, io.ReadCloser, error) {
	raw, err := c.run(ctx, q, true)
	var m jobinspect.WorkspaceDiff
	if decodeErr := json.Unmarshal(raw, &m); decodeErr != nil {
		return nil, nil, errors.Join(err, decodeErr)
	}
	if validation := jobinspect.ValidateWorkspaceDiff(q, &m); validation != nil {
		return nil, nil, validation
	}
	if m.Status == "unavailable" {
		var diagnostic *jobinspect.WorkspaceDiffError
		if !errors.As(err, &diagnostic) || diagnostic.Code != "unavailable" {
			return nil, nil, errors.New("CLI unavailable metadata must exit nonzero with an unavailable diagnostic")
		}
		return &m, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	data, err := c.run(ctx, q, false)
	if err != nil {
		return nil, nil, err
	}
	return &m, io.NopCloser(bytes.NewReader(data)), nil
}

func (c CLI) Inspect(ctx context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	if q.Operation != "workspace_diff" {
		return nil, errors.New("CLI fixture only supports workspace_diff inspection")
	}
	options := jobinspect.WorkspaceDiffOptions{}
	if q.WorkspaceDiff != nil {
		options = *q.WorkspaceDiff
	}
	raw, err := c.run(ctx, jobinspect.WorkspaceDiffRequest{JobID: q.JobID, WorkspaceDiffOptions: options}, true)
	var m jobinspect.WorkspaceDiff
	if e := json.Unmarshal(raw, &m); e != nil {
		return nil, errors.Join(err, e)
	}
	if err != nil && m.Status != "unavailable" {
		return nil, err
	}
	return &jobinspect.Response{Version: 1, WorkspaceDiff: &m}, nil
}
