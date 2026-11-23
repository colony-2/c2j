package recipetest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/internal/dispatchtest"
	inputop "github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/secrets"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

type PendingResponseIdentity struct {
	TenantID        string            `json:"tenant_id"`
	JobID           string            `json:"job_id"`
	Job             string            `json:"job"`
	Task            TaskSelection     `json:"task"`
	WaitOrdinal     int64             `json:"wait_ordinal"`
	OutputOrdinal   int64             `json:"output_ordinal"`
	RequestID       string            `json:"request_id"`
	Form            inputop.InputForm `json:"form"`
	FormSHA256      string            `json:"form_sha256"`
	InputSHA256     string            `json:"input_sha256"`
	SchedulerSHA256 string            `json:"scheduler_sha256"`
}
type DurableResponseReceipt struct {
	Status        string           `json:"status"` // present, absent, unreadable, incomplete
	Receipt       *inputop.Receipt `json:"receipt"`
	OutputOrdinal int64            `json:"output_ordinal"`
	Error         string           `json:"error,omitempty"`
}
type ResponseRecoveryLease struct {
	ID          string `json:"id"`
	WorkerID    string `json:"worker_id"`
	ExpiresAtNS int64  `json:"expires_at_ns"`
	Expired     bool   `json:"expired"`
}
type ResponseStateSnapshot struct {
	Status         string                 `json:"status"`
	Route          *jobdb.Route           `json:"route"`
	Waiting        *jobdb.TaskWait        `json:"waiting"`
	HandlePresent  bool                   `json:"handle_present"`
	HandleOrdinal  int64                  `json:"handle_ordinal"`
	Actionable     bool                   `json:"actionable"`
	Lease          *ResponseRecoveryLease `json:"lease"`
	Durable        DurableResponseReceipt `json:"durable"`
	InputUnchanged bool                   `json:"input_unchanged"`
	FormSHA256     string                 `json:"form_sha256,omitempty"`
	Session        int                    `json:"session"`
	Position       int                    `json:"position"`
	Dispatches     int                    `json:"dispatches"`
}
type ResponseCheckpoint struct {
	Event        string                `json:"event"`
	Status       string                `json:"status"`
	RecoveryMode string                `json:"recovery_mode"`
	Replay       *ResponseReplay       `json:"replay"`
	State        ResponseStateSnapshot `json:"state"`
}
type SubmissionAttempt struct {
	Omitted         []string                `json:"omitted,omitempty"`
	Version         int                     `json:"version"`
	ID              string                  `json:"id"`
	ResponseIndex   int                     `json:"response_index"`
	SubmissionID    string                  `json:"submission_id"`
	Pending         PendingResponseIdentity `json:"pending"`
	Actor           inputop.Actor           `json:"actor"`
	Attempted       bool                    `json:"attempted"`
	APIError        string                  `json:"api_error"`
	ErrorKind       string                  `json:"error_kind"`
	ExpectedError   *ResponseExpectedError  `json:"expected_error"`
	Matched         bool                    `json:"matched"`
	ReturnedReceipt *inputop.Receipt        `json:"returned_receipt"`
	Durable         DurableResponseReceipt  `json:"durable"`
	Before          *ResponseStateSnapshot  `json:"before"`
	After           *ResponseStateSnapshot  `json:"after"`
	FaultStage      string                  `json:"fault_stage,omitempty"`
	Fault           *ResponseFaultEvidence  `json:"fault,omitempty"`
	Checkpoints     []ResponseCheckpoint    `json:"checkpoints"`
	Complete        bool                    `json:"complete"`
	Errors          []string                `json:"errors"`
}
type controlledResponse struct {
	evidence         *SubmissionAttempt
	form             inputop.InputForm
	input            []byte
	leaseExpired     bool
	dispatchBaseline int
}
type ResponseReplay struct {
	JobID     string       `json:"job_id"`
	Session   int          `json:"session"`
	Position  int          `json:"position"`
	Route     *jobdb.Route `json:"route"`
	Finished  bool         `json:"finished"`
	Suspended bool         `json:"suspended"`
	Error     string       `json:"error,omitempty"`
}
type responseJobWorker struct {
	wf.JobWorker
	f  *runtimeFixture
	rt caseRuntime
}

func (w responseJobWorker) NewTaskResultObserver(ctx context.Context) (context.Context, func(wf.TaskEndEvent)) {
	if p, ok := w.JobWorker.(wf.TaskResultObserverProvider); ok {
		return p.NewTaskResultObserver(ctx)
	}
	return ctx, nil
}
func (w responseJobWorker) Run(c wf.ExecutionContext, data jobdb.JobData) (out jobdb.JobData, err error) {
	info, e := w.rt.GetJob(c.Context(), c.GetJobKey())
	if e != nil {
		return nil, w.f.controlError(e)
	}
	w.f.mu.Lock()
	s := w.f.scenario
	if len(s.report.Replays) >= w.f.c.Runtime.Controls.Scenario.limits().Events {
		w.f.mu.Unlock()
		return nil, w.f.controlError(fmt.Errorf("response replay evidence limit exceeded"))
	}
	s.position++
	index := len(s.report.Replays)
	s.report.Replays = append(s.report.Replays, ResponseReplay{JobID: c.GetJobKey().JobId, Session: s.session, Position: s.position, Route: info.NextRoute})
	w.f.mu.Unlock()
	defer func() {
		w.f.mu.Lock()
		defer w.f.mu.Unlock()
		r := &s.report.Replays[index]
		r.Finished = true
		r.Suspended = wf.IsExecutionSuspended(err)
		if err != nil && !r.Suspended {
			r.Error = err.Error()
		}
	}()
	return w.JobWorker.Run(c, data)
}
func (f *runtimeFixture) hasResponseSubmissions() bool {
	for _, r := range f.c.Runtime.Responses {
		if r.Submission != nil {
			return true
		}
	}
	return false
}
func (f *runtimeFixture) controlledResponseByID(id string) *controlledResponse {
	for _, r := range f.controlledResponses {
		if r.evidence.ID == id {
			return r
		}
	}
	return nil
}
func (f *runtimeFixture) hasControlledResponse(job string) bool {
	if f.scenario == nil {
		return false
	}
	for _, r := range f.c.Runtime.Responses {
		if s := r.Submission; s != nil {
			if j := f.scenario.report.Jobs[s.Pending.Job]; j.Ref.JobID == job {
				return true
			}
		}
	}
	return false
}
func (f *runtimeFixture) hasUnboundResponse(job, node, cell string) bool {
	// The root cannot be a submitted child. Other jobs require creation
	// provenance, not just a different alias: aliases can select the same child.
	if job == f.rootJobID {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.c.Runtime.Responses {
		if s := r.Submission; s != nil && !f.responses[i] && r.NodePath == node && (r.Cell == "" || r.Cell == cell) {
			if f.scenario.report.Jobs[s.Pending.Job].Ref.JobID == "" && !f.distinctResponseJob(job, s.Pending.Job) {
				return true
			}
		}
	}
	return false
}

// distinctResponseJob runs under f.mu and excludes only identities that cannot
// satisfy the unresolved selector. Missing provenance must keep reserving input.
func (f *runtimeFixture) distinctResponseJob(job, name string) bool {
	spec := f.c.Runtime.Controls.Scenario.Jobs[name]
	if spec.ChildBinding != "" {
		for alias, bound := range f.scenario.report.Jobs {
			binding := f.c.Runtime.Controls.Scenario.Jobs[alias].ChildBinding
			if bound.Ref.JobID == job && binding != "" && binding != spec.ChildBinding {
				// Different unique fixture bindings submit different native jobs.
				return true
			}
		}
	}
	caller := ""
	if q := spec.Submission; q != nil {
		caller = f.scenario.report.Jobs[q.Caller].Ref.JobID
		for ancestor := q.Caller; ancestor != ""; {
			if f.scenario.report.Jobs[ancestor].Ref.JobID == job {
				return true // A submission creates a new job, never an ancestor.
			}
			parent := f.c.Runtime.Controls.Scenario.Jobs[ancestor].Submission
			if parent == nil {
				break
			}
			ancestor = parent.Caller // Scenario validation rejects source cycles.
		}
	}
	distinct := false
	for _, r := range f.scenario.report.Receipts {
		if r.Ref.JobID != job {
			continue
		}
		if spec.ChildBinding != "" {
			if !f.distinctFixtureBinding(r.Caller, r.NodePath, spec.ChildBinding) {
				return false
			}
			distinct = true
		}
		if q := spec.Submission; q != nil && caller != "" {
			a := dispatchtest.Attempt{Name: r.TaskType, InvocationID: r.InvocationID, NodePath: r.NodePath, Attempt: r.Attempt}
			if r.Caller == caller && r.Index == q.Index && matchesTask(q.Task, a, r.ScopeID) {
				return false
			}
			distinct = true
		}
	}
	return distinct
}

// A native creation receipt can also be observed for a fixture broker call.
// Exclude a binding only when the actual creating operation could not have run
// its effect. Missing call provenance or a matching effect remains ambiguous.
func (f *runtimeFixture) distinctFixtureBinding(caller, node, binding string) bool {
	matched := false
	for identity, index := range f.callIndexes {
		call := f.report.Calls[index]
		if call.JobID != caller || call.NodePath != node {
			continue
		}
		matched = true
		selected, known := f.selected[identity]
		if !known {
			return false
		}
		if selected < 0 { // The creating operation ran natively.
			continue
		}
		if effects := f.c.Mocks.Ops[selected].Behavior.Effects; effects != nil {
			for _, child := range effects.Children {
				if child.Bind == binding {
					return false
				}
			}
		}
	}
	return matched
}

func (f *runtimeFixture) reservesPendingResponse(tenant, job, requestID string) bool {
	for _, r := range f.controlledResponses {
		p := r.evidence.Pending
		if p.TenantID == tenant && p.JobID == job && p.RequestID == requestID {
			return true
		}
	}
	return false
}
func formDigest(form inputop.InputForm) string {
	raw, _ := json.Marshal(form)
	return contentDigest(raw)
}
func responseInputContext(raw []byte) (node, scope string, err error) {
	var e struct {
		Context struct {
			NodePath  string `json:"node_path"`
			Workspace *struct {
				ScopeID string `json:"scope_id"`
			} `json:"workspace"`
		} `json:"context"`
	}
	if err = json.Unmarshal(raw, &e); err != nil {
		return
	}
	node = e.Context.NodePath
	if e.Context.Workspace != nil {
		scope = e.Context.Workspace.ScopeID
	}
	return
}

// Select waits from native history, including unresolved tasks which have no
// result yet. Completed task projections alone cannot identify pending input.
func (f *runtimeFixture) captureResponse(ctx context.Context, rt caseRuntime, tenant string, index int, input *inputop.Runtime) (*controlledResponse, error) {
	fixture := f.c.Runtime.Responses[index]
	spec := fixture.Submission
	j, ok := f.scenario.report.Jobs[spec.Pending.Job]
	if !ok {
		return nil, nil
	}
	key := j.Ref.Key()
	info, err := rt.GetJob(ctx, key)
	if err != nil {
		return nil, err
	}
	wait := info.ExecutionState.TaskWait
	if wait == nil || info.NextRoute == nil || info.NextRoute.TaskType != "input:collect_user_input" {
		return nil, nil
	}
	run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true, IncludeAttemptInputs: true})
	if err != nil {
		return nil, err
	}
	q := spec.Pending.Task
	type candidate struct {
		id    string
		wait  jobdb.ExecutionHistoryEntry
		scope string
	}
	var matches []candidate
	for _, entry := range run.Execution {
		if entry.Record.Kind != "task" {
			continue
		}
		var desc struct {
			Call struct {
				TaskType string `json:"task_type"`
				Command  struct {
					Node string `json:"node"`
				} `json:"command"`
			} `json:"call"`
		}
		if err = json.Unmarshal(entry.Record.Descriptor, &desc); err != nil {
			return nil, err
		}
		if desc.Call.TaskType != q.TaskType || q.NodePath != "" && q.NodePath != desc.Call.Command.Node || q.InvocationID != "" && q.InvocationID != entry.Record.ID {
			continue
		}
		for _, w := range run.Execution {
			if w.Record.Kind != "wait" || w.Record.ID != fmt.Sprintf("%s/attempt:%d", entry.Record.ID, q.Attempt) || w.Record.Input == nil {
				continue
			}
			node, scope, e := responseInputContext(w.Record.Input.Data)
			if e != nil {
				return nil, e
			}
			if scope == "" {
				for _, parent := range run.Execution {
					if parent.Record.Kind == "scope" && strings.HasPrefix(entry.Record.ID, parent.Record.ID+"/") && len(parent.Record.ID) > len(scope) {
						scope = parent.Record.ID
					}
				}
			}
			if q.ScopeID != "" && q.ScopeID != scope {
				continue
			}
			if node != desc.Call.Command.Node || node != fixture.NodePath {
				return nil, fmt.Errorf("pending native node identity mismatch")
			}
			matches = append(matches, candidate{entry.Record.ID, w, scope})
		}
	}
	if len(matches) < q.Occurrence {
		return nil, nil
	}
	selected := matches[q.Occurrence-1]
	if selected.wait.Ordinal != wait.InputOrdinal {
		return nil, fmt.Errorf("selected pending occurrence is no longer active")
	}
	handle, err := f.ctl.GetWaitingTask(ctx, key)
	if err != nil {
		return nil, err
	}
	if handle == nil || handle.TaskType() != q.TaskType || handle.TaskOrdinalToComplete() != wait.OutputOrdinal {
		return nil, fmt.Errorf("pending handle disagrees with selected native wait")
	}
	if fixture.Cell != "" && fixture.Cell != info.Cell {
		return nil, fmt.Errorf("pending response cell mismatch")
	}
	form, err := input.GetForm(ctx, tenant, key.JobId)
	if err != nil {
		return nil, err
	}
	if form.RequestID == "" {
		return nil, fmt.Errorf("controlled input requires native request identity")
	}
	raw := append([]byte(nil), selected.wait.Record.Input.Data...)
	db, err := responseDB(f.root)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var payload []byte
	if err = db.QueryRowContext(ctx, "SELECT payload FROM jobdb_jobs WHERE tenant_id=? AND job_id=?", tenant, key.JobId).Scan(&payload); err != nil {
		return nil, err
	}
	actor := inputop.Actor{ID: "recipe-test", Kind: "test"}
	if fixture.Actor != nil {
		actor = *fixture.Actor
	}
	a := SubmissionAttempt{Version: 1, ID: spec.ID, ResponseIndex: index, SubmissionID: fmt.Sprint(index), Actor: actor, ExpectedError: spec.ExpectError, FaultStage: spec.Fault, Checkpoints: []ResponseCheckpoint{}, Errors: []string{}, Durable: DurableResponseReceipt{Status: "incomplete", OutputOrdinal: wait.OutputOrdinal}, Pending: PendingResponseIdentity{TenantID: tenant, JobID: key.JobId, Job: spec.Pending.Job, Task: TaskSelection{TaskType: q.TaskType, NodePath: fixture.NodePath, InvocationID: selected.id, ScopeID: selected.scope, Attempt: q.Attempt, Occurrence: q.Occurrence}, WaitOrdinal: wait.InputOrdinal, OutputOrdinal: wait.OutputOrdinal, RequestID: form.RequestID, Form: form, FormSHA256: formDigest(form), InputSHA256: contentDigest(raw), SchedulerSHA256: contentDigest(payload)}}
	if err = f.requireExactResponseEvidence(a); err != nil {
		return nil, err
	}
	// Pointers remain stable: allocate independently, then copy into the selected
	// report at each controller boundary rather than retaining slice addresses.
	r := &controlledResponse{evidence: &a, form: form, input: raw, dispatchBaseline: f.responseDispatches(key.JobId)}
	return r, nil
}
func (f *runtimeFixture) requireExactResponseEvidence(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	for _, v := range f.c.Runtime.Secrets {
		if err = redactor.Register(v); err != nil {
			return err
		}
	}
	session := redactor.NewSession()
	defer session.Close()
	var safe any
	if err := snapshotRuntimeValue(secrets.WithSession(context.Background(), session), value, &safe); err != nil {
		return err
	}
	safeRaw, err := json.Marshal(safe)
	if err != nil {
		return err
	}
	var canonical any
	if err = json.Unmarshal(raw, &canonical); err != nil {
		return err
	}
	canonicalRaw, err := json.Marshal(canonical)
	if err != nil {
		return err
	}
	if !bytes.Equal(safeRaw, canonicalRaw) {
		return fmt.Errorf("exact response evidence requires secret redaction")
	}
	limit := 256 * 1024
	if f.c.Runtime.Controls.MaxEvidenceBytes > 0 {
		limit = f.c.Runtime.Controls.MaxEvidenceBytes
	}
	if len(raw) > limit {
		return fmt.Errorf("response evidence exceeds byte limit")
	}
	return nil
}
func (f *runtimeFixture) saveSubmission(r *controlledResponse) error {
	a := r.evidence
	evidenceErr := f.requireExactResponseEvidence(a)
	if evidenceErr != nil {
		a.Complete = false
		a.Errors = append(a.Errors, evidenceErr.Error())
	}
	// Deep copy: checkpoints are snapshots, never reconstructed from final state.
	var copy SubmissionAttempt
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	for _, v := range f.c.Runtime.Secrets {
		if err := redactor.Register(v); err != nil {
			return err
		}
	}
	session := redactor.NewSession()
	defer session.Close()
	if err := snapshotRuntimeValue(secrets.WithSession(context.Background(), session), a, &copy); err != nil {
		return err
	}
	if evidenceErr != nil {
		copy.Pending.Form = inputop.InputForm{}
		copy.Before = nil
		copy.After = nil
		copy.Checkpoints = nil
		copy.Omitted = []string{"form and state snapshots: " + evidenceErr.Error()}
	}
	f.mu.Lock()
	found := false
	for i := range f.report.Submissions {
		if f.report.Submissions[i].ID == a.ID {
			f.report.Submissions[i] = copy
			found = true
			break
		}
	}
	if !found {
		f.report.Submissions = append(f.report.Submissions, copy)
	}
	f.mu.Unlock()
	return errors.Join(evidenceErr, f.checkScenarioSize())
}
func (f *runtimeFixture) responseDispatches(job string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, d := range f.report.Controls.Dispatch {
		if d.JobID == job && d.Kind == "task" && d.Entered {
			n++
		}
	}
	return n
}
func readResponseReceipt(ctx context.Context, rt caseRuntime, db *sql.DB, p PendingResponseIdentity, submissionID string) DurableResponseReceipt {
	result := DurableResponseReceipt{Status: "unreadable", OutputOrdinal: p.OutputOrdinal}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM jobdb_chapter_chapters WHERE anthology_id=? AND story_id=? AND ordinal=?", p.TenantID, p.JobID, p.OutputOrdinal).Scan(&n); err != nil {
		result.Error = err.Error()
		return result
	}
	if n == 0 {
		result.Status = "absent"
		return result
	}
	// Recovery may append an activation at the original empty output slot.
	// Resolve only the selected result ID, then read that exact native chapter;
	// never recursively search request data for a receipt.
	run, e := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: p.TenantID, JobId: p.JobID}, IncludeOutputs: true})
	if e != nil {
		result.Error = e.Error()
		return result
	}
	found := false
	for _, entry := range run.Execution {
		if entry.Record.Kind == "result" && entry.Record.ID == fmt.Sprintf("%s/attempt:%d", p.Task.InvocationID, p.Task.Attempt) {
			if found {
				result.Status = "incomplete"
				result.Error = "duplicate selected response output"
				return result
			}
			found = true
			result.OutputOrdinal = entry.Ordinal
		}
	}
	if !found {
		result.Status = "absent"
		return result
	}
	ch, err := rt.GetChapter(ctx, jobdb.ChapterRef{JobKey: jobdb.JobKey{TenantId: p.TenantID, JobId: p.JobID}, Ordinal: result.OutputOrdinal})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	return decodeSelectedResponseReceipt(ch, p, submissionID, result.OutputOrdinal)
}
func decodeSelectedResponseReceipt(ch jobdb.Chapter, p PendingResponseIdentity, submissionID string, ordinal int64) DurableResponseReceipt {
	result := DurableResponseReceipt{Status: "incomplete", OutputOrdinal: ordinal}
	result.Status = "incomplete"
	body, ok := ch.Body.(jobdb.ExecutionChapter)
	if !ok {
		result.Error = "selected output is not an execution chapter"
		return result
	}
	record, err := jobdb.DecodeExecutionRecord(body.Record)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if record.Kind != "result" || record.ID != fmt.Sprintf("%s/attempt:%d", p.Task.InvocationID, p.Task.Attempt) || record.Output == nil {
		result.Error = "selected output does not match input occurrence"
		return result
	}
	var env coretask.OutputEnvelope
	if err = json.Unmarshal(record.Output.Data, &env); err != nil {
		result.Error = err.Error()
		return result
	}
	if env.Kind != coretask.OutputKindActivityInvocationOutput || (env.Version != 1 && env.Version != coretask.OutputEnvelopeVersion) {
		result.Error = "unsupported response output envelope"
		return result
	}
	var out workerops.ActivityInvocationOutput
	if err = env.DecodePayload(&out); err != nil {
		result.Error = err.Error()
		return result
	}
	raw, err := json.Marshal(out.OpOutput)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	var answer inputop.Output
	if err = json.Unmarshal(raw, &answer); err != nil {
		result.Error = err.Error()
		return result
	}
	if answer.Receipt == nil || answer.Receipt.RequestID != p.RequestID || answer.Receipt.SubmissionID != submissionID {
		result.Error = "durable receipt request/submission identity mismatch"
		return result
	}
	result.Status = "present"
	result.Receipt = answer.Receipt
	return result
}
func (f *runtimeFixture) responseSnapshot(ctx context.Context, rt caseRuntime, input *inputop.Runtime, r *controlledResponse) (snap ResponseStateSnapshot, err error) {
	p := r.evidence.Pending
	snap.Durable = DurableResponseReceipt{Status: "incomplete", OutputOrdinal: p.OutputOrdinal, Error: "state inspection did not reach durable output read"}
	key := jobdb.JobKey{TenantId: p.TenantID, JobId: p.JobID}
	snap.Session = f.scenario.session
	snap.Position = f.scenario.position
	snap.Dispatches = f.responseDispatches(p.JobID) - r.dispatchBaseline
	info, err := rt.GetJob(ctx, key)
	if err != nil {
		return snap, err
	}
	snap.Status = string(info.Status)
	snap.Route = info.NextRoute
	snap.Waiting = info.ExecutionState.TaskWait
	handle, e := f.ctl.GetWaitingTask(ctx, key)
	if e != nil && !errors.Is(e, jobdb.ErrJobNotFound) && !errors.Is(e, inputop.ErrInputNotPending) {
		return snap, e
	}
	if e == nil && handle != nil {
		snap.HandlePresent = true
		snap.HandleOrdinal = handle.TaskOrdinalToComplete()
		if handle.TaskType() != p.Task.TaskType {
			return snap, fmt.Errorf("waiting handle task identity changed")
		}
	}
	pending, e := input.ListPendingInputs(ctx, p.TenantID)
	if e != nil {
		return snap, e
	}
	for _, v := range pending {
		if v.Id == p.JobID {
			snap.Actionable = true
		}
	}
	if snap.HandlePresent {
		form, e := input.GetForm(ctx, p.TenantID, p.JobID)
		if e != nil {
			return snap, e
		}
		snap.FormSHA256 = formDigest(form)
		if snap.FormSHA256 != p.FormSHA256 {
			return snap, fmt.Errorf("frozen pending form changed")
		}
	}
	ch, e := rt.GetChapter(ctx, jobdb.ChapterRef{JobKey: key, Ordinal: p.WaitOrdinal})
	if e != nil {
		return snap, e
	}
	body, ok := ch.Body.(jobdb.ExecutionChapter)
	if !ok {
		return snap, fmt.Errorf("frozen wait chapter changed")
	}
	record, e := jobdb.DecodeExecutionRecord(body.Record)
	if e != nil {
		return snap, e
	}
	snap.InputUnchanged = record.Kind == "wait" && record.ID == fmt.Sprintf("%s/attempt:%d", p.Task.InvocationID, p.Task.Attempt) && record.Input != nil && bytes.Equal(record.Input.Data, r.input)
	if !snap.InputUnchanged {
		return snap, fmt.Errorf("original/recovery input snapshot changed")
	}
	db, e := responseDB(f.root)
	if e != nil {
		return snap, e
	}
	defer db.Close()
	var lease, worker sql.NullString
	var expiry sql.NullInt64
	if e = db.QueryRowContext(ctx, "SELECT lease_id,lease_worker_id,lease_expires_at_ns FROM jobdb_jobs WHERE tenant_id=? AND job_id=?", p.TenantID, p.JobID).Scan(&lease, &worker, &expiry); e != nil {
		return snap, e
	}
	if lease.Valid {
		if !worker.Valid || !expiry.Valid {
			return snap, fmt.Errorf("incomplete recovery lease")
		}
		snap.Lease = &ResponseRecoveryLease{ID: lease.String, WorkerID: worker.String, ExpiresAtNS: expiry.Int64, Expired: expiry.Int64 <= time.Now().UnixNano()}
	}
	snap.Durable = readResponseReceipt(ctx, rt, db, p, r.evidence.SubmissionID)
	if snap.Durable.Status == "unreadable" || snap.Durable.Status == "incomplete" {
		return snap, fmt.Errorf("durable response evidence %s: %s", snap.Durable.Status, snap.Durable.Error)
	}
	return snap, nil
}
