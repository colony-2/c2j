package cmd

import (
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/executionflags"
	"github.com/colony-2/c2j/cmd/c2j/internal/submitjob"
	"github.com/spf13/cobra"
)

func newSubmitCmd() *cobra.Command {
	var useEmbed bool
	opts := submitjob.Options{}

	cmd := &cobra.Command{
		Use:   "submit [prompt]",
		Short: "Submit a build job",
		Long: `Submit a build job for the target cell.

Uses build.yaml on main in github.com/colony-2/recipes directly.
Explicit bare recipe names look in the target cell's .c2/recipes directory.
The prompt becomes inputs.prompt. Use --prompt-file to read it from a text file;
if no prompt is supplied, it is requested interactively.
The optional --title becomes inputs.title and is included in job metadata.
Title and prompt are recorded even if the recipe does not declare them. The
recipe receives each only when declared in input_schema; other unknown inputs
still fail validation. The CLI requires a nonblank prompt.
Build submissions also set inputs.type to "build".
Custom recipes are an advanced use case: use --advanced-recipe[-file].

Inside a running task, current job context requires a child job broker.
Preserve the worker-provided environment or upgrade CLI and worker together if
it is missing; direct JobDB configuration and --embed cannot replace the broker.
Broker-only submission needs no direct JobDB configuration and records the
running job as the formal parent. Optional direct configuration must use the
current tenant; --run also needs a direct JobDB target.

Submission returns immediately. Every --wait-job, --wait-job-success
and --wait-until requirement must clear before execution. Completion includes
failure and cancellation; success waits do not. Each wait targets its exact job; replacements have independent outcomes. Local date/clock times
use this process's timezone; durations start at submission, before preparation.
--run uses the existing runner to wait (15 minutes by default); prerequisites
need another worker. A runner timeout leaves the durable submission pending.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			runOpts.Stdin = cmd.InOrStdin()
			runOpts.Stdout = cmd.OutOrStdout()
			runOpts.Stderr = cmd.ErrOrStderr()
			runOpts.Prompt = ""
			runOpts.PromptSet = false
			runOpts.PromptFileSet = cmd.Flags().Changed("prompt-file")
			runOpts.TitleSet = cmd.Flags().Changed("title")
			if useEmbed {
				runOpts.JobDBURI = defaults.EmbedURL
			}
			if len(args) == 1 {
				runOpts.Prompt = args[0]
				runOpts.PromptSet = true
			}
			return submitjob.Run(cmd.Context(), runOpts)
		},
	}

	flags := cmd.Flags()
	opts.AddWaitFlags(flags)
	flags.StringVar(&opts.GoalID, "goal", "", "Unsupported legacy goal selection; submit an independent job")
	flags.StringVar(&opts.JobID, "job-id", "", "Explicit destination job ID")
	flags.StringVar(&opts.RequestFile, "request-file", "", "Retain a resolved ordinary request before publication")
	flags.StringVar(&opts.ReplayFile, "replay", "", "Replay a retained request unchanged after an ambiguous result")
	executionflags.AddRequirementFlags(flags, &opts.ExecutionRequirements)
	opts.ExecutionFlags.AddFlags(flags)
	flags.StringVar(&opts.JobDBURI, "jobdb", "", "JobDB URI (http(s)://host/tenant or embed:///)")
	flags.StringVar(&opts.Recipe, "advanced-recipe", "", "Advanced: custom recipe name or git selector")
	flags.StringVar(&opts.RecipeFile, "advanced-recipe-file", "", "Advanced: custom local recipe YAML file")
	flags.StringVar(&opts.Recipe, "recipe", "", "Deprecated alias for --advanced-recipe")
	flags.StringVar(&opts.RecipeFile, "recipe-file", "", "Deprecated alias for --advanced-recipe-file")
	_ = flags.MarkDeprecated("recipe", "use --advanced-recipe")
	_ = flags.MarkDeprecated("recipe-file", "use --advanced-recipe-file")
	cmd.MarkFlagsMutuallyExclusive("advanced-recipe", "advanced-recipe-file", "recipe", "recipe-file")
	flags.StringVar(&opts.InputsJSON, "inputs-json", "", "Inline JSON object for recipe inputs")
	flags.StringVar(&opts.InputsFile, "inputs-file", "", "Path to a JSON or YAML file containing recipe inputs")
	flags.StringVar(&opts.PromptFile, "prompt-file", "", "Read the prompt from a UTF-8 text file (exclusive with positional prompt or inputs.prompt)")
	flags.StringVar(&opts.Title, "title", "", "Optional job title, inserted as inputs.title")
	flags.StringArrayVar(&opts.ArtifactSpecs, "artifact", nil, "Attach a local file as a job artifact; repeatable, accepts PATH or NAME=PATH")
	flags.BoolVar(&opts.FromWorkspace, "from-workspace", false, "Start from the current task's incoming workspace snapshot")
	flags.BoolVar(&opts.FromWorkspaceNow, "from-workspace-now", false, "Start from the workspace's current commits and file contents")
	cmd.MarkFlagsMutuallyExclusive("from-workspace", "from-workspace-now")
	flags.BoolVar(&opts.Self, "self", false, "Target the current cell explicitly (also the default when --cell is omitted)")
	flags.StringVar(&opts.CellRepo, "cell-repo", "", "Optional repository assertion for an already registered cell")
	flags.StringVar(&opts.Cell, "cell", "", "Target cell name")
	flags.BoolVarP(&opts.RunAfterSubmit, "run", "r", false, "Start the runner after submission, waiting for readiness")
	flags.BoolVar(&useEmbed, "embed", false, "Use embedded JobDB (equivalent to --jobdb "+defaults.EmbedURL+")")
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit the submitted job identity as JSON")

	return cmd
}
