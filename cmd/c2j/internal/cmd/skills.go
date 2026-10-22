package cmd

import (
	"github.com/colony-2/c2j/cmd/c2j/internal/skillinstall"
	skillsbundle "github.com/colony-2/c2j/skills"
	"github.com/spf13/cobra"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "skills", Short: "Manage bundled agent skills"}
	opts := skillinstall.Options{Bundle: skillsbundle.FS}
	update := &cobra.Command{
		Use:   "update",
		Short: "Install or refresh skills from this c2j binary",
		Long:  "Install or refresh the bundled c2j and c2ops skills without changing project configuration.\nUpgrade c2j first to obtain a newer bundle; this command does not fetch skills from GitHub.\nLocally modified skills are preserved unless --force is supplied.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			summary, err := skillinstall.InstallBundled(cmd.Context(), opts)
			skillinstall.PrintSummary(cmd.OutOrStdout(), summary)
			return err
		},
	}
	update.Flags().StringVar(&opts.Scope, "scope", skillinstall.ScopeAuto, "Install scope: auto (project), project, or user")
	update.Flags().BoolVar(&opts.Force, "force", false, "Replace locally modified c2j skill folders")
	cmd.AddCommand(update)
	return cmd
}
