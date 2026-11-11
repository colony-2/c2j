package recipetest

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/stretchr/testify/require"
)

func TestShippedNativeControls(t *testing.T) {
	withRegisteredCoreOps(t, extensions.GetExecutionOp(), inputop.GetOp())
	root, err := filepath.Abs("testdata/native-controls")
	require.NoError(t, err)
	t.Setenv("PATH", filepath.Join(root, "fake-bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	raw, err := os.ReadFile(filepath.Join(root, "native-controls.test.json"))
	require.NoError(t, err)
	var suite struct {
		Cases []Case `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &suite))
	recipe, err := os.ReadFile(filepath.Join(root, "recipe.yaml"))
	require.NoError(t, err)
	for _, c := range suite.Cases {
		t.Run(c.ID, func(t *testing.T) {
			result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), FixtureRoot: root, WorkRoot: t.TempDir()}, "shipped-controls", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: string(recipe)}, c, ExecutionOptions{Timeout: "20s"})
			require.Equal(t, "passed", result.Status, result.FailureReason)
			require.True(t, result.Runtime.Controls.Complete)
			var entry *ControlEvent
			for i := range result.Runtime.Controls.Events {
				if result.Runtime.Controls.Events[i].Kind == "entered" {
					entry = &result.Runtime.Controls.Events[i]
					break
				}
			}
			require.NotNil(t, entry)
			require.NotNil(t, entry.Prepared)
			original := entry.Prepared.Objects["session"]["checkpoint"]
			require.Equal(t, contentDigest([]byte("original checkpoint")), original.SHA256)
			data, err := base64.StdEncoding.DecodeString(entry.Prepared.Tools["neutral-tool"].ContentBase64)
			require.NoError(t, err)
			require.Equal(t, "neutral-prepared-tool-marker\n", string(data))
			require.NotEmpty(t, entry.Prepared.ArtifactKeys)
			require.Equal(t, "prepared", entry.Inputs["marker"])
			baseline := result.Runtime.Controls.Baselines["answer"]
			require.True(t, baseline.Complete)
			artifacts := 0
			for _, task := range baseline.SuccessfulTasks {
				artifacts += len(task.Artifacts)
			}
			require.Positive(t, artifacts)
			if c.ID == "cancelled" {
				require.True(t, result.Runtime.Controls.Preserved)
				require.Len(t, result.Runtime.Reviews, 1)
			} else {
				require.Len(t, result.Runtime.Reviews, 2)
				require.NotEqual(t, result.Runtime.Reviews[0].Receipt.SubmissionID, result.Runtime.Reviews[1].Receipt.SubmissionID)
				require.Equal(t, "continued", result.Outputs["sentinel"])
				if c.ID == "healthy" {
					checkpoint := false
					for _, task := range result.Runtime.Controls.FinalSnapshot.SuccessfulTasks {
						if !strings.Contains(task.InvocationID, `op:answer`) {
							continue
						}
						for _, artifact := range task.Artifacts {
							data, err := base64.StdEncoding.DecodeString(artifact.ContentBase64)
							require.NoError(t, err)
							reader := tar.NewReader(bytes.NewReader(data))
							for {
								header, err := reader.Next()
								if err != nil {
									break
								}
								if header.Typeflag == tar.TypeReg {
									content, err := io.ReadAll(reader)
									require.NoError(t, err)
									if string(content) == "answer checkpoint" {
										checkpoint = true
									}
								}
							}
						}
					}
					require.True(t, checkpoint, "healthy answer checkpoint was not published with successful native task")
				}

				pending := 0
				for _, event := range result.Runtime.Controls.Events {
					if event.Kind == "review_pending" {
						pending++
						for _, call := range event.Calls {
							require.NotEqual(t, "controls/continuation", call.NodePath)
						}
					}
				}
				require.Equal(t, 1, pending)
			}
		})
	}
}
