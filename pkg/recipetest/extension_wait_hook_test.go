package recipetest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtensionWaitVerifyHookGate(t *testing.T) {
	body, err := os.ReadFile("../../.c2/recipes/hooks/build/verify.yaml")
	require.NoError(t, err)
	_, block, found := strings.Cut(string(body), "\n          wait_status=0\n")
	require.True(t, found)
	block, _, found = strings.Cut(block, "\n          # End extension-wait acceptance gate.")
	require.True(t, found)
	lines := strings.Split(block, "\n")
	for i := range lines {
		lines[i] = strings.TrimPrefix(lines[i], "          ")
	}
	script := "set -eu\nwait_status=0\n" + strings.Join(lines, "\n") + "\n"
	compilerTests := []string{"TestExtensionWaitCompilerBeyond64", "TestExtensionWaitCompilerReadiness"}
	modes := []string{"pass_with_download_stderr", "missing_package", "missing_recipetest", "skipped_case", "unfinished_case", "missing_binding_case", "skipped_binding_case", "unfinished_binding_case", "failed_command"}
	for _, test := range compilerTests {
		for _, mode := range []string{"missing_compiler_case", "skipped_compiler_case", "unfinished_compiler_case", "failed_compiler_case", "compiler_pass_without_run"} {
			modes = append(modes, mode+"/"+test)
		}
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			var events []string
			variant, compilerTest, _ := strings.Cut(mode, "/")
			for _, name := range []string{"extensionwait", "ops/extensions", "worker/ops", "metricsruntime", "secrets", "executionruntime", "worker/compiler", "childbroker", "recipetest"} {
				if (mode == "missing_package" && name == "extensionwait") || (mode == "missing_recipetest" && name == "recipetest") {
					continue
				}
				for _, action := range []string{"run", "pass"} {
					if action == "pass" && name == "extensionwait" {
						if mode == "unfinished_case" {
							continue
						}
						if mode == "skipped_case" {
							action = "skip"
						}
					}
					e, err := json.Marshal(map[string]string{"Action": action, "Package": "github.com/colony-2/c2j/pkg/" + name, "Test": "TestExtensionWaitFixture"})
					require.NoError(t, err)
					events = append(events, string(e))
				}
			}
			for _, test := range compilerTests {
				if test == compilerTest && variant == "missing_compiler_case" {
					continue
				}
				for _, action := range []string{"run", "pass"} {
					if test == compilerTest {
						if action == "run" && variant == "compiler_pass_without_run" {
							continue
						}
						if action == "pass" {
							switch variant {
							case "unfinished_compiler_case":
								continue
							case "skipped_compiler_case":
								action = "skip"
							case "failed_compiler_case":
								action = "fail"
							}
						}
					}
					e, err := json.Marshal(map[string]string{"Action": action, "Package": "github.com/colony-2/c2j/pkg/worker/compiler", "Test": test})
					require.NoError(t, err)
					events = append(events, string(e))
				}
			}
			for _, backend := range []string{"toy", "sqlite", "remote"} {
				for _, target := range []string{"job_target", "goal_target"} {
					if mode == "missing_binding_case" && backend == "remote" && target == "goal_target" {
						continue
					}
					for _, action := range []string{"run", "pass"} {
						if action == "pass" && backend == "remote" && target == "goal_target" {
							if mode == "unfinished_binding_case" {
								continue
							}
							if mode == "skipped_binding_case" {
								action = "skip"
							}
						}
						e, err := json.Marshal(map[string]string{"Action": action, "Package": "github.com/colony-2/c2j/pkg/executionruntime", "Test": "TestExtensionWaitBindingAdmission/" + backend + "/" + target})
						require.NoError(t, err)
						events = append(events, string(e))
					}
				}
			}
			input := filepath.Join(dir, "events.jsonl")
			require.NoError(t, os.WriteFile(input, []byte(strings.Join(events, "\n")+"\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\ncat \"$WAIT_EVENTS\"\nprintf '%s\\n' 'go: downloading fixture dependency' >&2\nexit \"$WAIT_STATUS\"\n"), 0700))
			status := "0"
			if mode == "failed_command" {
				status = "1"
			}
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "WAIT_EVENTS="+input, "WAIT_STATUS="+status, "scratch="+dir)
			out, err := cmd.CombinedOutput()
			if mode == "pass_with_download_stderr" {
				require.NoError(t, err, "%s", out)
			} else {
				require.Error(t, err, "hook must reject %s", mode)
				switch variant {
				case "missing_compiler_case", "compiler_pass_without_run":
					require.Contains(t, string(out), "Compiler scheduler readiness acceptance missing: "+compilerTest)
				case "skipped_compiler_case", "unfinished_compiler_case", "failed_compiler_case":
					require.Contains(t, string(out), "Extension wait acceptance missing, skipped or failed: github.com/colony-2/c2j/pkg/worker/compiler")
				case "missing_binding_case":
					require.Contains(t, string(out), "Extension wait binding acceptance missing: TestExtensionWaitBindingAdmission/remote/goal_target")
				case "skipped_binding_case", "unfinished_binding_case":
					require.Contains(t, string(out), "Extension wait acceptance missing, skipped or failed: github.com/colony-2/c2j/pkg/executionruntime")
				case "missing_package", "skipped_case", "unfinished_case":
					require.Contains(t, string(out), "Extension wait acceptance missing, skipped or failed: github.com/colony-2/c2j/pkg/extensionwait")
				case "missing_recipetest":
					require.Contains(t, string(out), "Extension wait acceptance missing, skipped or failed: github.com/colony-2/c2j/pkg/recipetest")
				case "failed_command":
					require.NotContains(t, string(out), "acceptance missing", "failed test command must stop before evidence validation")
				}
			}
			require.Contains(t, string(out), "go: downloading fixture dependency", "stderr must remain visible without entering the JSON stream")
		})
	}
}
