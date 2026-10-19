package deletejobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Options struct {
	JobDBURI, WorkingDir             string
	Self                             bool
	Cells, JobIDs                    []string
	Recipe, CreatedBefore, OlderThan string
	All, DryRun, JSONOutput          bool
	Stdout                           io.Writer
}

type Selection struct {
	TenantID      string     `json:"tenant_id"`
	Repositories  []string   `json:"repositories,omitempty"`
	Recipe        string     `json:"recipe,omitempty"`
	JobIDs        []string   `json:"job_ids,omitempty"`
	CreatedBefore *time.Time `json:"created_before,omitempty"`
	All           bool       `json:"all"`
	DryRun        bool       `json:"dry_run"`
}

type Result struct {
	Selection
	MatchedCount int64 `json:"matched_count"`
	DeletedCount int64 `json:"deleted_count"`
}

func Run(ctx context.Context, opts Options) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	target, err := defaults.ResolveJobDBTarget(ctx, opts.WorkingDir, opts.JobDBURI)
	if err != nil {
		return err
	}
	if target.TenantID == "" || target.RuntimeURL == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	req, selection, err := buildRequest(ctx, opts, target.TenantID, time.Now().UTC())
	if err != nil {
		return err
	}
	handle, err := swfruntime.Open(ctx, target.RuntimeURL)
	if err != nil {
		return fmt.Errorf("open JobDB runtime: %w", err)
	}
	defer handle.Cleanup()
	admin, ok := handle.Runtime.(jobdb.JobDeletionRuntime)
	if !ok {
		return jobdb.ErrJobDeletionUnsupported
	}
	response, err := admin.DeleteJobs(ctx, req)
	if err != nil {
		return fmt.Errorf("delete jobs: %w", err)
	}
	result := Result{Selection: selection, MatchedCount: response.MatchedCount, DeletedCount: response.DeletedCount}
	if opts.JSONOutput {
		return json.NewEncoder(opts.Stdout).Encode(result)
	}
	// Show the exact resolved selection, including the fixed age cutoff.
	raw, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(opts.Stdout, "Selection: %s\nMatched: %d\nDeleted: %d\n", raw, result.MatchedCount, result.DeletedCount)
	return err
}

func buildRequest(ctx context.Context, opts Options, tenant string, now time.Time) (jobdb.DeleteJobsRequest, Selection, error) {
	req := jobdb.DeleteJobsRequest{TenantID: tenant, JobIDs: append([]string(nil), opts.JobIDs...), All: opts.All, DryRun: opts.DryRun}
	selection := Selection{TenantID: tenant, JobIDs: req.JobIDs, Recipe: opts.Recipe, All: opts.All, DryRun: opts.DryRun}
	invalid := func(message string) (jobdb.DeleteJobsRequest, Selection, error) {
		return jobdb.DeleteJobsRequest{}, Selection{}, fmt.Errorf("%w: %s", jobdb.ErrInvalidJobDeletion, message)
	}
	if opts.Self && len(opts.Cells) != 0 {
		return invalid("--self and --cell are mutually exclusive")
	}
	if opts.All && (opts.Self || len(opts.Cells) > 0 || len(opts.JobIDs) > 0 || opts.Recipe != "" || opts.CreatedBefore != "" || opts.OlderThan != "") {
		return invalid("--all cannot be combined with selectors")
	}
	if opts.CreatedBefore != "" && opts.OlderThan != "" {
		return invalid("--created-before and --older-than are mutually exclusive")
	}
	if opts.CreatedBefore != "" {
		cutoff, err := time.Parse(time.RFC3339Nano, opts.CreatedBefore)
		if err != nil {
			return invalid("--created-before must be an RFC3339 timestamp")
		}
		cutoff = cutoff.UTC()
		req.CreatedBefore = &cutoff
	}
	if opts.OlderThan != "" {
		age, err := time.ParseDuration(opts.OlderThan)
		if err != nil || age <= 0 {
			return invalid("--older-than must be a positive Go duration, e.g. 720h")
		}
		cutoff := now.UTC().Add(-age)
		req.CreatedBefore = &cutoff
	}
	selection.CreatedBefore = req.CreatedBefore
	filter := jobdb.Metadata()
	if opts.Self || len(opts.Cells) > 0 {
		cells := opts.Cells
		if opts.Self {
			cells = []string{""}
		}
		for _, cell := range cells {
			if !opts.Self && strings.TrimSpace(cell) == "" {
				return invalid("--cell must not be empty")
			}
			target, err := recipejob.ResolveTarget(ctx, recipejob.ResolveTargetRequest{WorkingDir: opts.WorkingDir, TenantID: tenant, Self: opts.Self, Cell: cell})
			if err != nil {
				return jobdb.DeleteJobsRequest{}, Selection{}, fmt.Errorf("resolve deletion cell: %w", err)
			}
			eq, err := jobdb.Metadata().EqualFilter("repo", target.RepositorySource)
			if err != nil {
				return invalid(err.Error())
			}
			filter, err = filter.OrFilter(eq)
			if err != nil {
				return invalid(err.Error())
			}
			selection.Repositories = append(selection.Repositories, target.RepositorySource)
		}
	}
	if opts.Recipe != "" {
		if strings.TrimSpace(opts.Recipe) == "" {
			return invalid("--recipe must not be empty")
		}
		var err error
		filter, err = filter.EqualFilter("recipe", opts.Recipe)
		if err != nil {
			return invalid(err.Error())
		}
	}
	req.MetadataFilter = filter
	if err := req.Validate(); err != nil {
		return jobdb.DeleteJobsRequest{}, Selection{}, err
	}
	return req, selection, nil
}
