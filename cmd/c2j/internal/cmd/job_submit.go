package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/submitjob"
	"github.com/spf13/cobra"
)

func newSubmitCmd() *cobra.Command {
	opts := submitjob.Options{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "submit [prompt]",
		Short: "Submit a new recipe job through the SWF remote runtime",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			runOpts.Prompt = ""
			runOpts.PromptSet = false
			if len(args) == 1 {
				runOpts.Prompt = args[0]
				runOpts.PromptSet = true
			}
			return submitjob.Run(context.Background(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.TenantID, "tenant-id", "", "Tenant/project ID for the job (defaults to "+defaults.TenantID+")")
	flags.StringVar(&opts.SWFURL, "swf-url", "", "Base URL for the SWF remote runtime (defaults to "+defaults.SWFURL+")")
	flags.StringVar(&opts.Recipe, "recipe", "", "Recipe name or git selector to submit (defaults to default)")
	flags.StringVar(&opts.RecipeFile, "recipe-file", "", "Path to a recipe YAML file to submit")
	flags.StringVar(&opts.InputsJSON, "inputs-json", "", "Inline JSON object for recipe inputs")
	flags.StringVar(&opts.InputsFile, "inputs-file", "", "Path to a JSON or YAML file containing recipe inputs")
	flags.BoolVar(&opts.Self, "self", false, "Target the current cell from .c2j/config.yaml")
	flags.StringVar(&opts.Cell, "cell", "", "Target cell git repository (canonical repo, clone URL, or local path)")
	flags.StringVar(&opts.ActorEmail, "actor-email", "", "Actor email recorded in job context")
	flags.StringVar(&opts.TicketID, "ticket-id", "", "Ticket ID recorded in job context")
	flags.BoolVarP(&opts.RunAfterSubmit, "run", "r", false, "Run the submitted job immediately after submission")
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit the submitted job identity as JSON")

	return cmd
}
