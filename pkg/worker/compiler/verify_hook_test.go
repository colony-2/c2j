package compiler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// Native YAML mocks cannot inject typed cancellation errors. Exercise the actual
// hook through the compiler with the same scripted task context as catch tests.
func TestVerifyHookCancellation(t *testing.T) {
	withRegisteredOps(t, commandop.GetOp())
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	for stop := 0; stop < 5; stop++ {
		t.Run([]string{"environment", "modules", "integration", "skills", "summary"}[stop], func(t *testing.T) {
			jobCtx, gitCtx := GenerateTestContext()
			script := &catchScriptedJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "verify-cancel"}}
			for i := 0; i < stop; i++ {
				script.results = append(script.results, catchTaskResult{out: catchOutput(t, gitCtx, map[string]interface{}{
					"success": true, "exit_code": 0, "timed_out": false, "stdout": "", "error_message": "",
				})})
			}
			script.results = append(script.results, catchTaskResult{err: context.Canceled})
			inputs := map[string]interface{}{"api_version": "c2.build-hook/v1", "request": "verify", "target_directory": ".", "candidate": "candidate", "base": "base", "documents": map[string]interface{}{}}
			out, _, runErr := ExecuteRecipe(catchWorkflowContext(script), *r, inputs, jobCtx, gitCtx)
			require.ErrorIs(t, runErr, context.Canceled)
			require.Empty(t, out)
			require.Equal(t, stop+1, script.calls, "cancellation must not run later checks or reporting")
		})
	}
}

func TestVerifyHookReport(t *testing.T) {
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	state := r.RecipeImpl.(*recipe.RecipeState)
	node := state.States.States["summarize"].Node.NodeImpl.(*recipe.NodeOp)
	root := t.TempDir()
	inbox, outbox := filepath.Join(root, "inbox"), filepath.Join(root, "outbox")
	require.NoError(t, os.Mkdir(inbox, 0700))
	require.NoError(t, os.Mkdir(outbox, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "environment.log"), []byte("Go 1.26.1\nNode v24.0.0\nOptional Docker absent\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "tests.log"), []byte(strings.Repeat("large output\n", 10000)+"--- SKIP: TestDocker (0.00s)\n"), 0600))
	cmd := exec.Command("bash", "-c", node.Inputs["run"].(string))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "INBOX="+inbox, "OUTBOX="+outbox, "CANDIDATE=candidate; $(touch never-execute)", "BASE=base", "VERDICT=failed")
	for i := 0; i < 4; i++ {
		cmd.Env = append(cmd.Env, fmt.Sprintf("RESULT_%d=failed", i), fmt.Sprintf("DETAIL_%d={\"success\":false}", i))
	}
	stdout, err := cmd.CombinedOutput()
	require.NoError(t, err, string(stdout))
	require.LessOrEqual(t, len(stdout), 8192)
	report, err := os.ReadFile(filepath.Join(outbox, "summary.md"))
	require.NoError(t, err)
	for _, text := range []string{"candidate; $(touch never-execute)", "Go 1.26.1", "Node v24.0.0", "--- SKIP: TestDocker", "Evidence unavailable: modules.log", "Evidence unavailable: skills.log", "does not certify the GitHub matrix"} {
		require.Contains(t, string(report), text)
	}
	_, err = os.Stat(filepath.Join(root, "never-execute"))
	require.True(t, os.IsNotExist(err), "candidate input must be treated as data")
}

func TestVerifyHookSubmissionEnvironmentAndGate(t *testing.T) {
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	state := r.RecipeImpl.(*recipe.RecipeState)
	node := state.States.States["test_integration"].Node.NodeImpl.(*recipe.NodeOp)
	require.Equal(t, "90m", node.Inputs["timeout"])
	for _, fixture := range []struct {
		name            string
		failGate        string
		missingWaitGoal bool
	}{
		{name: "healthy"},
		{name: "broker-gate", failGate: "TestBrokerOnlyCLI"},
		{name: "helper-gate", failGate: "TestLeaseBoundRestartHelperSubmission"},
		{name: "inspection-gate", failGate: "TestTaskInspectionUsesBackendReadPermissions"},
		{name: "health-gate", failGate: "^TestHealth"},
		{name: "managed-regressions-gate", failGate: "^Test(JobInspectionExpiredLifetimeBeforeDispatch"},
		{name: "extension-wait-gate", failGate: "^TestExtensionWait"},
		{name: "git-cleanup-gate", failGate: "^TestChanges"},
		{name: "scenario-gate", failGate: "TestPnpmVerifyHook"},
		{name: "catalog-gate", failGate: "TestExecutionBaseCatalog"},
		{name: "submission-inputs-gate", failGate: "^TestRecipeSubmissionInputs"},
		{name: "compact-gate", failGate: "Test(Compact|ExecutionFailurePresentation"},
		{name: "fixture-gate", failGate: "Run_EmbedExtensionFailureCompletesWithOriginalError"},
		{name: "artifact-file-gate", failGate: "TestArtifactFile"},
		{name: "artifact-metadata-gate", failGate: "TestNixOpDescriptionDoesNotInstall"},
		{name: "packaged-artifact-gate", failGate: "TestRealNixArtifactFiles"},
		{name: "fixture-source-gate", failGate: "Test(FixtureSource|NativeRuntimeFixtureSourceModes)"},
		{name: "full-suite", failGate: "-tags=integration -p 2"},
		{name: "missing-wait-help", missingWaitGoal: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			outbox := filepath.Join(root, "outbox")
			require.NoError(t, os.Mkdir(bin, 0700))
			require.NoError(t, os.Mkdir(outbox, 0700))
			// Run the hook's actual shell with a recording Go command. It rejects leaked
			// live routing and proves a failed process gate prevents later checks.
			// A successful build installs the same recorder as the scenario CLI.
			mock := `#!/bin/bash
set -eu
for variable in ${!C2J_CURRENT_@} ${!C2J_CHILD_JOB_@} ${!C2J_WORKSPACE_@}; do exit 91; done
test -z "${C2J_JOBDB+x}${C2J_TENANT_ID+x}${C2J_OBJECT_OUTBOX+x}${C2J_REQUIRE_EXECUTION_BASE_CATALOG+x}"
if [ -n "${C2J_TEST_SUBMIT_BINARY+x}" ]; then
  [[ "$*" == *TestLeaseParentage* ]]
  [[ "$C2J_TEST_SUBMIT_BINARY" == */c2j-parentage ]]
fi
test "$GOWORK" = off
printf '%s %s\n' "${0##*/}" "$*" >> "$COMMANDS"
if [ -n "$FAIL_GATE" ] && [[ "$*" == *"$FAIL_GATE"* ]]; then exit 42; fi
if [[ "$*" == *'TestHistory|TestInputHistory'* ]]; then
  cat "$HISTORY_GATE_EVENTS"
fi
if [[ "$*" == *'-run ^TestExtensionWait' ]]; then
  for pkg in extensionwait ops/extensions worker/ops metricsruntime secrets executionruntime worker/compiler childbroker; do
    printf '{"Action":"run","Package":"github.com/colony-2/c2j/pkg/%s","Test":"TestExtensionWaitFixture"}\n' "$pkg"
    printf '{"Action":"pass","Package":"github.com/colony-2/c2j/pkg/%s","Test":"TestExtensionWaitFixture"}\n' "$pkg"
  done
  for backend in toy sqlite remote; do
    for target in job_target goal_target; do
      for action in run pass; do
        printf '{"Action":"%s","Package":"github.com/colony-2/c2j/pkg/executionruntime","Test":"TestExtensionWaitBindingAdmission/%s/%s"}\n' "$action" "$backend" "$target"
      done
    done
  done
fi
if [[ "$*" == *TestLeaseParentage* ]]; then
  for item in cmd/c2j/internal/submitjob:TestLeaseParentageMissingBroker cmd/c2j/internal/submitjob:TestLeaseParentageStandalone cmd/c2j/internal/submitjob:TestLeaseParentageBrokerBoundaries cmd/c2j/internal/submitjob:TestSubmissionWaitBrokerLeaseRoutes cmd/c2j/internal/submitjob:TestRun_BrokerSubmissionAndConfiguredImmediateExecution pkg/childbroker:TestBrokerOnlyCLI pkg/childbroker:TestLeaseParentageLostOrdinaryResponse pkg/executionruntime:TestLeaseParentageSuppliedLease pkg/worker/compiler:TestLeaseParentageWorkerCLI pkg/worker/compiler:TestWorkerBrokerSubmitsIncomingAndLiveWorkspace cmd/c2j/internal/cmd:TestSubmitHelpPromotesConventions; do
    pkg="${item%%:*}"; test_name="${item#*:}"
    printf '{"Action":"run","Package":"github.com/colony-2/c2j/%s","Test":"%s"}\n' "$pkg" "$test_name"
    printf '{"Action":"pass","Package":"github.com/colony-2/c2j/%s","Test":"%s"}\n' "$pkg" "$test_name"
  done
fi
if [[ "$*" == *TestWorkspaceDiff* ]]; then
  for pkg in pkg/jobinspect pkg/childbroker cmd/c2j/internal/cmd cmd/c2j pkg/worker/compiler; do
    case "$pkg" in
      pkg/jobinspect) test_name=TestWorkspaceDiffNativeProbe ;;
      pkg/childbroker) test_name=TestWorkspaceDiffHelperProbe ;;
      cmd/c2j/internal/cmd) test_name=TestWorkspaceDiffCLI_Probe ;;
      cmd/c2j) test_name=TestWorkspaceDiffCLIProcessProbe ;;
      pkg/worker/compiler) test_name=TestWorkspaceDiffCompilerProbe ;;
    esac
    printf '{"Action":"run","Package":"github.com/colony-2/c2j/%s","Test":"%s"}\n' "$pkg" "$test_name"
    printf '{"Action":"pass","Package":"github.com/colony-2/c2j/%s","Test":"%s"}\n' "$pkg" "$test_name"
  done
  for item in pkg/jobinspect:TestWorkspaceDiffPatchlessObserverEvidence pkg/jobinspect:TestWorkspaceDiffNativePatchlessObservers pkg/jobinspect:TestWorkspaceDiffNativePatchlessObserverRejections pkg/jobinspect:TestWorkspaceDiffNativePatchlessObserverSources pkg/jobinspect:TestWorkspaceDiffNativePatchlessObserverAttemptsAndCutoffs pkg/worker/compiler:TestWorkspaceDiffCompilerReviewScopeWorkspace pkg/worker/compiler:TestWorkspaceDiffCompilerReviewFailedScope pkg/childbroker:TestWorkspaceDiffHelperReviewResponseContract; do
    pkg="${item%%:*}"; test_name="${item#*:}"
    printf '{"Action":"run","Package":"github.com/colony-2/c2j/%s","Test":"%s"}\n' "$pkg" "$test_name"
    printf '{"Action":"pass","Package":"github.com/colony-2/c2j/%s","Test":"%s"}\n' "$pkg" "$test_name"
  done
  for backend in SQLite HTTP; do
    for variant in omitted-parent parent-present; do
      for selection in producer observer-one observer-two latest; do
        test_name="TestWorkspaceDiffNativePatchlessObservers/$backend/$variant/$selection"
        printf '{"Action":"run","Package":"github.com/colony-2/c2j/pkg/jobinspect","Test":"%s"}\n' "$test_name"
        printf '{"Action":"pass","Package":"github.com/colony-2/c2j/pkg/jobinspect","Test":"%s"}\n' "$test_name"
      done
    done
  done
fi
if [ "$1" = build ]; then
  shift
  if [ "$1" = -buildvcs=false ] || [ "$1" = -mod=readonly ]; then shift; fi
  test "$#" = 3 && test "$1" = -o && test "$3" = ./cmd/c2j
  cp -- "$0" "$2"
  chmod 0700 "$2"
fi
if [[ "$*" == *standalone-squash-history.test.json* ]]; then
  test "$GIT_TEMPLATE_DIR" = "$PWD/pkg/recipetest/testdata/native-scenarios/standalone-squash-template"
elif [[ "$*" == *"-tags=integration -p 2"* ]]; then
  test -z "${GIT_TEMPLATE_DIR+x}"
fi
if [ "$1" = submit ]; then
  test "$#" = 2 && test "$2" = --help
  for flag in --wait-job --wait-job-success --wait-goal --wait-until; do
    if [ "$MISSING_WAIT_GOAL" = true ] && [ "$flag" = --wait-goal ]; then continue; fi
    printf '  %s value\n' "$flag"
  done
fi
`
			historyEvents := filepath.Join(root, "history-events.json")
			require.NoError(t, os.WriteFile(historyEvents, inputHistoryGateEvidence("passed"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(mock), 0700))
			commands := filepath.Join(root, "commands")
			cmd := exec.Command("bash", "-c", node.Inputs["run"].(string))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OUTBOX="+outbox, "COMMANDS="+commands, "FAIL_GATE="+fixture.failGate, fmt.Sprintf("MISSING_WAIT_GOAL=%t", fixture.missingWaitGoal), "C2J_CURRENT_JOB_ID=live", "C2J_CHILD_JOB_TOKEN=live", "C2J_WORKSPACE_ROOT=live", "C2J_JOBDB=embed:///", "C2J_TENANT_ID=live", "C2J_OBJECT_OUTBOX=live", "C2J_TEST_SUBMIT_BINARY=/stale/c2j", "C2J_REQUIRE_EXECUTION_BASE_CATALOG=true")
			cmd.Env = append(cmd.Env, "HISTORY_GATE_EVENTS="+historyEvents)
			out, err := cmd.CombinedOutput()
			log, logErr := os.ReadFile(filepath.Join(outbox, "tests.log"))
			require.NoError(t, logErr)
			if fixture.failGate != "" {
				require.Error(t, err)
				expectedExit := 42
				if fixture.failGate == "^TestExtensionWait" {
					expectedExit = 1
				}
				require.Equal(t, expectedExit, cmd.ProcessState.ExitCode(), "%s\n%s", out, log)
			} else if fixture.missingWaitGoal {
				require.Error(t, err)
				require.Equal(t, 1, cmd.ProcessState.ExitCode())
			} else {
				require.NoError(t, err, "%s\n%s", out, log)
			}
			raw, err := os.ReadFile(commands)
			require.NoError(t, err)
			// Retained history precedes the existing regression chain, using the
			// built executable and then the race checks. Keep all legacy gate
			// ordering and stop-on-failure assertions below.
			if fixture.name == "healthy" {
				require.Contains(t, string(raw), "go test -mod=readonly -count=3 -v -tags=integration -timeout=5m ./pkg/childbroker ./pkg/worker/compiler ./pkg/executionruntime -run ^Test(JobInspectionExpiredLifetimeBeforeDispatch|MetricsHelperAuthenticationExactnessAndIncompleteHistory|ExtensionWaitManifestTimeout|InputHistoryHelperExpiredLifetimeBeforeDispatch|ExtensionWaitReadinessClockRecheck)$\n")
			}
			lines := strings.SplitN(string(raw), "\n", 4)
			require.Len(t, lines, 4)
			require.True(t, strings.HasPrefix(lines[0], "go build -mod=readonly -o "))
			require.True(t, strings.HasSuffix(lines[0], "/c2j-input-history ./cmd/c2j"))
			require.Contains(t, lines[1], "-run ^(TestHistory|TestInputHistory)")
			require.Contains(t, lines[2], "go test -race -mod=readonly")
			require.True(t, strings.HasSuffix(lines[2], "-run ^TestInputHistory"))
			raw = []byte(lines[3])
			helperGate := "go test -mod=readonly -count=1 -v -timeout=5m ./pkg/executionruntime -run ^TestLeaseBoundRestartHelperSubmission$\n"
			inspectionGate := "go test -mod=readonly -count=1 -v -timeout=5m ./pkg/executionruntime -run ^TestTaskInspectionUsesBackendReadPermissions$\n"
			healthGate := "go test -mod=readonly -race -count=1 -v -timeout=10m ./cmd/c2j/internal/cmd ./cmd/c2j/internal/runjob ./pkg/task ./pkg/executionruntime ./pkg/worker/ops ./pkg/ops/extensions -run ^TestHealth|^TestPrompt\n"
			require.True(t, strings.HasPrefix(string(raw), helperGate), "helper regression must run first, after identity isolation")
			gates := []string{
				"TestLeaseBoundRestartHelperSubmission$",
				"TestTaskInspectionUsesBackendReadPermissions$",
				"^TestHealth",
				"^Test(JobInspectionExpiredLifetimeBeforeDispatch",
				"^TestExtensionWait",
				"^TestChanges(J30J32GitDiagnosticIdentity|J32L07GitProcessGroupCleanup|J30L07RepositoryTransferCleanup|ProcessExitObservation)$",
				"^TestPnpmVerifyHook$",
				"^TestExecutionBaseCatalog",
				"^TestRecipeSubmissionInputs",
				"Test(Compact|ExecutionFailurePresentation",
				"^Test(Run_EmbedExtensionFailureCompletesWithOriginalError|RecipeFixtures)",
				"^TestArtifactFile|^TestNixOpDescriptionDoesNotInstall$",
				"^TestRealNixArtifactFiles$",
				"^TestBrokerOnlyCLI",
				"^Test(FixtureSource|NativeRuntimeFixtureSourceModes)",
			}
			previous, stopped := -1, false
			for _, gate := range gates {
				if stopped {
					require.NotContains(t, string(raw), gate, "failed gate must prevent every later gate")
					continue
				}
				position := strings.Index(string(raw), gate)
				require.Greater(t, position, previous, "regression gates must run in order: %s", gate)
				previous = position
				if fixture.failGate != "" && strings.Contains(gate, fixture.failGate) {
					stopped = true
					lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
					require.Contains(t, lines[len(lines)-1], gate, "failed gate must be the last command")
				}
			}
			if fixture.failGate == "TestLeaseBoundRestartHelperSubmission" {
				require.Equal(t, helperGate, string(raw), "failed helper regression must prevent every later command")
			} else {
				require.True(t, strings.HasPrefix(string(raw), helperGate+inspectionGate), "inspection compatibility must run before later checks")
				if fixture.failGate == "TestTaskInspectionUsesBackendReadPermissions" {
					require.Equal(t, helperGate+inspectionGate, string(raw), "failed inspection regression must prevent every later command")
				} else {
					require.True(t, strings.HasPrefix(string(raw), helperGate+inspectionGate+healthGate), "health race checks must follow helper and inspection regressions")
					if fixture.failGate == "^TestHealth" {
						require.Equal(t, helperGate+inspectionGate+healthGate, string(raw), "failed health regression must prevent every later command")
					}
				}
			}
			if fixture.failGate == "-tags=integration -p 2" {
				require.Contains(t, string(raw), "go test -count=1 -json -tags=integration -p 2 -timeout=30m ./...\n")
				require.NotContains(t, string(raw), "c2j submit --help\n", "full-suite failure must stop verification before the final help check")
			} else if fixture.failGate != "" {
				require.NotContains(t, string(raw), "-tags=integration -p 2")
				require.NotContains(t, string(raw), "./internal/retirementcheck")
				require.NotContains(t, string(raw), "go build")
				require.NotContains(t, string(raw), "c2j test")
			} else {
				require.Contains(t, string(raw), "Test(Compact|ExecutionFailurePresentation")
				require.Contains(t, string(raw), "./pkg/ops/executiondiag ./pkg/ops/process ./pkg/recipe")
				require.Contains(t, string(raw), "go test -count=1 -v -tags=integration -timeout=10m ./cmd/c2j/internal/submitjob ./pkg/input/test-fixtures -run ^Test(Run_EmbedExtensionFailureCompletesWithOriginalError|RecipeFixtures)")
				require.Contains(t, string(raw), "-run ^TestSubmission")
				require.Contains(t, string(raw), "go test -count=1 -v -timeout=10m ./internal/retirementcheck")
				require.Less(t, strings.Index(string(raw), "-run ^TestSubmission"), strings.Index(string(raw), "./internal/retirementcheck"))
				require.Contains(t, string(raw), "go test -count=1 -v -timeout=10m ./internal/pathutil ./internal/portability ./pkg/shellcmd ./pkg/toolenv")
				require.Contains(t, string(raw), "go test -count=1 -v -timeout=10m ./pkg/worker/compiler -run ^TestRealRepositoryNativePackages$")
				require.Contains(t, string(raw), "./pkg/git/squashrebasemerge ./pkg/worker/compiler -run ^TestStandaloneSquash")
				require.Contains(t, string(raw), "go test -count=1 -json -tags=integration -p 2 -timeout=30m ./...\n")
				require.Contains(t, string(raw), "TestWorkspaceDiff|ExampleWorkspaceDiff")
				require.Contains(t, string(raw), "go build -buildvcs=false -o ")
				require.Contains(t, string(raw), "c2j submit --help\n")
				require.Contains(t, string(raw), "./pkg/recipetest ./cmd/c2j/internal/testjob -run ^Test(FixtureSource|NativeRuntimeFixtureSourceModes)")
				require.Contains(t, string(raw), "c2j test validate --file testdata/repros/fixture-source-modes/modes.test.yaml\n")
				require.Contains(t, string(raw), "c2j test run --file testdata/repros/fixture-source-modes/modes.test.yaml --out-dir ")
				require.Equal(t, 6, strings.Count(string(raw), "c2j test validate --file "))
				require.Equal(t, 6, strings.Count(string(raw), "c2j test run --file "))
				for _, suite := range []string{"admission-ordering", "advancement", "cancellation", "worker-recovery", "standalone-squash-history"} {
					fixture := "pkg/recipetest/testdata/native-scenarios/" + suite + ".test.json"
					require.Contains(t, string(raw), "c2j test validate --file "+fixture+"\n")
					timeout := "90s"
					if suite == "standalone-squash-history" {
						timeout = "3m"
					}
					require.Contains(t, string(raw), "c2j test run --file "+fixture+" --parallelism 1 --case-timeout "+timeout+" --out-dir ")
				}
			}
		})
	}
}

func inputHistoryGateEvidence(mode string) []byte {
	var evidence strings.Builder
	for pkg, names := range map[string][]string{
		"pkg/jobinputs":           {"TestInputHistoryProtocol", "TestInputHistoryVerifiedBytes", "TestInputHistoryBlockedCancellation"},
		"pkg/input":               {"TestHistorySQLiteRetainedWaitSnapshotsAndProducerBytes"},
		"pkg/input/historybridge": {"TestInputHistoryAdapterSnapshotsAndAuthority", "TestInputHistoryPrivateRestoreFailures", "TestInputHistoryConcurrentReaderIsolation"},
		"pkg/executionruntime":    {"TestInputHistoryLeaseLifetime", "TestInputHistoryRenewalPreservesAuthority", "TestInputHistoryActiveTaskCancellation", "TestInputHistoryCachedOperationsLifetime"},
		"pkg/childbroker":         {"TestInputHistoryHelperAuthenticationAndClaims", "TestInputHistoryHelperFinalAuthority", "TestInputHistoryHelperTransport", "TestInputHistoryHelperExpiredLifetimeBeforeDispatch", "TestInputHistoryHelperExpiredLifetimeBeforeDispatch/request-canceled/list", "TestInputHistoryHelperExpiredLifetimeBeforeDispatch/request-canceled/show", "TestInputHistoryHelperExpiredLifetimeBeforeDispatch/request-canceled/artifact"},
		"cmd/c2j/internal/cmd":    {"TestInputHistoryAtomicFiles", "TestInputHistoryCLIValidation"},
		"cmd/c2j/internal/runjob": {"TestInputHistoryRunnerComposition"},
		"pkg/recipetest":          {"TestInputHistoryRecipeTestComposition"},
		"cmd/c2j":                 {"TestInputHistoryCLIProcessRetainedRecords", "TestInputHistoryCLIProcessTransport", "TestInputHistoryCLIProcessAuthorityBeforeCompletion", "TestInputHistoryCLIProcessRemoteOrigin", "TestInputHistoryCLIProcessRetainedRecords/published-http", "TestInputHistoryCLIProcessRemoteOrigin/denied", "TestInputHistoryCLIProcessRemoteOrigin/wrapped-denied", "TestInputHistoryCLIProcessRemoteOrigin/producer-denied", "TestInputHistoryCLIProcessRemoteOrigin/producer-open-denied", "TestInputHistoryCLIProcessRemoteOrigin/short", "TestInputHistoryCLIProcessRemoteOrigin/legacy-server-denial"},
	} {
		for _, name := range names {
			if (mode == "missing" && name == "TestInputHistoryCLIProcessRemoteOrigin") || (mode == "missing-published-server" && name == "TestInputHistoryCLIProcessRetainedRecords/published-http") || (mode == "missing-history-lifetime" && strings.HasPrefix(name, "TestInputHistoryHelperExpiredLifetimeBeforeDispatch")) {
				continue
			}
			fmt.Fprintf(&evidence, "{\"Action\":\"run\",\"Package\":\"github.com/colony-2/c2j/%s\",\"Test\":%q}\n", pkg, name)
			if mode != "unfinished" {
				action := "pass"
				if name == "TestInputHistoryCLIProcessRemoteOrigin" {
					if mode == "skipped" {
						action = "skip"
					}
					if mode == "failed" {
						action = "fail"
					}
				}
				fmt.Fprintf(&evidence, "{\"Action\":%q,\"Package\":\"github.com/colony-2/c2j/%s\",\"Test\":%q}\n", action, pkg, name)
			}
		}
	}
	if mode == "skipped-child" || mode == "failed-child" {
		action := "skip"
		if mode == "failed-child" {
			action = "fail"
		}
		fmt.Fprintf(&evidence, "{\"Action\":%q,\"Package\":\"github.com/colony-2/c2j/cmd/c2j\",\"Test\":\"TestInputHistoryCLIProcessRemoteOrigin/read\"}\n", action)
	}
	if mode == "malformed" {
		evidence.WriteString("not JSON\n")
	}
	return []byte(evidence.String())
}

func TestVerifyHookInputHistoryEvidenceGuard(t *testing.T) {
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	node := r.RecipeImpl.(*recipe.RecipeState).States.States["test_integration"].Node.NodeImpl.(*recipe.NodeOp)
	shell := node.Inputs["run"].(string)
	marker := "node - \"$scratch/input-history.json\" <<'JS'\n"
	start := strings.Index(shell, marker)
	require.NotEqual(t, -1, start)
	code := shell[start+len(marker):]
	end := strings.Index(code, "\nJS\n")
	require.NotEqual(t, -1, end)
	code = code[:end]
	for _, mode := range []string{"passed", "missing", "missing-published-server", "missing-history-lifetime", "skipped", "failed", "unfinished", "skipped-child", "failed-child", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.json")
			require.NoError(t, os.WriteFile(path, inputHistoryGateEvidence(mode), 0600))
			cmd := exec.Command("node", "-", path)
			cmd.Stdin = strings.NewReader(code)
			out, err := cmd.CombinedOutput()
			if mode == "passed" {
				require.NoError(t, err, string(out))
			} else {
				require.Error(t, err, string(out))
			}
		})
	}
}

func TestVerifyHookWorkspaceDiffEvidenceGuard(t *testing.T) {
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	state := r.RecipeImpl.(*recipe.RecipeState)
	node := state.States.States["test_integration"].Node.NodeImpl.(*recipe.NodeOp)
	shell := node.Inputs["run"].(string)
	start := strings.Index(shell, "node - \"$scratch/saved-diff.json\" <<'JS'\n")
	require.NotEqual(t, -1, start)
	code := shell[start+len("node - \"$scratch/saved-diff.json\" <<'JS'\n"):]
	end := strings.Index(code, "\nJS\n")
	require.NotEqual(t, -1, end)
	code = code[:end]
	modes := []string{"passed", "missing", "skipped", "failed", "malformed", "missing-native", "unfinished", "missing-regression", "skipped-regression", "missing-observer", "skipped-observer-selection", "failed-observer-selection", "unfinished-observer-selection"}
	var observerSelections []string
	for _, backend := range []string{"SQLite", "HTTP"} {
		for _, variant := range []string{"omitted-parent", "parent-present"} {
			for _, selection := range []string{"producer", "observer-one", "observer-two", "latest"} {
				name := "TestWorkspaceDiffNativePatchlessObservers/" + backend + "/" + variant + "/" + selection
				observerSelections = append(observerSelections, name)
				modes = append(modes, "missing-selection/"+name)
			}
		}
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			var evidence strings.Builder
			for _, pkg := range []string{"pkg/jobinspect", "pkg/childbroker", "cmd/c2j/internal/cmd", "cmd/c2j", "pkg/worker/compiler"} {
				if mode == "missing" && pkg == "cmd/c2j" {
					continue
				}
				action := "pass"
				if mode == "skipped" && pkg == "pkg/childbroker" {
					action = "skip"
				}
				if mode == "failed" && pkg == "pkg/jobinspect" {
					action = "fail"
				}
				testName := map[string]string{"pkg/jobinspect": "TestWorkspaceDiffNativeProbe", "pkg/childbroker": "TestWorkspaceDiffHelperProbe", "cmd/c2j/internal/cmd": "TestWorkspaceDiffCLI_Probe", "cmd/c2j": "TestWorkspaceDiffCLIProcessProbe", "pkg/worker/compiler": "TestWorkspaceDiffCompilerProbe"}[pkg]
				if mode == "missing-native" {
					testName = "TestWorkspaceDiffRequest"
				}
				fmt.Fprintf(&evidence, "{\"Action\":\"run\",\"Package\":\"github.com/colony-2/c2j/%s\",\"Test\":%q}\n", pkg, testName)
				if mode != "unfinished" {
					fmt.Fprintf(&evidence, "{\"Action\":%q,\"Package\":\"github.com/colony-2/c2j/%s\",\"Test\":%q}\n", action, pkg, testName)
				}
			}
			if mode != "missing-regression" {
				for pkg, names := range map[string][]string{
					"pkg/jobinspect":      {"TestWorkspaceDiffPatchlessObserverEvidence", "TestWorkspaceDiffNativePatchlessObservers", "TestWorkspaceDiffNativePatchlessObserverRejections", "TestWorkspaceDiffNativePatchlessObserverSources", "TestWorkspaceDiffNativePatchlessObserverAttemptsAndCutoffs"},
					"pkg/worker/compiler": {"TestWorkspaceDiffCompilerReviewScopeWorkspace", "TestWorkspaceDiffCompilerReviewFailedScope"},
					"pkg/childbroker":     {"TestWorkspaceDiffHelperReviewResponseContract"},
				} {
					for _, name := range names {
						if mode == "missing-observer" && name == "TestWorkspaceDiffPatchlessObserverEvidence" {
							continue
						}
						action := "pass"
						if mode == "skipped-regression" {
							action = "skip"
						}
						fmt.Fprintf(&evidence, "{\"Action\":\"run\",\"Package\":\"github.com/colony-2/c2j/%s\",\"Test\":%q}\n", pkg, name)
						fmt.Fprintf(&evidence, "{\"Action\":%q,\"Package\":\"github.com/colony-2/c2j/%s\",\"Test\":%q}\n", action, pkg, name)
					}
				}
			}
			for _, name := range observerSelections {
				if mode == "missing-selection/"+name {
					continue
				}
				action := "pass"
				if name == "TestWorkspaceDiffNativePatchlessObservers/HTTP/omitted-parent/latest" {
					switch mode {
					case "skipped-observer-selection":
						action = "skip"
					case "failed-observer-selection":
						action = "fail"
					case "unfinished-observer-selection":
						action = "run"
					}
				}
				fmt.Fprintf(&evidence, "{\"Action\":\"run\",\"Package\":\"github.com/colony-2/c2j/pkg/jobinspect\",\"Test\":%q}\n", name)
				if action != "run" {
					fmt.Fprintf(&evidence, "{\"Action\":%q,\"Package\":\"github.com/colony-2/c2j/pkg/jobinspect\",\"Test\":%q}\n", action, name)
				}
			}
			if mode == "malformed" {
				evidence.WriteString("not JSON\n")
			}
			path := filepath.Join(t.TempDir(), "events.json")
			require.NoError(t, os.WriteFile(path, []byte(evidence.String()), 0600))
			cmd := exec.Command("node", "-", path)
			cmd.Stdin = strings.NewReader(code)
			out, err := cmd.CombinedOutput()
			if mode == "passed" {
				require.NoError(t, err, string(out))
			} else {
				require.Error(t, err, string(out))
				if mode == "missing-observer" {
					require.Contains(t, string(out), "Saved workspace-diff review regression missing: TestWorkspaceDiffPatchlessObserverEvidence")
				} else if name, missing := strings.CutPrefix(mode, "missing-selection/"); missing {
					require.Contains(t, string(out), "Patchless observer recovery missing: "+name)
				} else if strings.HasSuffix(mode, "-observer-selection") {
					require.Contains(t, string(out), "Saved workspace-diff acceptance missing, skipped or failed: github.com/colony-2/c2j/pkg/jobinspect")
				}
			}
		})
	}
}

func TestVerifyHookLeaseParentageEvidenceGuard(t *testing.T) {
	data, err := os.ReadFile("../../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	r, err := recipe.LoadRecipeFromString(data)
	require.NoError(t, err)
	node := r.RecipeImpl.(*recipe.RecipeState).States.States["test_integration"].Node.NodeImpl.(*recipe.NodeOp)
	shell := node.Inputs["run"].(string)
	marker := "node - \"$scratch/parentage.json\" <<'JS'\n"
	start := strings.Index(shell, marker)
	require.NotEqual(t, -1, start)
	code := shell[start+len(marker):]
	end := strings.Index(code, "\nJS\n")
	require.NotEqual(t, -1, end)
	code = code[:end]
	required := map[string][]string{
		"cmd/c2j/internal/submitjob": {"TestLeaseParentageMissingBroker", "TestLeaseParentageStandalone", "TestLeaseParentageBrokerBoundaries", "TestSubmissionWaitBrokerLeaseRoutes", "TestRun_BrokerSubmissionAndConfiguredImmediateExecution"},
		"pkg/childbroker":            {"TestBrokerOnlyCLI", "TestLeaseParentageLostOrdinaryResponse"},
		"pkg/executionruntime":       {"TestLeaseParentageSuppliedLease"},
		"pkg/worker/compiler":        {"TestLeaseParentageWorkerCLI", "TestWorkerBrokerSubmitsIncomingAndLiveWorkspace"},
		"cmd/c2j/internal/cmd":       {"TestSubmitHelpPromotesConventions"},
	}
	for _, mode := range []string{"passed", "missing", "skipped", "failed", "unfinished", "subcase-skipped", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var evidence strings.Builder
			for pkg, names := range required {
				for _, name := range names {
					action := "pass"
					if name == "TestLeaseParentageWorkerCLI" {
						switch mode {
						case "missing":
							continue
						case "skipped":
							action = "skip"
						case "failed":
							action = "fail"
						case "unfinished":
							action = "run"
						}
					}
					fmt.Fprintf(&evidence, "{\"Action\":%q,\"Package\":%q,\"Test\":%q}\n", action, "github.com/colony-2/c2j/"+pkg, name)
				}
			}
			if mode == "subcase-skipped" {
				evidence.WriteString("{\"Action\":\"skip\",\"Package\":\"github.com/colony-2/c2j/pkg/executionruntime\",\"Test\":\"TestLeaseParentageSuppliedLease/remote\"}\n")
			}
			if mode == "malformed" {
				evidence.WriteString("{bad json\n")
			}
			path := filepath.Join(t.TempDir(), "events.json")
			require.NoError(t, os.WriteFile(path, []byte(evidence.String()), 0600))
			cmd := exec.Command("node", "-", path)
			cmd.Stdin = strings.NewReader(code)
			out, err := cmd.CombinedOutput()
			if mode == "passed" {
				require.NoError(t, err, string(out))
			} else {
				require.Error(t, err, string(out))
			}
		})
	}
}
