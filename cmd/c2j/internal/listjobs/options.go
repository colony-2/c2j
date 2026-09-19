package listjobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/executionflags"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Options struct {
	ExecutionFlags          executionflags.Options
	CompatibleWithExecution bool
	IncludeUnresolved       bool
	ExecutionFilter         *executionflags.Filter
	JobDBURI                string
	TenantID                string
	SWFURL                  string

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
	filter, err := o.ExecutionFlags.ParseFilter(o.CompatibleWithExecution, o.IncludeUnresolved, os.LookupEnv)
	if err != nil {
		return err
	}
	o.ExecutionFilter = filter
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
	return nil
}

func (o Options) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
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
	for _, jobType := range o.JobTypes {
		if err := jobdb.ValidateIdentifier(jobType); err != nil {
			return fmt.Errorf("--job-type: %w", err)
		}
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
		return defaultVisibleStatuses(), nil
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

func defaultVisibleStatuses() []jobdb.JobStatus {
	return recipejob.DefaultVisibleStatuses()
}

func storesForStatuses(statuses []jobdb.JobStatus) []jobdb.JobStore {
	return recipejob.StoresForStatuses(statuses)
}

func parseWaitingForFilters(values []string) ([]jobdb.JobTaskFilter, error) {
	out := make([]jobdb.JobTaskFilter, 0, len(values))
	for _, value := range values {
		var route jobdb.Route
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&route); err != nil {
			return nil, fmt.Errorf("--waiting-for requires a JSON object with jobType and taskType: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("--waiting-for requires exactly one JSON object")
		}
		if err := route.Validate(); err != nil {
			return nil, fmt.Errorf("--waiting-for: %w", err)
		}
		if err := jobdb.ValidateIdentifier(route.TaskType); err != nil {
			return nil, fmt.Errorf("--waiting-for taskType: %w", err)
		}
		out = append(out, jobdb.JobTaskFilter{JobType: route.JobType, TaskType: route.TaskType})
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
