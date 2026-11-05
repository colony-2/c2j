package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/celledit"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newCellCmd() *cobra.Command {
	group := &cobra.Command{Use: "cell", Short: "Create and list JobDB cells or configure dependent routes"}
	var description string
	var stdoutOnly bool
	add := &cobra.Command{
		Use:   "add <repo-or-short-name>",
		Short: "Add or update a manually listed dependent cell",
		Long: "Add or update a dependent cell in the nearest project config, preserving comments.\n" +
			"Detected cells are used directly; this command does not replace automatic discovery.\n" +
			"Descriptions can be added to manually listed cells. Run c2j cell list --dependencies to list routes.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := celledit.Options{Cell: args[0], StdoutOnly: stdoutOnly, Stdout: cmd.OutOrStdout()}
			if cmd.Flags().Changed("description") {
				opts.Description = &description
			}
			return celledit.Run(cmd.Context(), opts)
		},
	}
	add.Flags().StringVar(&description, "description", "", "Set the description (empty clears it; omitted preserves it)")
	add.Flags().BoolVar(&stdoutOnly, "stdout", false, "Print proposed config YAML without writing")
	list := newCellsCmd()
	list.Use = "list"
	group.AddCommand(add, newCellCreateCmd(), list)
	return group
}

func newCellCreateCmd() *cobra.Command {
	var uri, repo, description, requestID string
	var jsonOutput bool
	cmd := &cobra.Command{Use: "create NAME", Short: "Register a cell before submitting work", Args: cobra.ExactArgs(1)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := jobdb.ValidateCellName(args[0]); err != nil {
			return fmt.Errorf("invalid cell name %q: %w", args[0], err)
		}
		normalized, err := jobdb.NormalizeCellRepo(repo)
		if err != nil {
			return fmt.Errorf("--repo must be a portable repository locator: %w", err)
		}
		broker, hasBroker, err := jobcontext.ChildJobBrokerFromEnv(os.Getenv)
		if err != nil {
			return err
		}
		current, hasCurrent, err := jobcontext.CurrentFromEnv(os.Getenv)
		if err != nil {
			return err
		}
		request := jobdb.CreateCellRequest{Name: args[0], Repo: normalized, Description: description, RequestID: requestID}
		var cell jobdb.Cell
		if hasBroker {
			if !hasCurrent || !current.HasJob() {
				return fmt.Errorf("cell creation broker requires current job context")
			}
			if uri != "" {
				return fmt.Errorf("--jobdb cannot retarget cell creation inside a running task")
			}
			cell, err = childbroker.CreateCell(cmd.Context(), broker, request)
		} else {
			if hasCurrent {
				return fmt.Errorf("cell creation inside a running job requires a task broker")
			}
			target, resolveErr := defaults.ResolveJobDBTarget(cmd.Context(), "", uri)
			if resolveErr != nil {
				return resolveErr
			}
			handle, openErr := swfruntime.Open(cmd.Context(), target.RuntimeURL)
			if openErr != nil {
				return openErr
			}
			defer handle.Cleanup()
			request.TenantID = target.TenantID
			cell, err = handle.Runtime.CreateCell(cmd.Context(), request)
		}
		if err != nil {
			return fmt.Errorf("create cell %q: %w", args[0], err)
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(cell)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", cell.ID, cell.Name, cell.Repo)
		return err
	}
	f := cmd.Flags()
	f.StringVar(&uri, "jobdb", "", "JobDB URI")
	f.StringVar(&repo, "repo", "", "Repository locator (required; no Git repository is created)")
	f.StringVar(&description, "description", "", "Cell description")
	f.StringVar(&requestID, "request-id", "", "Stable idempotency key for retrying the same cell creation")
	f.BoolVar(&jsonOutput, "json", false, "Emit the registered cell as JSON")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}
