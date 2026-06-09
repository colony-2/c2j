package cmd

import (
	"fmt"

	"github.com/colony-2/c2j/cmd/c2j/internal/buildinfo"
	"github.com/spf13/cobra"
)

func Execute(info buildinfo.Info) (int, error) {
	root := newRootCmd(info)
	if err := root.Execute(); err != nil {
		if coded, ok := err.(exitCoder); ok {
			return coded.ExitCode(), err
		}
		return 1, err
	}
	return 0, nil
}

func newRootCmd(info buildinfo.Info) *cobra.Command {
	root := &cobra.Command{
		Use:           "c2j",
		Short:         "C2J job tooling",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newVersionCmd(info))
	root.AddCommand(newSubmitCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newReadyCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newTestCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newCellsCmd())
	root.AddCommand(newSelfCmd())

	return root
}

func newVersionCmd(info buildinfo.Info) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print c2j build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "c2j version %s\n", info.Version)
			return nil
		},
	}
}

type exitCoder interface {
	error
	ExitCode() int
}
