package runjob

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const provenanceTimeout = 2 * time.Second

type workerVersions struct {
	C2J    string  `json:"c2j"`
	Commit *string `json:"c2j.commit,omitempty"`
	JobDB  string  `json:"jobdb"`
}

type executionDiagnostic struct {
	Message               string                              `json:"message"`
	JobID                 string                              `json:"jobid"`
	TenantID              string                              `json:"tenant_id"`
	Ordinal               *int64                              `json:"ordinal"`
	Writers               []workerVersions                    `json:"writers"`
	Versions              workerVersions                      `json:"versions"`
	Kind                  string                              `json:"kind"`
	Reason                jobdb.ExecutionInfrastructureReason `json:"reason"`
	Operation             string                              `json:"operation"`
	WritersScope          string                              `json:"writers_scope"`
	WritersStatus         jobdb.ExecutionProvenanceStatus     `json:"writers_status"`
	ThroughOrdinal        *int64                              `json:"writers_through_ordinal"`
	ScannedThroughOrdinal *int64                              `json:"writers_scanned_through_ordinal"`
	Issues                []jobdb.ExecutionProvenanceIssue    `json:"writers_issues"`
	OmittedIssues         int                                 `json:"writers_omitted_issue_count"`
	OmittedPairs          int                                 `json:"writers_omitted_pair_count"`
	UnexaminedRecords     *int64                              `json:"writers_unexamined_record_count"`
	LookupFailure         string                              `json:"writers_lookup_failure,omitempty"`
	Hint                  string                              `json:"hint"`
}

// The CLI suppresses its generic error only after the JSON was written. Library
// callers still receive the original typed cause and exit status.
type reportedExecutionError struct{ error }

func (e reportedExecutionError) Unwrap() error         { return e.error }
func (reportedExecutionError) ExitCode() int           { return exitCodeFailure }
func (reportedExecutionError) DiagnosticPrinted() bool { return true }

func reportExecutionFailure(ctx context.Context, opts Options, runtime any, err error) error {
	if !jobdb.IsExecutionInfrastructureError(err) {
		return err
	}
	d := makeExecutionDiagnostic(ctx, runtime, jobdb.JobKey{TenantId: opts.TenantID, JobId: opts.JobID}, err, provenanceTimeout)
	if writeErr := json.NewEncoder(opts.Stderr).Encode(d); writeErr != nil {
		// Retain the primary cause even when the diagnostic destination fails.
		return err
	}
	return reportedExecutionError{err}
}

func makeExecutionDiagnostic(ctx context.Context, runtime any, key jobdb.JobKey, err error, timeout time.Duration) executionDiagnostic {
	classified := jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDeterminism, "replay", &key, nil)
	var infra *jobdb.ExecutionInfrastructureError
	errors.As(classified, &infra)
	// A prerequisite can be the failing source. Query that job, not the caller's
	// history, and preserve its physical ordinal (including zero).
	if infra.JobKey != nil {
		key = *infra.JobKey
	}
	current := compiler.ExecutionConsumerVersions(nil)
	versions := workerVersions{C2J: current["c2j"], JobDB: jobdb.CurrentBuildInfo().Version}
	if commit := current["c2j.commit"]; commit != "" {
		versions.Commit = &commit
	}
	message := "runtime operation failed"
	switch infra.Reason {
	case jobdb.ExecutionInfrastructureDeterminism:
		message = "workflow was not deterministic"
	case jobdb.ExecutionInfrastructureDurableDecode:
		message = "stored execution data could not be decoded"
	}
	// Backend errors may contain task bodies, URLs or credentials. Report the
	// safe reason and source; keep the complete local cause on the returned error.
	d := executionDiagnostic{Message: message, JobID: key.JobId, TenantID: key.TenantId, Ordinal: infra.Ordinal,
		Kind: "execution_infrastructure_error", Reason: infra.Reason, Operation: infra.Operation,
		Versions: versions, Writers: []workerVersions{}, Issues: []jobdb.ExecutionProvenanceIssue{},
		WritersScope: jobdb.ExecutionProvenanceScope, WritersStatus: jobdb.ExecutionProvenanceUnavailable,
		Hint: "Check worker builds against recorded writers. Fix the worker or infrastructure issue before clearing crash concern with c2j job clear-crash-concern JOB --reason 'issue fixed'."}
	lookup, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p, lookupErr := jobdb.ReadExecutionProvenance(lookup, runtime, jobdb.ReadExecutionProvenanceRequest{JobKey: key, Consumer: "c2j"})
	if lookupErr != nil {
		d.LookupFailure = "lookup_failed"
		switch {
		case errors.Is(lookupErr, jobdb.ErrExecutionProvenanceUnsupported):
			d.LookupFailure = "unsupported"
		case errors.Is(lookupErr, context.DeadlineExceeded):
			d.LookupFailure = "timeout"
		case errors.Is(lookupErr, context.Canceled):
			d.LookupFailure = "cancelled"
		case errors.Is(lookupErr, jobdb.ErrLeasePermissionDenied):
			d.LookupFailure = "permission_denied"
		}
		return d
	}
	if p.Capability != jobdb.ExecutionProvenanceCapability || p.Scope != jobdb.ExecutionProvenanceScope || p.Consumer != "c2j" {
		d.LookupFailure = "unsupported_response"
		return d
	}
	d.WritersStatus, d.ThroughOrdinal, d.ScannedThroughOrdinal = p.Status, p.ThroughOrdinal, p.ScannedThroughOrdinal
	d.Issues = append(d.Issues, p.Issues...)
	d.OmittedIssues, d.OmittedPairs, d.UnexaminedRecords = p.OmittedIssueCount, p.OmittedPairCount, p.UnexaminedRecordCount
	// Deduplicate by values, including recorded commit identity, never by
	// independent version lists. The current worker is not a historical writer.
	seenKeys := map[string]bool{}
	for _, pair := range p.Pairs {
		v := workerVersions{C2J: pair.ConsumerVersion, Commit: pair.ConsumerCommit, JobDB: pair.JobDBVersion}
		raw, _ := json.Marshal(v)
		if !seenKeys[string(raw)] {
			seenKeys[string(raw)] = true
			d.Writers = append(d.Writers, v)
		}
	}
	sort.Slice(d.Writers, func(i, j int) bool {
		a, _ := json.Marshal(d.Writers[i])
		b, _ := json.Marshal(d.Writers[j])
		return string(a) < string(b)
	})
	return d
}
