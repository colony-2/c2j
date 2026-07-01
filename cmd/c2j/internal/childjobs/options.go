package childjobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Options struct {
	JobDBURI string
	TenantID string
	SWFURL   string

	ParentTenantID       string
	ParentJobID          string
	ParentInvocationHash string
	AllParentInvocations bool

	Statuses      []string
	CreatedAfter  string
	CreatedBefore string
	PageSize      int
	PageToken     string
	All           bool
	JSONOutput    bool
	WorkingDir    string

	Stdout io.Writer
	Stderr io.Writer
}

func (o *Options) Complete(ctx context.Context) error {
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
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	target, err := defaults.ResolveJobDBTarget(ctx, o.WorkingDir, o.JobDBURI)
	if err != nil {
		return err
	}
	o.JobDBURI = target.URI
	o.SWFURL = target.RuntimeURL
	o.TenantID = target.TenantID

	current, ok, err := jobcontext.CurrentFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if ok {
		if strings.TrimSpace(o.ParentTenantID) == "" {
			o.ParentTenantID = current.TenantID
		}
		if strings.TrimSpace(o.ParentJobID) == "" {
			o.ParentJobID = current.JobID
		}
		if strings.TrimSpace(o.ParentInvocationHash) == "" {
			o.ParentInvocationHash = current.InvocationHash
		}
	}
	return nil
}

func (o Options) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.ParentTenantID) == "" {
		return fmt.Errorf("parent tenant ID is required; run inside an op or pass --parent-tenant-id")
	}
	if strings.TrimSpace(o.ParentJobID) == "" {
		return fmt.Errorf("parent job ID is required; run inside an op or pass --parent-job-id")
	}
	if strings.TrimSpace(o.ParentTenantID) != strings.TrimSpace(o.TenantID) {
		return fmt.Errorf("child job listing is limited to the selected tenant %q; parent tenant is %q", o.TenantID, o.ParentTenantID)
	}
	if !o.AllParentInvocations && strings.TrimSpace(o.ParentInvocationHash) == "" {
		return fmt.Errorf("parent invocation hash is required; run inside an op, pass --parent-invocation-hash, or use --all-ops")
	}
	if o.PageSize < 0 {
		return fmt.Errorf("--page-size must be >= 0")
	}
	if _, err := parseJobStatuses(o.Statuses); err != nil {
		return err
	}
	if _, err := parseOptionalTime(o.CreatedAfter); err != nil {
		return fmt.Errorf("--created-after: %w", err)
	}
	if _, err := parseOptionalTime(o.CreatedBefore); err != nil {
		return fmt.Errorf("--created-before: %w", err)
	}
	after, _ := parseOptionalTime(o.CreatedAfter)
	before, _ := parseOptionalTime(o.CreatedBefore)
	if after != nil && before != nil && after.After(*before) {
		return fmt.Errorf("--created-after must be <= --created-before")
	}
	return nil
}

func parseJobStatuses(values []string) ([]jobdb.JobStatus, error) {
	if len(values) == 0 {
		return recipejob.DefaultVisibleStatuses(), nil
	}

	out := make([]jobdb.JobStatus, 0, len(values))
	for _, value := range values {
		for _, part := range splitCSV(value) {
			status := jobdb.JobStatus(strings.ToUpper(strings.TrimSpace(part)))
			switch status {
			case jobdb.JobStatusReady,
				jobdb.JobStatusExpired,
				jobdb.JobStatusPendingJobs,
				jobdb.JobStatusAwaitingFuture,
				jobdb.JobStatusActive,
				jobdb.JobStatusCrashConcern,
				jobdb.JobStatusCancelled,
				jobdb.JobStatusCompleted:
				out = append(out, status)
			default:
				return nil, fmt.Errorf("unsupported --status %q", part)
			}
		}
	}
	return out, nil
}

func parseOptionalTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("expected RFC3339 timestamp")
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
