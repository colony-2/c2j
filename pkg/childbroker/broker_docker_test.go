package childbroker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/starter"
)

const shaiBaseTestImage = "ghcr.io/colony-2/shai-base:latest"

func TestBrokerSubmitFromMountedC2JInShai(t *testing.T) {
	if runningInContainer() {
		t.Skip("skipping Docker/Shai child broker integration test because the test process is already running inside a container")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Shai sandbox execution is not supported on Windows hosts")
	}
	requireDockerImage(t, shaiBaseTestImage)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	repoRoot := testRepoRoot(t)
	binDir := t.TempDir()
	c2jPath := filepath.Join(binDir, "c2j")
	build := exec.CommandContext(ctx, "go", "build", "-o", c2jPath, "./cmd/c2j")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build c2j: %v\n%s", err, string(out))
	}
	if err := os.Chmod(c2jPath, 0o755); err != nil {
		t.Fatalf("chmod c2j: %v", err)
	}

	workspace := t.TempDir()
	writeDockerBrokerWorkspace(t, workspace)

	current := jobcontext.Current{
		TenantID:           "0",
		JobID:              "parent-docker",
		JobType:            starter.RecipeJobType,
		OpType:             "command_execution",
		OpStep:             "submit-child",
		OpTaskType:         "activity:command_execution",
		CellName:           "parent-cell",
		RepositorySource:   "file:///parent",
		GitRef:             "main",
		InvocationPath:     "sequence.submit-child",
		InvocationSequence: 7,
		InvocationHash:     "invoke-docker",
	}
	submitter := &captureSubmitter{}
	broker, err := Start(ctx, Options{
		Current:            current,
		Submitter:          submitter,
		ContainerReachable: true,
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer broker.Close()
	if !dockerCanReach(t, broker.Host(), broker.Port()) {
		t.Fatalf("Docker containers cannot reach parent child-job broker at %s:%d", broker.Host(), broker.Port())
	}

	env := jobcontext.EnvForCurrent(current)
	for key, value := range broker.Env() {
		env[key] = value
	}
	noProxy := "host.docker.internal,localhost,127.0.0.1"
	if host := strings.TrimSpace(broker.Host()); host != "" {
		noProxy += "," + host
	}
	env["NO_PROXY"] = noProxy
	env["no_proxy"] = env["NO_PROXY"]

	stdout, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{
		WorkspaceRoot: workspace,
		WorkingDir:    workspace,
		Shell:         "sh",
		Run:           "/c2j-bin/c2j submit --embed --cell /src --recipe-file child.yaml --json",
		Env:           env,
		Sandbox:       &process.SandboxInput{Type: process.SandboxTypeShai},
		RequiredMounts: []ops.RequiredMount{{
			Source: binDir,
			Target: "/c2j-bin",
			Mode:   ops.MountModeReadOnly,
		}},
		RequiredPorts: []ops.RequiredPort{{
			Host: broker.Host(),
			Port: broker.Port(),
		}},
	})
	if err != nil {
		t.Fatalf("run c2j submit in Shai: %v\nstdout:\n%s\nstderr:\n%s", err, string(stdout), string(stderr))
	}

	var submitted struct {
		TenantID string `json:"tenant_id"`
		JobID    string `json:"job_id"`
		Recipe   string `json:"recipe"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(stdout))), &submitted); err != nil {
		t.Fatalf("decode c2j submit output %q: %v\nstderr:\n%s", string(stdout), err, string(stderr))
	}
	if submitted.TenantID != "0" || submitted.JobID == "" || submitted.Recipe != "child_from_docker" {
		t.Fatalf("unexpected c2j submit output: %#v", submitted)
	}
	if submitter.calls != 1 {
		t.Fatalf("submit calls = %d, want 1", submitter.calls)
	}
	if submitter.last.TenantId != "0" || submitter.last.JobID != submitted.JobID {
		t.Fatalf("unexpected submitted job: %#v", submitter.last)
	}

	artifacts, err := submitter.last.Data.GetArtifacts()
	if err != nil {
		t.Fatalf("GetArtifacts(): %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Name() != "child_from_docker.recipe.yaml" {
		t.Fatalf("unexpected submitted artifacts: %#v", artifacts)
	}

	var meta starter.JobMetadata
	if err := json.Unmarshal(submitter.last.Metadata, &meta); err != nil {
		t.Fatalf("metadata decode: %v", err)
	}
	if meta.ParentTenantID != "0" || meta.ParentJobID != "parent-docker" || meta.ParentInvocationHash != "invoke-docker" {
		t.Fatalf("broker did not attach parent metadata: %#v", meta)
	}
	if meta.ParentOpStep != "submit-child" || meta.ParentOpType != "command_execution" {
		t.Fatalf("broker did not preserve parent op context: %#v", meta)
	}

	started := broker.StartedJobs()
	if len(started.JobIDs) != 1 || started.JobIDs[0] != submitted.JobID {
		t.Fatalf("unexpected broker started jobs: %#v", started)
	}
}

func requireDockerImage(t *testing.T, image string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if out, err := exec.CommandContext(ctx, "docker", "info").CombinedOutput(); err != nil {
		t.Fatalf("docker info failed: %v\n%s", err, string(out))
	}
	if _, err := exec.CommandContext(ctx, "docker", "image", "inspect", image).CombinedOutput(); err == nil {
		return
	} else {
		t.Logf("docker image %s is not available locally; pulling it now", image)
	}

	pullCtx, pullCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer pullCancel()
	if out, err := exec.CommandContext(pullCtx, "docker", "pull", image).CombinedOutput(); err != nil {
		t.Fatalf("docker pull %s failed: %v\n%s", image, err, string(out))
	}
}

func dockerCanReach(t *testing.T, host string, port int) bool {
	t.Helper()
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx,
		"docker", "run", "--rm",
		"--add-host", "host.docker.internal:host-gateway",
		"-e", "C2J_BROKER_HOST="+host,
		"-e", "C2J_BROKER_PORT="+strconv.Itoa(port),
		shaiBaseTestImage,
		"bash", "-lc", `timeout 3 bash -c ': >/dev/tcp/${C2J_BROKER_HOST}/${C2J_BROKER_PORT}'`,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("Docker broker reachability probe failed: %v\n%s", err, string(out))
		return false
	}
	return true
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

func writeDockerBrokerWorkspace(t *testing.T, workspace string) {
	t.Helper()
	configPath := filepath.Join(workspace, ".shai", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir .shai: %v", err)
	}
	config := `
type: shai-sandbox
version: 1
image: ghcr.io/colony-2/shai-base:latest
resources:
  child-broker-test: {}
apply:
  - path: ./
    resources: [child-broker-test]
`
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(config)+"\n"), 0o644); err != nil {
		t.Fatalf("write shai config: %v", err)
	}

	recipeYAML := `
id: child_from_docker
version: "1.0.0"
sequence: []
outputs: {}
`
	if err := os.WriteFile(filepath.Join(workspace, "child.yaml"), []byte(strings.TrimSpace(recipeYAML)+"\n"), 0o644); err != nil {
		t.Fatalf("write child recipe: %v", err)
	}
}
