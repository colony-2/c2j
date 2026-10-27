//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return out
	}
	require.Equal(t, "c2j version "+version+"\n", string(cli(t, "version")))
	for _, mode := range []string{"run", "run one", "run with-lease", "submit --run"} {
		t.Run(mode, func(t *testing.T) {
			rt := toy.New()
			server := httptest.NewServer(remote.NewServer(rt))
			defer server.Close()
			uri := server.URL + "/provenance"
			args := []string{"submit", "--jobdb", uri, "--cell", repo, "--advanced-recipe-file", recipeFile}
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
				run := append(strings.Fields(mode), "--jobdb", uri, "--job-id", key.JobId, "--ci")
				if mode == "run with-lease" {
					client, err := remote.New(server.URL, server.Client())
					require.NoError(t, err)
					lease, err := client.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "dispatcher", Routes: []jobdb.Route{{JobType: "recipe"}}, LeaseDuration: time.Minute})
					require.NoError(t, err)
					capability, err := remote.ExportLease(lease)
					require.NoError(t, err)
					encoded, err := capability.Encode()
					require.NoError(t, err)
					leaseFile := filepath.Join(t.TempDir(), "lease.json")
					require.NoError(t, os.WriteFile(leaseFile, encoded, 0600))
					run = append(run, "--lease-file", leaseFile)
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
