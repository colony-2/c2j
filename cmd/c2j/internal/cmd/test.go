package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/testjob"
	"github.com/spf13/cobra"
)

func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Compile, validate, and run recipe test suites locally",
	}
	cmd.AddCommand(newTestCompileCmd(), newTestValidateCmd(), newTestRunCmd(), newTestCaseCmd())
	return cmd
}

func newTestCompileCmd() *cobra.Command {
	opts := newBaseTestOptions()
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Compile a recipe test suite into canonical IR",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return testjob.CompileAndWrite(context.Background(), opts)
		},
	}
	bindTestCommonFlags(cmd, &opts)
	cmd.Flags().StringVar(&opts.OutPath, "out", "", "Output path for compiled canonical IR")
	cmd.Flags().BoolVar(&opts.JSONOutput, "json", false, "Emit compiled IR as JSON")
	return cmd
}

func newTestValidateCmd() *cobra.Command {
	opts := newBaseTestOptions()
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate recipe test cases locally",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return testjob.Validate(context.Background(), opts)
		},
	}
	bindTestCommonFlags(cmd, &opts)
	cmd.Flags().IntVar(&opts.Parallelism, "parallelism", 4, "Number of cases to process in parallel")
	cmd.Flags().BoolVar(&opts.FailFast, "fail-fast", false, "Stop scheduling new cases after first invalid case")
	cmd.Flags().BoolVar(&opts.JSONOutput, "json", false, "Emit validation summary as JSON")
	return cmd
}

func newTestRunCmd() *cobra.Command {
	opts := newBaseTestOptions()
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run recipe test cases locally",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return testjob.Run(context.Background(), opts)
		},
	}
	bindTestCommonFlags(cmd, &opts)
	bindTestRunFlags(cmd, &opts)
	return cmd
}

func newTestCaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "case",
		Short: "Validate or run a single recipe test case",
	}
	cmd.AddCommand(newTestCaseValidateCmd(), newTestCaseRunCmd())
	return cmd
}

func newTestCaseValidateCmd() *cobra.Command {
	opts := newBaseTestOptions()
	var caseID string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate one recipe test case locally",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.CaseIDs = []string{caseID}
			return testjob.Validate(context.Background(), opts)
		},
	}
	bindTestCommonFlags(cmd, &opts)
	cmd.Flags().StringVar(&caseID, "case-id", "", "Case ID to validate")
	cmd.Flags().IntVar(&opts.Parallelism, "parallelism", 1, "Number of cases to process in parallel")
	_ = cmd.MarkFlagRequired("case-id")
	return cmd
}

func newTestCaseRunCmd() *cobra.Command {
	opts := newBaseTestOptions()
	var caseID string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run one recipe test case locally",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.CaseIDs = []string{caseID}
			return testjob.Run(context.Background(), opts)
		},
	}
	bindTestCommonFlags(cmd, &opts)
	bindTestRunFlags(cmd, &opts)
	cmd.Flags().StringVar(&caseID, "case-id", "", "Case ID to run")
	_ = cmd.MarkFlagRequired("case-id")
	return cmd
}

func newBaseTestOptions() testjob.Options {
	return testjob.Options{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

func bindTestCommonFlags(cmd *cobra.Command, opts *testjob.Options) {
	flags := cmd.Flags()
	flags.StringVar(&opts.Recipe, "recipe", "", "Recipe name or git selector to test (defaults to default)")
	flags.StringVar(&opts.RecipeFile, "recipe-file", "", "Path to a local recipe YAML file")
	flags.StringVar(&opts.FilePath, "file", "", "Suite file path")
	flags.BoolVar(&opts.UseStdin, "stdin", false, "Read suite from stdin")
	flags.StringVar(&opts.Format, "format", "", "Suite format: canonical_yaml|canonical_json|compact_yaml|scenario_md")
	flags.StringSliceVar(&opts.CaseIDs, "case", nil, "Only include selected case IDs (repeatable)")
	flags.BoolVar(&opts.Strict, "strict", false, "Strict local compile")
	flags.BoolVar(&opts.Self, "self", false, "Target the current cell explicitly")
	flags.StringVar(&opts.Cell, "cell", "", "Target cell git repository, clone URL, local path, or configured short name")
}

func bindTestRunFlags(cmd *cobra.Command, opts *testjob.Options) {
	flags := cmd.Flags()
	flags.IntVar(&opts.Parallelism, "parallelism", 4, "Number of cases to process in parallel")
	flags.BoolVar(&opts.StopOnFailure, "stop-on-failure", false, "Stop scheduling new cases after first failure")
	flags.StringVar(&opts.Execution.Timeout, "case-timeout", "", "Per-case timeout duration")
	flags.StringVar(&opts.Execution.ArtifactMode, "artifact-mode", "none", "Artifact mode: none|inline")
	flags.Int64Var(&opts.Execution.ArtifactMaxBytes, "artifact-max-bytes", 65536, "Max inline artifact bytes per artifact")
	flags.StringVar(&opts.Execution.EvaluationMode, "evaluation-mode", "enforce", "Evaluation mode: enforce|report_only")
	flags.StringVar(&opts.OutDir, "out-dir", "", "Output directory for local test artifacts")
	flags.StringVar(&opts.JSONLEvents, "jsonl-events", "", "Optional JSONL event output path")
}
