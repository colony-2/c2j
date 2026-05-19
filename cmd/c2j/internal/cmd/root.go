package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func Execute(version string, buildTime string) (int, error) {
	root := &cobra.Command{
		Use:           "c2j",
		Short:         "C2J job tooling",
		Version:       fmt.Sprintf("%s (built %s)", version, buildTime),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newSubmitCmd())
	root.AddCommand(newExecCmd())
	root.AddCommand(newWorkCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newTestCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newCellsCmd())
	root.AddCommand(newSelfCmd())

	if err := root.Execute(); err != nil {
		if coded, ok := err.(exitCoder); ok {
			return coded.ExitCode(), err
		}
		return 1, err
	}
	return 0, nil
}

type exitCoder interface {
	error
	ExitCode() int
}
