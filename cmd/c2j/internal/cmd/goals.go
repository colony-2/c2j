package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

func newGoalsCmd() *cobra.Command {
	unsupported := func(*cobra.Command, []string) error {
		return fmt.Errorf("goal commands are unsupported; inspect an exact job")
	}
	root := &cobra.Command{Use: "goals", Short: "Legacy goal commands (unsupported)", PersistentPreRunE: unsupported, RunE: unsupported}
	root.PersistentFlags().String("jobdb", "", "JobDB URI")
	for _, name := range []string{"get", "candidates", "fail", "list"} {
		command := &cobra.Command{Use: name, RunE: unsupported}
		command.Flags().String("message", "", "Legacy failure message")
		command.Flags().StringSlice("cell", nil, "Legacy cell filter")
		command.Flags().StringSlice("state", nil, "Legacy state filter")
		command.Flags().String("page-token", "", "Legacy cursor")
		command.Flags().Int("page-size", 0, "Legacy page size")
		root.AddCommand(command)
	}
	return root
}
