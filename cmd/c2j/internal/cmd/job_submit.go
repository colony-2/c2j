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
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a new recipe job through the SWF remote runtime",
		RunE: func(cmd *cobra.Command, args []string) error {
			return submitjob.Run(context.Background(), opts)
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
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit the submitted job identity as JSON")

	return cmd
}
