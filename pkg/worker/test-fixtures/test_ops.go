package testfixtures

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	recipeops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/swf-go/pkg/swf"
)

type testWriteFileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type testWriteFileOutput struct {
	Path string `json:"path"`
}

type testReadFileInput struct {
	Path string `json:"path"`
}

type testReadFileOutput struct {
	Content string `json:"content"`
}

type testGitCommitInput struct {
	RepoPath string `json:"repo_path"`
	Message  string `json:"message"`
}

type testGitCommitOutput struct {
	CommitHash string `json:"commit_hash"`
}

type testEmitArtifactInput struct{}

type testEmitArtifactOutput struct {
	Name string `json:"name"`
}

type testConsumeArtifactInput struct {
	InputArtifact swf.ArtifactKey `json:"artifact" validate:"required"`
}

type testConsumeArtifactOutput struct {
	Name string `json:"name"`
}

type testComplexMeta struct {
	Label string   `json:"label"`
	Flags []string `json:"flags"`
}

type testComplexConfigItem struct {
	ID float64 `json:"id" yaml:"id"`
}

type testComplexConfigNested struct {
	Name string `json:"name" yaml:"name"`
}

type testComplexConfig struct {
	Enabled   bool                    `json:"enabled" yaml:"enabled"`
	Threshold float64                 `json:"threshold" yaml:"threshold"`
	Nested    testComplexConfigNested `json:"nested" yaml:"nested"`
	Items     []testComplexConfigItem `json:"items" yaml:"items"`
}

type testComplexInput struct {
	Config testComplexConfig `json:"config"`
	Meta   testComplexMeta   `json:"meta"`
}

type testComplexOutput struct {
	Config testComplexConfig `json:"config"`
	Meta   testComplexMeta   `json:"meta"`
}

type testNextTaskInput struct {
	Message string `json:"message"`
}

type testNextTaskOutput struct {
	Message string `json:"message"`
	Step    string `json:"step"`
}

func init() {
	recipeops.Register(
		recipeops.NewActivityMappedOpV2[testWriteFileInput, testWriteFileOutput](
			recipeops.OpMetadata{
				Type:        "test_write_file",
				Description: "writes file contents for git workspace tests",
				Version:     "1.0.0",
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input testWriteFileInput) (testWriteFileOutput, error) {
				if input.Path == "" {
					return testWriteFileOutput{}, fmt.Errorf("path is required")
				}
				if err := os.MkdirAll(filepath.Dir(input.Path), 0o755); err != nil {
					return testWriteFileOutput{}, err
				}
				if err := os.WriteFile(input.Path, []byte(input.Content), 0o644); err != nil {
					return testWriteFileOutput{}, err
				}
				return testWriteFileOutput{Path: input.Path}, nil
			},
		),
		recipeops.NewActivityMappedOpV2[testReadFileInput, testReadFileOutput](
			recipeops.OpMetadata{
				Type:        "test_read_file",
				Description: "reads file contents from git workspace for tests",
				Version:     "1.0.0",
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input testReadFileInput) (testReadFileOutput, error) {
				if input.Path == "" {
					return testReadFileOutput{}, fmt.Errorf("path is required")
				}
				data, err := os.ReadFile(input.Path)
				if err != nil {
					return testReadFileOutput{}, err
				}
				return testReadFileOutput{Content: string(data)}, nil
			},
		),
		recipeops.NewActivityMappedOpV2[testGitCommitInput, testGitCommitOutput](
			recipeops.OpMetadata{
				Type:        "test_git_commit_all",
				Description: "stages all changes and creates a git commit for tests",
				Version:     "1.0.0",
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input testGitCommitInput) (testGitCommitOutput, error) {
				if input.RepoPath == "" {
					return testGitCommitOutput{}, fmt.Errorf("repo_path is required")
				}
				if input.Message == "" {
					return testGitCommitOutput{}, fmt.Errorf("message is required")
				}
				if err := runGitCmd(ctx, input.RepoPath, "add", "-A"); err != nil {
					return testGitCommitOutput{}, err
				}
				if err := runGitCmd(ctx, input.RepoPath, "commit", "-m", input.Message); err != nil {
					return testGitCommitOutput{}, err
				}
				hash, err := gitOutput(ctx, input.RepoPath, "rev-parse", "HEAD")
				if err != nil {
					return testGitCommitOutput{}, err
				}
				return testGitCommitOutput{CommitHash: hash}, nil
			},
		),
		recipeops.NewActivityMappedOpV2[testEmitArtifactInput, testEmitArtifactOutput](
			recipeops.OpMetadata{
				Type:        "test_emit_artifact",
				Description: "emits a named artifact for artifact flow tests",
				Version:     "1.0.0",
			},
			func(deps recipeops.OpDependencies, ctx context.Context, input testEmitArtifactInput) (testEmitArtifactOutput, error) {
				artifact := swf.NewArtifactFromBytes("foo", []byte("hello world"))
				if err := deps.AddOutputArtifact(artifact); err != nil {
					return testEmitArtifactOutput{}, err
				}
				return testEmitArtifactOutput{Name: artifact.Name()}, nil
			},
		),
		recipeops.NewActivityMappedOpV2[testComplexInput, testComplexOutput](
			recipeops.OpMetadata{
				Type:        "test_complex_input",
				Description: "echoes complex structured input for template tests",
				Version:     "1.0.0",
			},
			func(_ recipeops.OpDependencies, ctx context.Context, input testComplexInput) (testComplexOutput, error) {
				if input.Meta.Label == "" {
					return testComplexOutput{}, fmt.Errorf("meta.label is required")
				}
				return testComplexOutput{
					Config: input.Config,
					Meta:   input.Meta,
				}, nil
			},
		),
		recipeops.NewActivityMappedOpV2[testConsumeArtifactInput, testConsumeArtifactOutput](
			recipeops.OpMetadata{
				Type:        "test_consume_artifact",
				Description: "consumes a named artifact reference and input artifacts",
				Version:     "1.0.0",
			},
			func(deps recipeops.OpDependencies, ctx context.Context, input testConsumeArtifactInput) (testConsumeArtifactOutput, error) {
				matched, err := deps.FindArtifact(input.InputArtifact)
				if err != nil {
					return testConsumeArtifactOutput{}, err
				}

				data, err := matched.Bytes(ctx)
				if err != nil {
					return testConsumeArtifactOutput{}, err
				}
				if string(data) != "hello world" {
					return testConsumeArtifactOutput{}, fmt.Errorf("unexpected artifact content: %s", string(data))
				}
				return testConsumeArtifactOutput{Name: matched.Name()}, nil
			},
		),
		recipeops.NewOp().
			WithType("test_next_task_override").
			WithDescription("overrides the next task type from within an op").
			WithVersion("1.0.0").
			AddStep("first", recipeops.NewStepWithDeps(func(deps recipeops.OpDependencies, ctx context.Context, input testNextTaskInput) (testNextTaskOutput, error) {
				_ = ctx
				deps.SetNextTaskType("")
				return testNextTaskOutput{
					Message: input.Message,
					Step:    "first",
				}, nil
			})).
			AddStep("second", recipeops.NewStepWithDeps(func(_ recipeops.OpDependencies, ctx context.Context, input testNextTaskOutput) (testNextTaskOutput, error) {
				_ = ctx
				_ = input
				return testNextTaskOutput{}, fmt.Errorf("unexpected second step execution")
			})).
			BuildOrPanic(),
	)
}

func runGitCmd(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v failed: %w (%s)", args, err, out)
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v failed: %w (%s)", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
