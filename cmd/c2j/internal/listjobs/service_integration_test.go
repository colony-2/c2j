package listjobs

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/colony-2/jobdb/pkg/jobdb"
	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunDefaultsToCurrentCell(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := t.TempDir()
	writeListConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	server, tenantID, _, err := startListTestRuntime(ctx)
	if err != nil {
		t.Fatalf("startListTestRuntime(): %v", err)
	}
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(ctx, Options{
		TenantID:   tenantID,
		SWFURL:     server.URL,
		WorkingDir: root,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "JOB ID") || !strings.Contains(out, "STATUS") {
		t.Fatalf("expected table header, got:\n%s", out)
	}
	if !strings.Contains(out, "job-alpha-1") || !strings.Contains(out, "job-alpha-2") {
		t.Fatalf("expected alpha job rows, got:\n%s", out)
	}
	if strings.Contains(out, "job-beta-1") {
		t.Fatalf("expected beta job to be filtered out, got:\n%s", out)
	}
}

func TestRunJSONSupportsPagination(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := t.TempDir()
	writeListConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	server, tenantID, _, err := startListTestRuntime(ctx)
	if err != nil {
		t.Fatalf("startListTestRuntime(): %v", err)
	}
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(ctx, Options{
		TenantID:   tenantID,
		SWFURL:     server.URL,
		WorkingDir: root,
		PageSize:   1,
		JSONOutput: true,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	var result listResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(result.Jobs) != 1 {
		t.Fatalf("expected one page row, got %#v", result.Jobs)
	}
	if strings.TrimSpace(result.NextPageToken) == "" {
		t.Fatalf("expected next page token, got %#v", result)
	}
}

func TestRunAllJSONFetchesEveryPage(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := t.TempDir()
	writeListConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	server, tenantID, expectedStatus, err := startListTestRuntime(ctx)
	if err != nil {
		t.Fatalf("startListTestRuntime(): %v", err)
	}
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(ctx, Options{
		TenantID:   tenantID,
		SWFURL:     server.URL,
		WorkingDir: root,
		PageSize:   1,
		All:        true,
		Statuses:   []string{strings.ToLower(string(expectedStatus))},
		JSONOutput: true,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	var result listResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(result.Jobs) != 2 {
		t.Fatalf("expected both jobs, got %#v", result.Jobs)
	}
	if result.NextPageToken != "" {
		t.Fatalf("expected no next page token after --all, got %#v", result)
	}
}

func TestRunExplicitCellAcceptsLongRepo(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := t.TempDir()
	writeListConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	server, tenantID, _, err := startListTestRuntime(ctx)
	if err != nil {
		t.Fatalf("startListTestRuntime(): %v", err)
	}
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(ctx, Options{
		TenantID:   tenantID,
		SWFURL:     server.URL,
		WorkingDir: root,
		Cell:       "github.com/acme/boo-beta",
		JSONOutput: true,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	var result listResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(result.Jobs) != 1 || result.Jobs[0].JobID != "job-beta-1" {
		t.Fatalf("unexpected jobs: %#v", result.Jobs)
	}
}

func TestRunExplicitCellAcceptsShortName(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	root := t.TempDir()
	writeListConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	server, tenantID, _, err := startListTestRuntime(ctx)
	if err != nil {
		t.Fatalf("startListTestRuntime(): %v", err)
	}
	defer server.Close()

	var stdout bytes.Buffer
	if err := Run(ctx, Options{
		TenantID:   tenantID,
		SWFURL:     server.URL,
		WorkingDir: root,
		Cell:       "beta",
		JSONOutput: true,
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Run(): %v", err)
	}

	var result listResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(result.Jobs) != 1 || result.Jobs[0].JobID != "job-beta-1" {
		t.Fatalf("unexpected jobs: %#v", result.Jobs)
	}
}

func startListTestRuntime(ctx context.Context) (*httptest.Server, string, jobdb.JobStatus, error) {
	tenantID := "tenant-list-test"
	underlying := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(underlying))

	runtime, err := remoteruntime.New(server.URL, server.Client())
	if err != nil {
		server.Close()
		return nil, "", "", err
	}

	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		server.Close()
		return nil, "", "", err
	}

	for _, job := range []struct {
		id       string
		jobType  string
		cellName string
		repo     string
	}{
		{id: "job-alpha-1", jobType: "alpha", cellName: "alpha", repo: "https://github.com/acme/boo-alpha.git"},
		{id: "job-alpha-2", jobType: "alpha", cellName: "alpha", repo: "https://github.com/acme/boo-alpha.git"},
		{id: "job-beta-1", jobType: "beta", cellName: "beta", repo: "https://github.com/acme/boo-beta.git"},
	} {
		data, err := jobdb.NewTaskData(map[string]any{"job_id": job.id})
		if err != nil {
			server.Close()
			return nil, "", "", err
		}
		metadata, err := json.Marshal(map[string]any{"cell_name": job.cellName, "repo": job.repo})
		if err != nil {
			server.Close()
			return nil, "", "", err
		}
		if _, err := engine.SubmitJob(ctx, jobdb.SubmitJob{
			TenantId:  tenantID,
			JobID:     job.id,
			JobType:   job.jobType,
			Data:      data,
			RunPolicy: jobdb.DefaultRunPolicy(),
			Metadata:  metadata,
		}); err != nil {
			server.Close()
			return nil, "", "", err
		}
	}

	info, err := engine.GetJob(ctx, jobdb.JobKey{TenantId: tenantID, JobId: "job-alpha-1"})
	if err != nil {
		server.Close()
		return nil, "", "", err
	}

	return server, tenantID, info.Status, nil
}

func writeListConfig(t *testing.T, root string, raw string) {
	t.Helper()

	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(raw)+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
