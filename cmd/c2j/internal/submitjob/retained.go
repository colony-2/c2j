package submitjob

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Check fresh CLI preparation before saving or publishing. Historical replay
// continues to use Descriptor.Job as authoritative, without rebuilding input.
func validatePreparedWaits(d starter.Descriptor, deps []jobdb.Dependency, availableAt *time.Time) error {
	var input workflowctl.StartJob
	if err := json.Unmarshal(d.Input, &input); err != nil {
		return err
	}
	sameTime := func(a, b *time.Time) bool {
		return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
	}
	if !slices.Equal(d.Job.Dependencies, deps) || !slices.Equal(input.Dependencies, deps) || !sameTime(d.Job.AvailableAt, availableAt) || !sameTime(input.AvailableAt, availableAt) {
		return fmt.Errorf("prepared submission changed dependencies or earliest start time")
	}
	return nil
}

type retainedSubmission struct {
	Version    int                `json:"version"`
	RuntimeURL string             `json:"runtime_url"`
	TenantID   string             `json:"tenant_id"`
	Broker     bool               `json:"broker"`
	Descriptor starter.Descriptor `json:"descriptor"`
}

func saveRetained(path string, d retainedSubmission) error {
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("retain request before publication: %w", err)
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}
func replaySelected(ctx context.Context, opts Options, route *submissionRoute) error {
	raw, err := os.ReadFile(opts.ReplayFile)
	if err != nil {
		return err
	}
	var retained retainedSubmission
	if err = json.Unmarshal(raw, &retained); err != nil {
		return err
	}
	if retained.Version != 1 || retained.TenantID != opts.TenantID || retained.RuntimeURL != opts.SWFURL {
		return fmt.Errorf("retained request destination differs from configured JobDB")
	}
	job, err := retained.Descriptor.Request()
	if err != nil {
		return err
	}
	if job.TenantId != retained.TenantID || job.GoalID == "" {
		return fmt.Errorf("invalid retained selected submission")
	}
	if route.hasBroker != retained.Broker {
		return fmt.Errorf("retained submission route changed")
	}
	handle, err := route.openRuntime(ctx, opts.SWFURL, swfruntime.Open)
	if err != nil {
		return err
	}
	if handle != nil {
		defer handle.Cleanup()
	}
	return publishRetained(ctx, opts, retained, route.broker, handle)
}
func publishRetained(ctx context.Context, opts Options, d retainedSubmission, broker jobcontext.ChildJobBroker, handle *swfruntime.Handle) error {
	var start workflowctl.StartJob
	if err := json.Unmarshal(d.Descriptor.Input, &start); err != nil {
		return err
	}
	var result submitResult
	if d.Broker {
		ref, err := childbroker.SubmitSelected(ctx, broker, d.Descriptor)
		if err != nil {
			return fmt.Errorf("selected submission ambiguous or rejected; retain request and destination: %w", err)
		}
		if ref.Cell != d.Descriptor.Job.Cell || ref.CellRepo != d.Descriptor.Job.CellRepo || ref.Recipe != start.RecipeName {
			return jobdb.ErrSubmissionReceiptUnavailable
		}
		result = submitResult{Cell: ref.Cell, CellID: ref.CellID, CellRepo: ref.CellRepo, TenantID: ref.TenantID, JobID: ref.JobID, GoalID: ref.GoalID, Recipe: ref.Recipe}
	} else {
		submitter, ok := handle.Engine.(jobref.Submitter)
		if !ok {
			return fmt.Errorf("submitter must preserve complete job references")
		}
		ref, err := d.Descriptor.Submit(ctx, submitter)
		if err != nil {
			return fmt.Errorf("selected submission ambiguous or rejected; retain request and destination: %w", err)
		}
		result = submitResult{Cell: ref.Cell, CellID: ref.CellID, CellRepo: ref.CellRepo, TenantID: ref.TenantID, JobID: ref.JobID, GoalID: ref.GoalID, Recipe: start.RecipeName}
	}
	if err := writeSubmitResult(opts, result); err != nil {
		return err
	}
	if opts.RunAfterSubmit {
		if handle != nil {
			handle.Cleanup()
		}
		return runjob.Run(ctx, runjob.Options{ExecutionFlags: opts.ExecutionFlags, JobID: result.JobID, JobDBURI: opts.JobDBURI, Stdin: opts.Stdin, Stdout: opts.Stdout, Stderr: opts.Stderr})
	}
	return nil
}
