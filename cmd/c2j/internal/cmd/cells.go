package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/configinspect"
	"github.com/spf13/cobra"
)

func newCellsCmd() *cobra.Command {
	opts := configinspect.CellsOptions{
		Stdout: os.Stdout,
	}

	cmd := &cobra.Command{
		Use:   "cells",
		Short: "List the current cell's allowed dependent cells",
		RunE: func(cmd *cobra.Command, args []string) error {
			return configinspect.RunCells(context.Background(), opts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit the resolved cells as JSON")

	return cmd
}
