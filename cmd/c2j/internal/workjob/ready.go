package workjob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type ReadyOptions struct {
	JobDBURI   string
	TenantID   string
	SWFURL     string
	WorkingDir string
	Stdout     io.Writer
	Stderr     io.Writer
}

func (o *ReadyOptions) Complete(ctx context.Context) error {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if strings.TrimSpace(o.WorkingDir) == "" {
		if cwd, err := os.Getwd(); err == nil {
			o.WorkingDir = cwd
		}
	}
	if strings.TrimSpace(o.WorkingDir) != "" {
		if absPath, err := filepath.Abs(o.WorkingDir); err == nil {
			o.WorkingDir = absPath
		}
	}
	target, err := defaults.ResolveJobDBTarget(ctx, o.WorkingDir, o.JobDBURI)
	if err != nil {
		return err
	}
	o.JobDBURI = target.URI
	o.SWFURL = target.RuntimeURL
	o.TenantID = target.TenantID
	return nil
}

func (o ReadyOptions) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if err := validateJobDBRuntimeURL(o.SWFURL); err != nil {
		return err
	}
	return nil
}

func Ready(ctx context.Context, opts ReadyOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := opts.Complete(ctx); err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	if err := opts.Validate(); err != nil {
		return exitError{code: exitCodeInvalidOptions, err: err}
	}

	count, err := CountReady(ctx, opts)
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	_, err = fmt.Fprintln(opts.Stdout, count)
	if err != nil {
		return exitError{code: exitCodeFailure, err: err}
	}
	return nil
}

func CountReady(ctx context.Context, opts ReadyOptions) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := opts.Complete(ctx); err != nil {
		return 0, err
	}
	if err := opts.Validate(); err != nil {
		return 0, err
	}

	handle, err := swfruntime.Open(ctx, opts.SWFURL)
	if err != nil {
		return 0, fmt.Errorf("open JobDB runtime: %w", err)
	}
	defer handle.Cleanup()

	count := 0
	request := readyListRequest(opts.TenantID, "")
	for {
		resp, err := handle.Engine.ListJobs(ctx, request)
		if err != nil {
			return 0, fmt.Errorf("list ready jobs: %w", err)
		}
		count += len(resp.Jobs)
		if strings.TrimSpace(resp.NextPageToken) == "" {
			break
		}
		request.PageToken = resp.NextPageToken
	}
	return count, nil
}

func readyListRequest(tenantID string, pageToken string) jobdb.ListJobsRequest {
	return jobdb.ListJobsRequest{
		TenantIds: []string{tenantID},
		Statuses:  []jobdb.JobStatus{jobdb.JobStatusReady},
		Stores:    []jobdb.JobStore{jobdb.JobStoreActive},
		JobTypes:  []string{starter.RecipeJobType},
		PageSize:  jobdb.MaxListJobsPageSize,
		PageToken: pageToken,
	}
}
