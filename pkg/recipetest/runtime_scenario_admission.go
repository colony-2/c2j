package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/colony-2/c2j/internal/scenariotest"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// This wrapper never acquires a held lease. The backend still decides whether
// the listed route remains eligible at the instant of native acquisition.
type scenarioRuntime struct {
	caseRuntime
	f *runtimeFixture
}

type delegatedScenarioRuntime struct{ scenarioRuntime }

func (r delegatedScenarioRuntime) CreateDelegation(ctx context.Context, p jobdb.ExecutionLease, q jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	d, ok := r.caseRuntime.(jobdb.LeaseDelegator)
	if !ok {
		return nil, jobdb.ErrLeaseDelegationUnsupported
	}
	return d.CreateDelegation(ctx, p, q)
}
func (r scenarioRuntime) GetJobLease(ctx context.Context, q jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	r.f.nativeMu.Lock()
	defer r.f.nativeMu.Unlock()
	if err := validateScenarioLease(q.Routes, q.LeaseDuration); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := r.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{q.JobKey.TenantId}, JobKeys: []jobdb.JobKey{q.JobKey}, IncludeHidden: true})
	if err != nil {
		return nil, err
	}
	if len(info.Jobs) != 1 {
		return nil, fmt.Errorf("admission target %s unavailable", q.JobKey.JobId)
	}
	if r.f.admissionHeld(info.Jobs[0], q.Routes) {
		return nil, nil
	}
	l, err := r.caseRuntime.GetJobLease(ctx, q)
	if err == nil && l != nil {
		r.f.admissionRecord(AdmissionReceipt{JobID: q.JobKey.JobId, Kind: "admitted", LeaseID: l.LeaseID(), Route: l.Route()})
	}
	return l, err
}
func (r scenarioRuntime) PollWork(ctx context.Context, q jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	if err := validateScenarioLease(q.Routes, q.LeaseDuration); err != nil {
		return nil, err
	}
	if q.TenantId == "" {
		return nil, fmt.Errorf("tenant_id is required for PollWork")
	}
	if len(q.Routes) == 0 {
		return nil, nil
	}
	if _, err := matchesMetadata(json.RawMessage(`{}`), q.MetadataEquals); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 1
	}
	for {
		r.f.nativeMu.Lock()
		out, err := r.pollPass(ctx, q, limit)
		r.f.nativeMu.Unlock()
		if err != nil {
			for _, l := range out {
				l.StopKeepAlive()
			}
			out = nil
		}
		if err != nil || len(out) > 0 || q.LongPollUntil == nil || !time.Now().Before(*q.LongPollUntil) {
			return out, err
		}
		delay := min(10*time.Millisecond, time.Until(*q.LongPollUntil))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
func (r scenarioRuntime) pollPass(ctx context.Context, q jobdb.PollWorkRequest, limit int) ([]jobdb.ExecutionLease, error) {
	// Preserve metadata predicates by applying their exact native JSON values to
	// each read-only candidate. Listing itself preserves routes, cells and tenant.
	var out []jobdb.ExecutionLease
	token := ""
	seen := map[string]bool{}
	for {
		page, err := r.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{q.TenantId}, CellNames: q.CellNames, AvailableRoutes: q.Routes, PageSize: 200, PageToken: token})
		if err != nil {
			return out, err
		}
		for _, j := range page.Jobs {
			match, err := matchesMetadata(j.Metadata, q.MetadataEquals)
			if err != nil {
				return out, err
			}
			if !match || r.f.admissionHeld(j, q.Routes) {
				continue
			}
			l, err := r.caseRuntime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: j.JobKey, WorkerID: q.WorkerID, Routes: q.Routes, LeaseDuration: q.LeaseDuration})
			if err != nil {
				for _, l := range out {
					l.StopKeepAlive()
				}
				return nil, err
			}
			if l != nil {
				out = append(out, l)
				r.f.admissionRecord(AdmissionReceipt{JobID: j.JobKey.JobId, Kind: "admitted", LeaseID: l.LeaseID(), Route: l.Route()})
				if len(out) >= limit {
					return out, nil
				}
			}
		}
		token = page.NextPageToken
		if token == "" {
			return out, nil
		}
		if seen[token] {
			return out, fmt.Errorf("admission listing repeated cursor")
		}
		seen[token] = true
	}
}
func validateScenarioLease(routes []jobdb.Route, d time.Duration) error {
	for _, route := range routes {
		if err := route.Validate(); err != nil {
			return err
		}
	}
	if d < 0 {
		return fmt.Errorf("lease duration must be >= 0")
	}
	return nil
}
func encodeScenarioMetadata(v any) ([]byte, error) {
	switch value := v.(type) {
	case json.RawMessage:
		if !json.Valid(value) {
			return nil, fmt.Errorf("metadata predicate value must be valid JSON")
		}
		return value, nil
	case []byte:
		if !json.Valid(value) {
			return nil, fmt.Errorf("metadata predicate value must be valid JSON")
		}
		return value, nil
	default:
		return json.Marshal(v)
	}
}
func matchesMetadata(raw json.RawMessage, predicates []jobdb.MetadataPredicate) (bool, error) {
	if len(predicates) == 0 {
		return true, nil
	}
	candidates := make([][]string, len(predicates))
	for i, p := range predicates {
		if len(p.Path) == 0 || len(p.Values) == 0 {
			return false, fmt.Errorf("metadata predicate requires path and values")
		}
		for _, segment := range p.Path {
			if segment == "" {
				return false, fmt.Errorf("metadata predicate path contains empty segment")
			}
		}
		for _, want := range p.Values {
			if want == nil {
				return false, fmt.Errorf("metadata predicate values cannot contain nil")
			}
			b, err := encodeScenarioMetadata(want)
			if err != nil {
				return false, err
			}
			candidates[i] = append(candidates[i], string(b))
		}
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	for i, p := range predicates {
		v := value
		for _, part := range p.Path {
			m, ok := v.(map[string]any)
			if !ok {
				return false, nil
			}
			v, ok = m[part]
			if !ok {
				return false, nil
			}
		}
		actual, err := json.Marshal(v)
		if err != nil {
			return false, err
		}
		found := false
		for _, want := range candidates[i] {
			found = found || string(actual) == want
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}
func (f *runtimeFixture) admissionRecord(r AdmissionReceipt) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.scenario
	key := r.Kind + ":" + r.Hold + ":" + r.JobID + ":" + r.LeaseID
	if s.admissionSeen[key] {
		return
	}
	if len(s.report.Admission) >= f.c.Runtime.Controls.Scenario.limits().Events {
		f.report.Controls.Errors = append(f.report.Controls.Errors, "admission evidence limit exceeded")
		return
	}
	s.admissionSeen[key] = true
	s.position++
	r.Position = s.position
	r.Session = s.session
	s.report.Admission = append(s.report.Admission, r)
}
func (f *runtimeFixture) admissionHeld(j jobdb.JobSummary, routes []jobdb.Route) bool {
	f.mu.Lock()
	s := f.scenario
	if s.stopped {
		f.mu.Unlock()
		return true
	}
	var hold string
	for _, h := range f.c.Runtime.Controls.Scenario.AdmissionHolds {
		if s.released[h.ID] || j.Cell != h.Cell || j.CellRepo != f.seededRepos[h.Cell] {
			continue
		}
		var meta struct {
			Recipe string `json:"recipe"`
		}
		if json.Unmarshal(j.Metadata, &meta) != nil || meta.Recipe != h.Recipe {
			continue
		}
		for _, route := range routes {
			if route == h.Route {
				hold = h.ID
				break
			}
		}
		if hold != "" {
			break
		}
	}
	f.mu.Unlock()
	if hold != "" {
		f.admissionRecord(AdmissionReceipt{JobID: j.JobKey.JobId, Kind: "held", Hold: hold})
		return true
	}
	return false
}
func (f *runtimeFixture) beginSubmission(ctx context.Context, s scenariotest.Submission) (func(jobref.Ref, error) error, error) {
	// Admission and submission receipts share this short critical section. Never
	// hold it while a task is gated or while waiting for long polling.
	f.nativeMu.Lock()
	return func(ref jobref.Ref, err error) error {
		defer f.nativeMu.Unlock()
		if err != nil {
			return err
		}
		if s.Caller == "" {
			return nil
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		s.Ref = ref
		for _, a := range f.report.Controls.Dispatch {
			if a.Kind == "task" && a.JobID == s.Caller && a.InvocationID == s.InvocationID && a.Attempt.Attempt == s.Attempt {
				s.NodePath = a.NodePath
				s.TaskType = a.Name
				s.ScopeID = a.ScopeID
			}
		}
		if s.InvocationID == "" || s.TaskType == "" {
			return fmt.Errorf("native submission lacks invocation provenance")
		}
		s.Index = 1
		for _, old := range f.scenario.report.Receipts {
			if old.Caller == s.Caller && old.InvocationID == s.InvocationID && old.Attempt == s.Attempt {
				s.Index++
			}
		}

		if len(f.scenario.report.Receipts) >= f.c.Runtime.Controls.Scenario.limits().Receipts {
			f.report.Controls.Errors = append(f.report.Controls.Errors, "submission receipt limit exceeded")
			return fmt.Errorf("submission receipt limit exceeded")
		}
		f.scenario.report.Receipts = append(f.scenario.report.Receipts, s)
		return nil
	}, nil
}
