package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/joblabel"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newJobLabelsCmd(uri *string, embed, jsonOutput *bool) *cobra.Command {
	root := &cobra.Command{Use: "labels", Short: "Read and edit versioned job labels"}
	for action, args := range map[string]string{"list": "JOB", "get": "JOB KEY", "set": "JOB KEY VALUE", "remove": "JOB KEY"} {
		var versionText, colorText string
		cmd := &cobra.Command{Use: action + " " + args, Args: cobra.ExactArgs(map[string]int{"list": 1, "get": 2, "set": 3, "remove": 2}[action]), SilenceUsage: true}
		if action == "set" || action == "remove" {
			cmd.Flags().StringVar(&versionText, "if-version", "", "Required expected label version (0 for a new key)")
			_ = cmd.MarkFlagRequired("if-version")
		}
		if action == "set" {
			cmd.Flags().StringVar(&colorText, "color", "", "Required label color")
			_ = cmd.MarkFlagRequired("color")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			edit := joblabel.Edit{Action: action}
			if len(args) > 1 {
				edit.Key = args[1]
				if err := joblabel.ValidateKey(edit.Key); err != nil {
					return err
				}
			}
			if action == "set" || action == "remove" {
				version, err := joblabel.ParseVersion(versionText)
				if err != nil {
					return err
				}
				edit.ExpectedVersion = &version
				if action == "set" {
					color := joblabel.Color(colorText)
					edit.Value, edit.Color = &args[2], &color
				}
				if err := edit.Validate(); err != nil {
					return err
				}
			}
			if _, present, err := jobcontext.CurrentFromEnv(os.Getenv); err != nil {
				return err
			} else if present {
				return errors.New("operator label commands are unavailable inside a task")
			}
			if _, present, err := jobcontext.ChildJobBrokerFromEnv(os.Getenv); err != nil {
				return err
			} else if present {
				return errors.New("operator label commands are unavailable inside a task")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			targetURI := *uri
			if *embed {
				targetURI = defaults.EmbedURL
			}
			ctx := cmd.Context()
			target, err := defaults.ResolveJobDBTarget(ctx, cwd, targetURI)
			if err != nil {
				return err
			}
			handle, err := swfruntime.Open(ctx, target.RuntimeURL)
			if err != nil {
				return err
			}
			defer handle.Cleanup()
			job := jobdb.JobKey{TenantId: target.TenantID, JobId: args[0]}
			out := cmd.OutOrStdout()
			if action == "list" {
				view, err := joblabel.ListJobLabels(ctx, handle.Runtime, job)
				if err != nil {
					return labelCommandError(err)
				}
				if *jsonOutput {
					return json.NewEncoder(out).Encode(view)
				}
				if _, err := fmt.Fprintln(out, view.Projection); err != nil {
					return err
				}
				for _, label := range view.Labels {
					if _, err := fmt.Fprintf(out, "%s=%s  %s  version=%d\n", label.Key, label.Value, label.Color, label.Version); err != nil {
						return err
					}
				}
				return nil
			}
			var state joblabel.State
			switch action {
			case "get":
				state, err = joblabel.GetJobLabel(ctx, handle.Runtime, job, edit.Key)
			case "set":
				state, err = joblabel.SetJobLabel(ctx, handle.Runtime, job, edit.Key, *edit.Value, *edit.Color, *edit.ExpectedVersion)
			case "remove":
				state, err = joblabel.RemoveJobLabel(ctx, handle.Runtime, job, edit.Key, *edit.ExpectedVersion)
			}
			if err != nil {
				return labelCommandError(err)
			}
			if *jsonOutput {
				return json.NewEncoder(out).Encode(state)
			}
			if state.Label != nil {
				_, err = fmt.Fprintf(out, "%s  %s=%s  %s  version=%d\n", state.Status, state.Label.Key, state.Label.Value, state.Label.Color, state.Label.Version)
			} else if state.Version != nil {
				_, err = fmt.Fprintf(out, "%s  version=%d\n", state.Status, *state.Version)
			} else {
				_, err = fmt.Fprintln(out, state.Status)
			}
			return err
		}
		root.AddCommand(cmd)
	}
	return root
}

func labelCommandError(err error) error {
	for _, category := range []struct {
		name  string
		cause error
	}{
		{"conflict", jobdb.ErrConflict},
		{"denied", jobdb.ErrLeasePermissionDenied},
		{"denied", jobdb.ErrCellForbidden},
		{"lease lost", jobdb.ErrExecutionLeaseLost},
		{"not found", jobdb.ErrJobNotFound},
		{"unsupported", jobdb.ErrJobMetadataUnsupported},
	} {
		if errors.Is(err, category.cause) {
			return fmt.Errorf("job label %s: %w", category.name, err)
		}
	}
	return err
}
