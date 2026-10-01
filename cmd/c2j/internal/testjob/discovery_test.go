package testjob

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeDiscoveryFile(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0644))
}

const discoveryRecipe = "id: discovery\nversion: '1'\nsequence: []\noutputs: {answer: 42}\n"
const discoveryCase = "cases:\n- id: answer\n  type: recipe_case\n  assertions:\n  - {type: output_equals, path: answer, value: 42}\n"

func TestDirectoryRunsSelfContainedSuitesAndKeepsFailures(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFile(t, root, "recipe.yaml", discoveryRecipe)
	writeDiscoveryFile(t, root, "tests/a.test.yaml", "recipe: ../recipe.yaml\n"+discoveryCase)
	writeDiscoveryFile(t, root, "tests/nested/b.scenario.md", "# Suite\n```yaml\nrecipe: ../../recipe.yaml\n"+discoveryCase+"```\n")
	writeDiscoveryFile(t, root, "tests/broken.test.yaml", "recipe: [bad\n")
	writeDiscoveryFile(t, root, "tests/live.test.yaml", "recipe: ../recipe.yaml\nlive: true\n"+discoveryCase)
	writeDiscoveryFile(t, root, "tests/.cache/ignore.test.yaml", "invalid")
	writeDiscoveryFile(t, root, "tests/readme.md", "not a suite")
	var out bytes.Buffer
	opts := Options{Directory: filepath.Join(root, "tests"), WorkingDir: t.TempDir(), OutDir: filepath.Join(root, "results"), Stdout: &out, JSONOutput: true}
	require.ErrorContains(t, Run(context.Background(), opts), "1 suite(s) failed")
	var report struct {
		Selected, Failed int
		Suites           []suiteResult
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.Equal(t, 2, report.Selected)
	require.Equal(t, 1, report.Failed)
	require.Len(t, report.Suites, 4)
	require.Equal(t, "passed", report.Suites[0].Status)
	require.Equal(t, "excluded_live", report.Suites[2].Status)
	require.Equal(t, "passed", report.Suites[3].Status)
	require.FileExists(t, filepath.Join(root, "results/suites/nested/b.scenario.md/summary.json"))
	require.FileExists(t, filepath.Join(root, "results/summary.json"))
}

func TestDiscoveryRejectsEmptyMissingTargetsAndRequiresLiveOptIn(t *testing.T) {
	root := t.TempDir()
	_, err := discoverSuites(root)
	require.ErrorContains(t, err, "no test suites")
	writeDiscoveryFile(t, root, "recipe.yaml", discoveryRecipe)
	writeDiscoveryFile(t, root, "one.test.yaml", discoveryCase)
	opts := Options{Directory: root, OutDir: filepath.Join(t.TempDir(), "results"), Stdout: &bytes.Buffer{}}
	require.ErrorContains(t, Validate(context.Background(), opts), "1 suite(s) failed")
	writeDiscoveryFile(t, root, "one.test.yaml", "recipe: recipe.yaml\nlive: true\n"+discoveryCase)
	require.ErrorContains(t, Run(context.Background(), opts), "no test cases selected")
	opts.IncludeLive = true
	require.NoError(t, Run(context.Background(), opts))
	opts.CaseIDs = []string{"missing"}
	require.ErrorContains(t, Run(context.Background(), opts), "no test cases selected")
	opts.FilePath = "other.yaml"
	require.ErrorContains(t, Run(context.Background(), opts), "cannot be combined")
}

func TestSingleFileUsesDeclaredRecipeAndRejectsDuplicateCases(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFile(t, root, "recipe.yaml", discoveryRecipe)
	writeDiscoveryFile(t, root, "suite.test.yaml", "recipe: recipe.yaml\n"+discoveryCase)
	opts := Options{FilePath: filepath.Join(root, "suite.test.yaml"), WorkingDir: t.TempDir(), Stdout: &bytes.Buffer{}}
	require.NoError(t, Validate(context.Background(), opts))
	_, err := parseSuiteCases([]byte("cases: [{id: a}, {id: a}]"), "canonical_yaml")
	require.ErrorContains(t, err, "duplicate case ID")
}
