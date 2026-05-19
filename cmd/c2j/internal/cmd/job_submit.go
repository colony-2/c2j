package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/submitjob"
	"github.com/spf13/cobra"
)

func newSubmitCmd() *cobra.Command {
	var useEmbed bool
	opts := submitjob.Options{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "submit [prompt]",
		Short: "Submit a new recipe job through the SWF runtime",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			runOpts.Prompt = ""
			runOpts.PromptSet = false
			if useEmbed {
				runOpts.SWFURL = defaults.EmbedURL
			}
			if len(args) == 1 {
				runOpts.Prompt = args[0]
				runOpts.PromptSet = true
			}
			return submitjob.Run(context.Background(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.TenantID, "tenant-id", "", "Tenant/project ID for the job (defaults to project self.tenant_id or derived self.repo hash when available)")
	flags.StringVar(&opts.SWFURL, "swf-url", "", "SWF runtime URL (http(s)://... or embed:///; defaults to "+defaults.SWFURL+")")
	flags.StringVar(&opts.Recipe, "recipe", "", "Recipe name or git selector to submit (defaults to default)")
	flags.StringVar(&opts.RecipeFile, "recipe-file", "", "Path to a recipe YAML file to submit")
	flags.StringVar(&opts.InputsJSON, "inputs-json", "", "Inline JSON object for recipe inputs")
	flags.StringVar(&opts.InputsFile, "inputs-file", "", "Path to a JSON or YAML file containing recipe inputs")
	flags.StringArrayVar(&opts.ArtifactSpecs, "artifact", nil, "Attach a local file as a job artifact; repeatable, accepts PATH or NAME=PATH")
	flags.BoolVar(&opts.Self, "self", false, "Target the current cell explicitly (also the default when --cell is omitted)")
	flags.StringVar(&opts.Cell, "cell", "", "Target cell git repository (canonical repo, clone URL, or local path)")
	flags.BoolVarP(&opts.RunAfterSubmit, "run", "r", false, "Run the submitted job immediately after submission")
	flags.BoolVar(&useEmbed, "embed", false, "Use the embedded SWF runtime (equivalent to --swf-url "+defaults.EmbedURL+")")
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit the submitted job identity as JSON")

	return cmd
}
