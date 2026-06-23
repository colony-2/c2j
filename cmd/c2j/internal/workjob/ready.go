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
	TenantID   string
	SWFURL     string
	WorkingDir string
	Stdout     io.Writer
	Stderr     io.Writer
}

func (o *ReadyOptions) Complete(ctx context.Context) error {
	if strings.TrimSpace(o.SWFURL) == "" {
		o.SWFURL = strings.TrimSpace(os.Getenv(defaults.SWFEnv))
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		o.SWFURL = defaults.SWFURL
	}
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
	if strings.TrimSpace(o.TenantID) == "" {
		o.TenantID = strings.TrimSpace(os.Getenv(defaults.TenantEnv))
	}
	if strings.TrimSpace(o.TenantID) == "" {
		tenantID, err := defaults.ResolveTenantID(ctx, o.WorkingDir)
		if err != nil {
			return err
		}
		o.TenantID = tenantID
	}
	return nil
}

func (o ReadyOptions) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--tenant-id is required (or %s, or project self.tenant_id/self.repo)", defaults.TenantEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--swf-url is required (or %s)", defaults.SWFEnv)
	}
	if err := validateSWFURL(o.SWFURL); err != nil {
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
		return 0, fmt.Errorf("open SWF runtime: %w", err)
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
