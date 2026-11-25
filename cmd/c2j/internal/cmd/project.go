package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func newProjectCmd() *cobra.Command {
	group := &cobra.Command{Use: "project", Short: "Create and list JobDB projects"}
	group.AddCommand(newProjectCreateCmd(), newProjectListCmd())
	return group
}

type projectFlags struct {
	uri        string
	embed      bool
	jsonOutput bool
}

func (options *projectFlags) bind(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.StringVar(&options.uri, "jobdb", "", "JobDB server URL (optional project ID does not scope the catalog)")
	flags.BoolVar(&options.embed, "embed", false, "Use the local embedded JobDB")
	flags.BoolVar(&options.jsonOutput, "json", false, "Emit JSON on stdout")
	cmd.MarkFlagsMutuallyExclusive("jobdb", "embed")
}

func (options projectFlags) target(cmd *cobra.Command) (defaults.JobDBTarget, error) {
	if _, present, err := jobcontext.CurrentFromEnv(os.Getenv); err != nil || present {
		return defaults.JobDBTarget{}, errors.New("project commands are not available inside a running task")
	}
	if _, present, err := jobcontext.ChildJobBrokerFromEnv(os.Getenv); err != nil || present {
		return defaults.JobDBTarget{}, errors.New("project commands are not available through a task broker")
	}
	uri := options.uri
	if options.embed {
		uri = "embed:///"
	}
	target, err := defaults.ResolveProjectCatalogTarget(cmd.Context(), "", uri)
	if err == nil && target.RuntimeURL == "" {
		err = errors.New("JobDB target is required: use --jobdb or --embed, or configure C2J_JOBDB or project config")
	}
	return target, err
}

func newProjectCreateCmd() *cobra.Command {
	var options projectFlags
	var metadata []string
	var input jobdb.CreateProjectRequest
	cmd := &cobra.Command{Use: "create", Short: "Create a JobDB project and return its ID", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		target, err := options.target(cmd)
		if err != nil {
			return err
		}
		request := input
		request.Metadata, err = parseProjectMetadata(metadata)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("request-id") {
			id, err := uuid.NewRandom()
			if err != nil {
				return fmt.Errorf("generate project request ID: %w", err)
			}
			request.RequestID = id.String()
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Project request ID: %s\n", request.RequestID); err != nil {
				return err
			}
		}
		result, err := swfruntime.CreateProject(cmd.Context(), target, request)
		if err != nil {
			switch {
			case errors.Is(err, jobdb.ErrInvalidProject), errors.Is(err, jobdb.ErrProjectConflict),
				errors.Is(err, jobdb.ErrProjectAccessDenied), errors.Is(err, jobdb.ErrProjectNotFound):
				return fmt.Errorf("create project: %w", err)
			default:
				return fmt.Errorf("create project: %w; if retrying, use --request-id %q with identical input", err, request.RequestID)
			}
		}
		if options.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", result.Project.ID, result.Project.Name)
		return err
	}
	options.bind(cmd)
	flags := cmd.Flags()
	flags.StringVar(&input.Name, "name", "", "Project name (required)")
	flags.StringVar(&input.Description, "description", "", "Project description")
	flags.StringArrayVar(&metadata, "metadata", nil, "Project metadata entry KEY=JSON_VALUE (repeatable)")
	flags.StringVar(&input.RequestID, "request-id", "", "Reuse this ID with identical input to retry a create")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func parseProjectMetadata(flags []string) (json.RawMessage, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	values := make(map[string]json.RawMessage, len(flags))
	for _, flag := range flags {
		key, raw, found := strings.Cut(flag, "=")
		if !found || key == "" || !json.Valid([]byte(raw)) {
			return nil, errors.New("--metadata requires KEY=JSON_VALUE")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate --metadata key %q", key)
		}
		values[key] = json.RawMessage(raw)
	}
	return json.Marshal(values)
}

func newProjectListCmd() *cobra.Command {
	var options projectFlags
	var request jobdb.ListProjectsRequest
	cmd := &cobra.Command{Use: "list", Short: "List one page of JobDB projects", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		target, err := options.target(cmd)
		if err != nil {
			return err
		}
		result, err := swfruntime.ListProjects(cmd.Context(), target, request)
		if err != nil {
			return fmt.Errorf("list projects: %w", err)
		}
		if options.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(out, "ID\tNAME"); err != nil {
			return err
		}
		for _, project := range result.Projects {
			if _, err := fmt.Fprintf(out, "%s\t%s\n", project.ID, project.Name); err != nil {
				return err
			}
		}
		if err := out.Flush(); err != nil {
			return err
		}
		if result.NextPageToken != "" {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Next page token: %s\n", result.NextPageToken)
		}
		return err
	}
	options.bind(cmd)
	flags := cmd.Flags()
	flags.StringVar(&request.Name, "name", "", "Filter by exact project name")
	flags.IntVar(&request.PageSize, "page-size", 0, "Maximum projects on this page (server default: 50)")
	flags.StringVar(&request.PageToken, "page-token", "", "Continue with the same filter and page size using the returned token")
	return cmd
}
