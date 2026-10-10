package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/nixtest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/toolenv"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRealNixExtension(t *testing.T) {
	if !nixtest.InContainer(t) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	run := func(command string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
		require.NoError(t, err, "%s %v: %s", command, args, out)
		return strings.TrimSpace(string(out))
	}
	nix := func(args ...string) string {
		t.Helper()
		return run("nix", append([]string{"--extra-experimental-features", "nix-command flakes"}, args...)...)
	}
	cacheRoot := t.TempDir()
	t.Setenv("C2J_TOOL_CACHE_DIR", cacheRoot)
	system, err := toolenv.NixSystem()
	require.NoError(t, err)
	shell, err := exec.LookPath("sh")
	require.NoError(t, err)
	shell, err = filepath.EvalSymlinks(shell)
	require.NoError(t, err)
	// Simulate a binary release served separately from the small definition repo.
	payload := []byte("#!" + shell + "\nIFS= read -r input\nprintf '%s\\n' \"$input\"\n")
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	hash := sha256.Sum256(payload)
	manifest := []byte(`{"name":"echo","command":["bin/op"],"input_schema":{"type":"object","required":["message"],"properties":{"message":{"type":"string","default":"hello"}}},"output_schema":{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}}`)
	definition := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(definition, "op.json"), manifest, 0600))
	flake := fmt.Sprintf(`{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/b6018f87da91d19d0ab4cf979885689b469cdd41";
  outputs = { nixpkgs, ... }: let
    pkgs = import nixpkgs { system = %q; };
    manifest = builtins.fromJSON (builtins.readFile ./op.json);
  in { packages.%s.echo = pkgs.stdenvNoCC.mkDerivation {
    pname = "c2j-echo-test";
    version = "1";
    src = pkgs.fetchurl { url = %q; hash = %q; };
    dontUnpack = true;
    dontFixup = true;
    installPhase = ''
      install -Dm755 "$src" "$out/bin/op"
      install -Dm644 ${./op.json} "$out/share/c2j/op.json"
    '';
    passthru.c2j = manifest;
  }; };
}`, system, system, server.URL+"/binary", "sha256-"+base64.StdEncoding.EncodeToString(hash[:]))
	require.NoError(t, os.WriteFile(filepath.Join(definition, "flake.nix"), []byte(flake), 0600))
	selector := "nix:path:" + definition + "#echo"
	described, err := extops.Resolve(ctx, selector, extops.ResolveOptions{})
	require.NoError(t, err)
	require.False(t, described.Ready())
	require.EqualValues(t, 0, downloads.Load(), "manifest lookup must not fetch the binary")
	require.NoFileExists(t, described.Nix.StorePath)

	// This is the publisher/CI step: only CI is allowed to build the package.
	nix("build", "--max-jobs", "1", "--builders", "", "--option", "sandbox", "false", "--no-link", strings.TrimPrefix(selector, "nix:"))
	require.Greater(t, downloads.Load(), int32(0))
	server.Close()
	binaryCache := "file://" + t.TempDir()
	nix("copy", "--to", binaryCache, described.Nix.StorePath)
	run("nix-store", "--delete", described.Nix.StorePath)
	// The fixture cache is unsigned and local. Production uses trusted cache keys.
	t.Setenv("NIX_CONFIG", "substituters = "+binaryCache+"\nrequire-sigs = false\nmax-jobs = 1\nbuilders = ssh://c2j-no-build.invalid\n")

	// Missing cache entries must fail setup; they cannot fall back to the source.
	t.Run("missing_prebuilt", func(t *testing.T) {
		t.Setenv("NIX_CONFIG", "substituters =\nmax-jobs = 1\nbuilders = ssh://c2j-no-build.invalid\n")
		_, _, err := extops.PrepareNixOp(ctx, described)
		require.Error(t, err)
	})
	// Realize the recorded output with no access to the original definition.
	hidden := definition + "-hidden"
	require.NoError(t, os.Rename(definition, hidden))
	prepared, reused, err := extops.PrepareNixOp(ctx, described)
	require.NoError(t, err)
	require.False(t, reused)
	require.True(t, prepared.Ready())
	// Detect metadata/output disagreement before ever executing the package.
	bad := *described
	p := *bad.Nix
	p.Manifest = json.RawMessage(`{"name":"different"}`)
	bad.Nix = &p
	_, _, err = extops.PrepareNixOp(ctx, &bad)
	require.ErrorContains(t, err, "differs from passthru.c2j")
	require.NoError(t, os.Rename(hidden, definition))

	// Drop the root and output so the real recipe must fetch from the cache.
	require.NoError(t, os.RemoveAll(cacheRoot))
	run("nix-store", "--delete", described.Nix.StorePath)
	// Deliberately make installation slower than the op's entire timeout.
	nixBinary, err := exec.LookPath("nix")
	require.NoError(t, err)
	delayBin := t.TempDir()
	delayScript := "#!/bin/sh\ncase \" $* \" in *' build '*) sleep 0.4;; esac\nexec " + nixBinary + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(delayBin, "nix"), []byte(delayScript), 0700))
	t.Setenv("PATH", delayBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	withRegisteredOps(t, extops.GetExecutionOp())
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
	require.NoError(t, err)
	engine := newToyEngineWithWorkSet(t, "nix-ops", ws, nil)
	job, git := GenerateTestContext()
	start := func(rec *recipe.Recipe) jobdb.JobKey {
		key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "nix-ops", RecipeName: rec.GetMetdata().ID, JobContext: job, GitRef: git.ParentRef}, engine, *rec)
		require.NoError(t, err)
		require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 30*time.Second, key, engine))
		return key
	}
	invalid, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf("id: invalid\nop: %s\ninputs: {message: 42}\n", selector)))
	require.NoError(t, err)
	invalidKey := start(invalid)
	_, err = swfutil.JobResult(ctx, engine, invalidKey)
	require.Error(t, err)
	require.NoFileExists(t, described.Nix.StorePath, "invalid inputs must not fetch the payload")
	rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf(`id: nix-op
sequence:
  - id: echo
    op: %s
    timeout: 250ms
  - id: branches
    state:
      initial: idle
      states:
        idle:
          sequence: []
        unused:
          op: nix:github:does-not-exist/no-op#missing
outputs:
  message: "${{ sequence.echo.outputs.message }}"
`, selector)))
	require.NoError(t, err)
	key := start(rec)
	result, err := swfutil.JobResult(ctx, engine, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	require.JSONEq(t, `{"message":"hello"}`, string(raw))
	history, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
	require.NoError(t, err)
	missSeen, setupSeen := false, false
	for _, task := range history.Attempts[0].Tasks {
		if strings.HasPrefix(task.TaskType, "extension_execution:") && !setupSeen {
			missSeen = true
		}
		if task.TaskType == workerops.ToolSetupTaskType {
			require.True(t, missSeen, "package setup must follow an uncached activity")
			setupSeen = true
			var setup workerops.ToolSetupResult
			require.NoError(t, json.Unmarshal(task.Attempts[0].Output.Data, &setup))
			require.Equal(t, described.Nix.StorePath, setup.Diagnostics.Tools[0].Identity)
			require.GreaterOrEqual(t, setup.Diagnostics.Tools[0].WallMS, int64(350))
			require.NotNil(t, setup.Diagnostics.TaskOrdinal)
		}
	}
	require.True(t, setupSeen)
	// Cached replay needs neither definitions, installed output, nor any cache.
	require.NoError(t, os.RemoveAll(cacheRoot))
	run("nix-store", "--delete", described.Nix.StorePath)
	require.NoError(t, os.RemoveAll(definition))
	t.Setenv("PATH", t.TempDir())
	replay, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	replayed, err := replay.GetData()
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(replayed))
}
