package listjobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/swf-go/pkg/swf"
)

type Options struct {
	TenantID string
	SWFURL   string

	Statuses      []string
	JobTypes      []string
	JobIDs        []string
	WaitingFor    []string
	CreatedAfter  string
	CreatedBefore string
	PageSize      int
	PageToken     string
	All           bool
	JSONOutput    bool
	Self          bool
	Cell          string
	WorkingDir    string

	Stdout io.Writer
	Stderr io.Writer
}

func (o *Options) Complete(ctx context.Context) error {
	if strings.TrimSpace(o.SWFURL) == "" {
		o.SWFURL = strings.TrimSpace(os.Getenv(defaults.SWFEnv))
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		o.SWFURL = defaults.SWFURL
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
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
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

func (o Options) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--tenant-id is required (or %s, or project self.tenant_id/self.repo)", defaults.TenantEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--swf-url is required (or %s)", defaults.SWFEnv)
	}
	if o.PageSize < 0 {
		return fmt.Errorf("--page-size must be >= 0")
	}
	if o.Self && strings.TrimSpace(o.Cell) != "" {
		return fmt.Errorf("--self and --cell are mutually exclusive")
	}
	if _, err := parseJobStatuses(o.Statuses); err != nil {
		return err
	}
	if _, err := parseWaitingForFilters(o.WaitingFor); err != nil {
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

func parseJobStatuses(values []string) ([]swf.JobStatus, error) {
	if len(values) == 0 {
		return defaultVisibleStatuses(), nil
	}

	out := make([]swf.JobStatus, 0, len(values))
	for _, value := range values {
		for _, part := range splitCSV(value) {
			status := swf.JobStatus(strings.ToUpper(strings.TrimSpace(part)))
			switch status {
			case swf.JobStatusReady,
				swf.JobStatusExpired,
				swf.JobStatusPendingJobs,
				swf.JobStatusAwaitingFuture,
				swf.JobStatusActive,
				swf.JobStatusCrashConcern,
				swf.JobStatusCancelled,
				swf.JobStatusCompleted:
				out = append(out, status)
			default:
				return nil, fmt.Errorf("unsupported --status %q", part)
			}
		}
	}
	return out, nil
}

func defaultVisibleStatuses() []swf.JobStatus {
	return []swf.JobStatus{
		swf.JobStatusReady,
		swf.JobStatusExpired,
		swf.JobStatusPendingJobs,
		swf.JobStatusAwaitingFuture,
		swf.JobStatusActive,
		swf.JobStatusCrashConcern,
	}
}

func storesForStatuses(statuses []swf.JobStatus) []swf.JobStore {
	hasActive := false
	hasArchived := false
	for _, status := range statuses {
		switch status {
		case swf.JobStatusCancelled, swf.JobStatusCompleted:
			hasArchived = true
		default:
			hasActive = true
		}
	}

	switch {
	case hasActive && hasArchived:
		return []swf.JobStore{swf.JobStoreActive, swf.JobStoreArchived}
	case hasArchived:
		return []swf.JobStore{swf.JobStoreArchived}
	default:
		return []swf.JobStore{swf.JobStoreActive}
	}
}

func parseWaitingForFilters(values []string) ([]swf.JobTaskFilter, error) {
	out := make([]swf.JobTaskFilter, 0, len(values))
	for _, value := range values {
		for _, part := range splitCSV(value) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			jobType, taskType, ok := strings.Cut(part, ":")
			if !ok || strings.TrimSpace(jobType) == "" || strings.TrimSpace(taskType) == "" {
				return nil, fmt.Errorf("--waiting-for must be in JOBTYPE:TASKTYPE form, got %q", part)
			}
			out = append(out, swf.JobTaskFilter{
				JobType:  strings.TrimSpace(jobType),
				TaskType: strings.TrimSpace(taskType),
			})
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
