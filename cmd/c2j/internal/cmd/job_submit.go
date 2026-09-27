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
		Short: "Submit a build (default) or evolve job",
		Long: `Submit a build (default) or evolve job for the target cell.

Uses the target cell's .c2j/recipes/build.yaml or evolve.yaml, falling back
to build.yaml or evolve.yaml on main in github.com/colony-2/recipes.
The prompt becomes inputs.prompt; if omitted, it is requested interactively.
Build/evolve submissions also set inputs.type to the selected mode.
Custom recipes are an advanced use case: use --advanced-recipe[-file].`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			runOpts.Stdin = cmd.InOrStdin()
			runOpts.Stdout = cmd.OutOrStdout()
			runOpts.Stderr = cmd.ErrOrStderr()
			runOpts.Prompt = ""
			runOpts.PromptSet = false
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
	executionflags.AddRequirementFlags(flags, &opts.ExecutionRequirements)
	opts.ExecutionFlags.AddFlags(flags)
	flags.StringVar(&opts.JobDBURI, "jobdb", "", "JobDB URI (http(s)://host/tenant or embed:///)")
	flags.BoolVar(&opts.Build, "build", false, "Submit a build job (default)")
	flags.BoolVar(&opts.Evolve, "evolve", false, "Submit an evolve job")
	flags.StringVar(&opts.Recipe, "advanced-recipe", "", "Advanced: custom recipe name or git selector")
	flags.StringVar(&opts.RecipeFile, "advanced-recipe-file", "", "Advanced: custom local recipe YAML file")
	flags.StringVar(&opts.Recipe, "recipe", "", "Deprecated alias for --advanced-recipe")
	flags.StringVar(&opts.RecipeFile, "recipe-file", "", "Deprecated alias for --advanced-recipe-file")
	_ = flags.MarkDeprecated("recipe", "use --advanced-recipe")
	_ = flags.MarkDeprecated("recipe-file", "use --advanced-recipe-file")
	cmd.MarkFlagsMutuallyExclusive("build", "evolve", "advanced-recipe", "advanced-recipe-file", "recipe", "recipe-file")
	flags.StringVar(&opts.InputsJSON, "inputs-json", "", "Inline JSON object for recipe inputs")
	flags.StringVar(&opts.InputsFile, "inputs-file", "", "Path to a JSON or YAML file containing recipe inputs")
	flags.StringArrayVar(&opts.ArtifactSpecs, "artifact", nil, "Attach a local file as a job artifact; repeatable, accepts PATH or NAME=PATH")
	flags.BoolVar(&opts.Self, "self", false, "Target the current cell explicitly (also the default when --cell is omitted)")
	flags.StringVar(&opts.Cell, "cell", "", "Target cell git repository (canonical repo, clone URL, or local path)")
	flags.BoolVarP(&opts.RunAfterSubmit, "run", "r", false, "Run the submitted job immediately after submission")
	flags.BoolVar(&useEmbed, "embed", false, "Use embedded JobDB (equivalent to --jobdb "+defaults.EmbedURL+")")
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit the submitted job identity as JSON")

	return cmd
}
