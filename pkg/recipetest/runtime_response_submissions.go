package recipetest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	inputop "github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func (f *runtimeFixture) pumpResponseSubmissions(ctx context.Context, rt caseRuntime, tenant string, input *inputop.Runtime) error {
	if !f.hasResponseSubmissions() {
		return nil
	}
	if f.controlledResponses == nil {
		f.controlledResponses = map[int]*controlledResponse{}
	}
	for i, fixture := range f.c.Runtime.Responses {
		spec := fixture.Submission
		if spec == nil || f.responses[i] {
			continue
		}
		r := f.controlledResponses[i]
		if r == nil {
			if err := f.captureResponseSubmission(ctx, rt, tenant, i, input); err != nil {
				return err
			}
			continue
		}
		ready := true
		for _, p := range spec.AfterAll {
			ready = ready && (f.scenario.facts[p] || f.scenario.actions[p])
		}
		if !ready {
			continue
		}
		f.mu.Lock()
		eligible := fixture.AfterCall == nil || f.gateCounts[i] >= fixture.AfterCall.Occurrence
		f.mu.Unlock()
		if !eligible {
			continue
		}
		gate, err := f.gateInput(ctx, fixture, r.evidence.Pending.JobID, r.form)
		if err != nil {
			return err
		}
		if !gate {
			continue
		}
		if !f.workersJoined() {
			return fmt.Errorf("controlled response %s requires stopped and joined workers", spec.ID)
		}
		if err = f.submitControlledResponse(ctx, rt, input, fixture, r); err != nil {
			return err
		}
	}
	return nil
}

func (f *runtimeFixture) captureResponseSubmission(ctx context.Context, rt caseRuntime, tenant string, index int, input *inputop.Runtime) error {
	spec := f.c.Runtime.Responses[index].Submission
	if spec == nil || f.responses[index] || f.controlledResponses[index] != nil {
		return nil
	}
	// A later attempt to the same occurrence is captured after the preceding
	// declared attempt and recovery gate; it must not freeze stale output slots.
	for k := 0; k < index; k++ {
		s := f.c.Runtime.Responses[k].Submission
		if s != nil && s.Pending == spec.Pending && !f.responses[k] {
			return nil
		}
	}
	r, err := f.captureResponse(ctx, rt, tenant, index, input)
	if err != nil || r == nil {
		return err
	}
	if f.controlledResponses == nil {
		f.controlledResponses = map[int]*controlledResponse{}
	}
	f.controlledResponses[index] = r
	return f.saveSubmission(r)
}

func (f *runtimeFixture) submitControlledResponse(ctx context.Context, rt caseRuntime, input *inputop.Runtime, fixture InputFixture, r *controlledResponse) (err error) {
	a := r.evidence
	defer func() {
		if err != nil {
			a.Complete = false
			a.Errors = append(a.Errors, err.Error())
		}
		if e := f.saveSubmission(r); e != nil {
			err = errors.Join(err, e)
		}
	}()
	before, e := f.responseSnapshot(ctx, rt, input, r)
	a.Before = &before
	if e != nil {
		return e
	}
	if before.Waiting != nil && (before.Waiting.InputOrdinal != a.Pending.WaitOrdinal || before.Waiting.OutputOrdinal != a.Pending.OutputOrdinal) {
		return fmt.Errorf("captured waiting identity changed before submission")
	}
	if before.Status != "CANCELLED" && (!before.Actionable || before.Waiting == nil || before.Waiting.InputOrdinal != a.Pending.WaitOrdinal || before.Waiting.OutputOrdinal != a.Pending.OutputOrdinal) {
		return fmt.Errorf("captured pending target changed before submission")
	}
	fields := cloneStringMap(fixture.Fields)
	if fields == nil {
		fields = map[string]any{}
	}
	for field, source := range fixture.Attachments {
		b, e := os.ReadFile(filepath.Join(f.opts.FixtureRoot, source))
		if e != nil {
			return e
		}
		fields[field] = jobdb.NewArtifactFromBytes(filepath.Base(source), b)
	}
	var fault *responseFault
	if a.FaultStage != "" {
		fault, e = f.armResponseFault(ctx, a)
		if e != nil {
			return e
		}
	}
	// Register cleanup before entering the unchanged native submission API.
	if fault != nil {
		defer func() {
			if !fault.evidence.Removed {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer stop()
				err = errors.Join(err, fault.remove(cleanup))
			}
		}()
	}
	a.Attempted = true
	f.responses[a.ResponseIndex] = true
	f.mu.Lock()
	index := a.ResponseIndex
	f.event(OrderingEvent{Kind: "response_release", JobID: a.Pending.JobID, NodePath: fixture.NodePath, Cell: fixture.Cell, ResponseIndex: &index})
	f.mu.Unlock()
	var accepted inputop.Output
	var apiErr error
	if r.form.ResponseSchema != nil {
		accepted, apiErr = input.SubmitStructuredResponse(ctx, a.Pending.TenantID, a.Pending.JobID, inputop.StructuredSubmission{RequestID: a.Pending.RequestID, SubmissionID: a.SubmissionID, Response: fixture.Response}, a.Actor)
	} else {
		accepted, apiErr = input.SubmitFormResponse(ctx, a.Pending.TenantID, a.Pending.JobID, inputop.FormSubmission{RequestID: a.Pending.RequestID, SubmissionID: a.SubmissionID, Fields: fields, Response: fixture.Response}, a.Actor)
	}
	a.ReturnedReceipt = accepted.Receipt
	a.ErrorKind = responseErrorKind(apiErr)
	if apiErr != nil {
		a.APIError = apiErr.Error()
	}
	a.Matched = matchesResponseError(a.ExpectedError, apiErr)
	// Remove before any new worker can run, including a mismatched expectation.
	var cleanupErr error
	if fault != nil {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		cleanupErr = fault.remove(cleanup)
		stop()
	}
	after, readErr := f.responseSnapshot(ctx, rt, input, r)
	a.After = &after
	a.Durable = after.Durable
	if cleanupErr != nil || readErr != nil {
		return errors.Join(cleanupErr, readErr)
	}
	if fault != nil && a.Fault.Hits != 1 {
		return fmt.Errorf("response fault boundary hit %d times, require exactly once", a.Fault.Hits)
	}
	if !a.Matched {
		return fmt.Errorf("response %s submission expectation mismatch: kind=%s error=%q", a.ID, a.ErrorKind, a.APIError)
	}
	if fault != nil {
		if a.ReturnedReceipt != nil || after.Dispatches != 0 {
			return fmt.Errorf("faulted response unexpectedly returned acceptance or dispatched continuation")
		}
		switch a.FaultStage {
		case "claim":
			if !after.Actionable || after.Lease != nil || after.Durable.Status != "absent" || !reflect.DeepEqual(before.Waiting, after.Waiting) || after.FormSHA256 != a.Pending.FormSHA256 {
				return fmt.Errorf("claim rejection changed pending state")
			}
		case "response_output":
			if after.Lease == nil || after.Actionable || after.Durable.Status != "absent" || after.Route == nil || after.Route.TaskType != "" {
				return fmt.Errorf("output rejection lacks native recovery ownership without acceptance")
			}
		case "settlement":
			if after.Lease == nil || after.Lease.ID != a.Fault.ClaimedLease || after.Actionable || after.Durable.Status != "present" || after.Route == nil || after.Route.TaskType != "" {
				return fmt.Errorf("settlement rejection lacks claimed lease and durable answer")
			}
		}
	}

	if apiErr == nil {
		if a.ReturnedReceipt == nil || a.Durable.Status != "present" || !reflect.DeepEqual(a.ReturnedReceipt, a.Durable.Receipt) {
			return fmt.Errorf("returned and durable response receipts disagree")
		}
		f.mu.Lock()
		f.report.Reviews = append(f.report.Reviews, ReviewCall{NodePath: fixture.NodePath, Form: r.form, JobID: a.Pending.JobID, RequestID: a.Pending.RequestID, Receipt: accepted.Receipt})
		f.mu.Unlock()
	}
	a.Complete = true
	return nil
}
func (f *runtimeFixture) responseFact(ctx context.Context, rt caseRuntime, tenant string, e ScenarioEvent) (bool, ScenarioFact, error) {
	fact := ScenarioFact{ID: e.ID}
	r := f.controlledResponseByID(e.Response)
	if r == nil {
		return false, fact, nil
	}
	a := r.evidence
	p := a.Pending
	fact.JobID = p.JobID
	fact.InvocationID = p.Task.InvocationID
	fact.Attempt = p.Task.Attempt
	fact.ResultOrdinal = p.WaitOrdinal
	if e.Phase == "pending" {
		return true, fact, nil
	}
	if !a.Attempted || !a.Complete {
		return false, fact, nil
	}
	if e.Phase == "attempted" {
		return true, fact, nil
	}
	// Checkpoints are frozen once. Multiple visits never replace their evidence.
	for _, c := range a.Checkpoints {
		if c.Event == e.ID {
			return true, fact, nil
		}
	}
	mode := "worker_replay"
	var replay *ResponseReplay
	if a.FaultStage == "claim" {
		mode = "not_required"
	} else {
		f.mu.Lock()
		for _, v := range f.scenario.report.Replays {
			if v.JobID == p.JobID && v.Session > a.After.Session && v.Finished && v.Route != nil && v.Route.TaskType == "" && ((e.Status == "pending" && v.Suspended) || (e.Status == "completed" && !v.Suspended && v.Error == "")) {
				copy := v
				replay = &copy
			}
		}
		f.mu.Unlock()
		if replay == nil {
			return false, fact, nil
		}
	}
	input, err := inputop.NewRuntime(f.ctl, nil)
	if err != nil {
		return false, fact, err
	}
	state, err := f.responseSnapshot(ctx, rt, input, r)
	if err != nil {
		return false, fact, err
	}
	if e.Status == "pending" {
		if !state.Actionable || !state.HandlePresent || state.Waiting == nil {
			return false, fact, nil
		}
		if state.Waiting.InputOrdinal != p.WaitOrdinal || state.FormSHA256 != p.FormSHA256 || state.Durable.Status != "absent" || a.ReturnedReceipt != nil || state.Lease != nil || state.Dispatches != 0 {
			return false, fact, fmt.Errorf("recovered pending response lacks unchanged identity, absent receipt/lease or zero dispatches")
		}
		if a.Fault == nil || !a.Fault.Removed || a.Fault.Hits != 1 {
			return false, fact, fmt.Errorf("pending checkpoint requires a proven removed fault")
		}
		if a.FaultStage == "claim" {
			if !f.workersJoined() || !reflect.DeepEqual(a.Before.Waiting, a.After.Waiting) || !a.After.Actionable || a.After.Lease != nil || a.After.Durable.Status != "absent" || a.After.FormSHA256 != p.FormSHA256 || a.After.Dispatches != 0 {
				return false, fact, fmt.Errorf("claim checkpoint lacks unchanged pending evidence with workers joined")
			}
		} else if a.FaultStage != "response_output" {
			return false, fact, fmt.Errorf("unsupported pending recovery fault")
		}
	} else {
		if state.Status != "COMPLETED" {
			return false, fact, nil
		}
		if state.Durable.Status != "present" || !reflect.DeepEqual(state.Durable.Receipt, a.Durable.Receipt) {
			return false, fact, fmt.Errorf("durable receipt changed across recovery")
		}
	}
	fact.Status = e.Status
	// Assign the checkpoint the same next event position as its published fact.
	state.Position = f.scenario.position + 1
	a.Checkpoints = append(a.Checkpoints, ResponseCheckpoint{Event: e.ID, Status: e.Status, RecoveryMode: mode, Replay: replay, State: state})
	if err = f.saveSubmission(r); err != nil {
		return false, fact, err
	}
	return true, fact, nil
}
