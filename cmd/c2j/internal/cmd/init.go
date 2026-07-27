package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/initconfig"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	opts := initconfig.Options{
		InstallSkills: true,
		SkillsScope:   "auto",
		Stdout:        os.Stdout,
	}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented .c2j/config.yaml template",
		RunE: func(cmd *cobra.Command, args []string) error {
			return initconfig.Run(context.Background(), opts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.Force, "force", false, "Overwrite an existing .c2j/config.yaml")
	flags.BoolVar(&opts.StdoutOnly, "stdout", false, "Print the generated config to stdout instead of writing it")
	flags.BoolVar(&opts.NoSkills, "no-skills", false, "Do not install bundled c2j skills")
	flags.BoolVar(&opts.SkillsForce, "skills-force", false, "Overwrite existing installed c2j skill folders")
	flags.StringVar(&opts.SkillsScope, "skills-scope", "auto", "Skill install scope: auto, project, or user")
	flags.BoolVar(&opts.InstallOpSkills, "with-op-skills", false, "Also try to install trusted external op-specific skills when available")

	return cmd
}
