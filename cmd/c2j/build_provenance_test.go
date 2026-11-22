//go:build integration

package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestReleaseBuildRecordsConsumerVersions(t *testing.T) {
	// Use the release workflow's actual linker flags so changing CI cannot
	// silently disconnect the CLI version from durable execution provenance.
	workflow, err := os.ReadFile("../../.github/workflows/release.yaml")
	require.NoError(t, err)
	flags := regexp.MustCompile(`-ldflags "([^"]+)"`).FindStringSubmatch(string(workflow))
	require.Len(t, flags, 2)
	t.Run("release", func(t *testing.T) { testStampedBuildProvenance(t, flags[1]) })

	nix, err := os.ReadFile("../../nix/package.nix")
	require.NoError(t, err)
	line := regexp.MustCompile(`ldflags = \[([^\n]+)\]`).FindStringSubmatch(string(nix))
	require.Len(t, line, 2)
	var nixFlags []string
	for _, item := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(line[1], -1) {
		nixFlags = append(nixFlags, item[1])
	}
	t.Run("nix", func(t *testing.T) { testStampedBuildProvenance(t, strings.Join(nixFlags, " ")) })
}

func testStampedBuildProvenance(t *testing.T, flags string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 3*time.Minute)
	defer cancel()
	const version = "1.2.3-provenance-test"
	const commit = "abcdef1234567890abcdef1234567890abcdef1234"
	ldflags := os.Expand(flags, func(key string) string {
		return map[string]string{"VERSION": version, "COMMIT": commit, "DATE": "2026-10-27T00:00:00Z", "version": version, "revision": commit}[key]
	})
	binary := filepath.Join(t.TempDir(), "c2j")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", binary, ".")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	builtInfo, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	linkedJobDBVersion := ""
	for _, dep := range builtInfo.Deps {
		if dep.Path == "github.com/colony-2/jobdb" {
			if dep.Replace != nil {
				dep = dep.Replace
			}
			linkedJobDBVersion = dep.Version
			if linkedJobDBVersion == "" {
				linkedJobDBVersion = "(devel)"
			}
			break
		}
	}
	require.NotEmpty(t, linkedJobDBVersion, "built CLI must link JobDB")
	// These processes own isolated fixture jobs, not the live managed task.
	// Keep the tool and Git credential environment for building the binary.
	var cliEnv []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "C2J_CURRENT_") || strings.HasPrefix(key, "C2J_CHILD_JOB_") || strings.HasPrefix(key, "C2J_WORKSPACE_") {
			continue
		}
		switch key {
		case "C2J_JOBDB", "C2J_TENANT_ID", "C2J_OBJECT_OUTBOX":
			continue
		}
		cliEnv = append(cliEnv, kv)
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "initial"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	recipeFile := filepath.Join(t.TempDir(), "recipe.yaml")
	require.NoError(t, os.WriteFile(recipeFile, []byte("id: provenance\ninput_schema: {prompt: {type: string}}\nsequence: []\noutputs: {ok: true}\n"), 0600))
	cli := func(t *testing.T, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = cliEnv
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return out
	}
	require.Equal(t, "c2j version "+version+"\n", string(cli(t, "version")))
	for _, mode := range []string{"run", "run one", "run with-lease", "submit --run", "run mismatch", "run with-lease mismatch"} {
		t.Run(mode, func(t *testing.T) {
			rt := toy.New()
			server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(rt)))
			defer server.Close()
			uri := server.URL + "/provenance"
			cli(t, "cell", "create", "app", "--repo", celltest.Repo(repo), "--jobdb", uri)
			args := []string{"submit", "--jobdb", uri, "--cell", "app", "--cell-repo", celltest.Repo(repo), "--advanced-recipe-file", recipeFile}
			key := jobdb.JobKey{TenantId: "provenance"}
			if mode == "submit --run" {
				out := cli(t, append(args, "--run", "check provenance")...)
				match := regexp.MustCompile(`job_id=(\S+)`).FindSubmatch(out)
				require.Len(t, match, 2, "%s", out)
				key.JobId = string(match[1])
			} else {
				out := cli(t, append(args, "--json", "check provenance")...)
				var submitted struct {
					JobID string `json:"job_id"`
				}
				require.NoError(t, json.Unmarshal(out, &submitted), "%s", out)
				key.JobId = submitted.JobID
				// Provenance checks need no health listener or fixed host port.
				mismatch := strings.HasSuffix(mode, " mismatch")
				var before []jobdb.Chapter
				if mismatch {
					seedWorker := jobworkflow.ExecutionWorker{JobType: "recipe", Execute: func(c jobworkflow.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
						_, e := workflow.ValueCall(c, "cell-context", compiler.CellContextTaskType, jobdb.RunPolicy{}, map[string]any{"compiler": "recorded-build"})
						return in, e
					}}
					seed, e := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: seedWorker, ConsumerVersions: map[string]string{"c2j": "recorded-build"}})
					require.NoError(t, e)
					waiting, e := seed.Run(nil)
					require.NoError(t, e)
					require.Equal(t, jobworkflow.JobRunSuspended, waiting.Status)
					before, e = rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
					require.NoError(t, e)
				}
				// These build-identity checks do not need the shared health listener.
				run := append(strings.Fields(strings.TrimSuffix(mode, " mismatch")), "--jobdb", uri, "--job-id", key.JobId, "--ci", "--health-port", "0")
				if strings.HasPrefix(mode, "run with-lease") {
					client, err := remote.New(server.URL, server.Client())
					require.NoError(t, err)
					lease, err := client.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "dispatcher", Routes: []jobdb.Route{{JobType: "recipe"}, {JobType: "recipe", TaskType: compiler.CellContextTaskType}}, LeaseDuration: time.Minute})
					require.NoError(t, err)
					capability, err := remote.ExportLease(lease)
					require.NoError(t, err)
					encoded, err := capability.Encode()
					require.NoError(t, err)
					leaseFile := filepath.Join(t.TempDir(), "lease.json")
					require.NoError(t, os.WriteFile(leaseFile, encoded, 0600))
					run = append(run, "--lease-file", leaseFile)
				}
				if mismatch {
					command := exec.CommandContext(ctx, binary, run...)
					command.Env = cliEnv
					command.Dir = repo
					var stdout, stderr bytes.Buffer
					command.Stdout = &stdout
					command.Stderr = &stderr
					e := command.Run()
					var exit *exec.ExitError
					require.ErrorAs(t, e, &exit)
					require.Equal(t, 1, exit.ExitCode())
					var diagnostic struct {
						Kind     string              `json:"kind"`
						Versions map[string]string   `json:"versions"`
						Writers  []map[string]string `json:"writers"`
					}
					require.NoError(t, json.Unmarshal(stderr.Bytes(), &diagnostic), stderr.String())
					require.Equal(t, "execution_infrastructure_error", diagnostic.Kind)
					require.Equal(t, version, diagnostic.Versions["c2j"])
					require.Equal(t, commit, diagnostic.Versions["c2j.commit"])
					require.Equal(t, linkedJobDBVersion, diagnostic.Versions["jobdb"])
					require.Len(t, diagnostic.Writers, 1)
					require.Equal(t, "recorded-build", diagnostic.Writers[0]["c2j"])
					require.NotContains(t, stderr.String(), "Error:")
					require.Equal(t, 1, strings.Count(stderr.String(), "\n"))
					after, e := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
					require.NoError(t, e)
					require.Equal(t, before, after)
					info, e := rt.GetJob(ctx, key)
					require.NoError(t, e)
					require.NotEqual(t, jobdb.JobStatusCompleted, info.Status)
					return
				}
				cli(t, run...)
			}
			info, err := rt.GetJob(ctx, key)
			require.NoError(t, err)
			require.Equal(t, jobdb.JobStatusCompleted, info.Status)
			history, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key})
			require.NoError(t, err)
			found := map[string]bool{}
			for _, event := range history.Execution {
				r := event.Record
				if r.Kind == "header" || r.Kind == "activation" {
					require.NotNil(t, r.Versions)
					require.Equal(t, version, r.Versions.ConsumerVersions["c2j"])
					require.Equal(t, commit, r.Versions.ConsumerVersions["c2j.commit"])
					found[r.Kind] = true
				}
			}
			require.Equal(t, map[string]bool{"header": true, "activation": true}, found)
		})
	}
}
