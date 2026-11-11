package recipetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/colony-2/c2j/internal/preparedtest"
	inputop "github.com/colony-2/c2j/pkg/input"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// RuntimeControls are opt-in, case-local controls. Completion is scripted only
// after native extension preparation; these are not whole-operation mocks.
type RuntimeControls struct {
	Snapshot         *EffectSnapshotConfig `json:"snapshot,omitempty"`
	Dispatch         *DispatchPolicy       `json:"dispatch,omitempty"`
	MaxEvidenceBytes int                   `json:"max_evidence_bytes,omitempty"`
	Barriers         []PreparedBarrier     `json:"barriers"`
	Actions          []ControlAction       `json:"actions,omitempty"`
}

type PreparedBarrier struct {
	CapturePrepared bool                     `json:"capture_prepared,omitempty"`
	ProbeTools      []string                 `json:"probe_tools,omitempty"`
	Objects         map[string]ObjectFixture `json:"objects,omitempty"`
	CaptureEnv      []string                 `json:"capture_env,omitempty"`
	ID              string                   `json:"id"`
	Match           PreparedMatch            `json:"match"`
	Stdout          string                   `json:"stdout,omitempty"`
	Error           string                   `json:"error,omitempty"`
}

type PreparedMatch struct {
	Selector   string `json:"selector"`
	NodePath   string `json:"node_path"`
	Cell       string `json:"cell"`
	Occurrence int    `json:"occurrence"`
	Attempt    int    `json:"attempt"`
}

// On names a barrier's entered event. A barrier without an action deliberately
// remains blocked until cancellation or the case deadline, and cannot pass.
type ControlAction struct {
	On string `json:"on"`
	Do string `json:"do"` // release or cancel_job
}

type ControlJobState struct {
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	Completion  string `json:"completion"`
	FailureKind string `json:"failure_kind"`
	HasOutput   bool   `json:"has_output"`
}

type ControlReport struct {
	Baselines        map[string]*EffectSnapshot `json:"baselines,omitempty"`
	FinalSnapshot    *EffectSnapshot            `json:"final_snapshot,omitempty"`
	Preserved        bool                       `json:"preserved"`
	Dispatch         []DispatchAttempt          `json:"dispatch"`
	DispatchDropped  int                        `json:"dispatch_dropped"`
	DispatchComplete bool                       `json:"dispatch_complete"`
	Final            *ControlJobState           `json:"final,omitempty"`
	Version          int                        `json:"version"`
	Events           []ControlEvent             `json:"events"`
	Complete         bool                       `json:"complete"`
	Errors           []string                   `json:"errors"`
}

type ControlEvent struct {
	Prepared           *PreparedEvidence  `json:"prepared,omitempty"`
	ResolvedSelector   string             `json:"resolved_selector,omitempty"`
	RequestID          string             `json:"request_id,omitempty"`
	Form               *inputop.InputForm `json:"form,omitempty"`
	Calls              []OpCall           `json:"calls,omitempty"`
	NativeCancellation bool               `json:"native_cancellation,omitempty"`
	Kind               string             `json:"kind"`
	Barrier            string             `json:"barrier,omitempty"`
	JobID              string             `json:"job_id,omitempty"`
	InvocationID       string             `json:"invocation_id,omitempty"`
	Attempt            int                `json:"attempt,omitempty"`
	ResultOrdinal      int64              `json:"result_ordinal,omitempty"`
	NodePath           string             `json:"node_path,omitempty"`
	Selector           string             `json:"selector,omitempty"`
	Cell               string             `json:"cell,omitempty"`
	Inputs             map[string]any     `json:"inputs,omitempty"`
	Env                map[string]string  `json:"env,omitempty"`
	Cause              string             `json:"cause,omitempty"`
}

type barrierState struct {
	spec    PreparedBarrier
	entry   *ControlEvent
	seen    map[string]int
	release chan struct{}
	exited  chan struct{}
	action  bool
}

func validateControls(c *RuntimeCase) error {
	if c.Controls == nil {
		return nil
	}
	if p := c.Controls.Snapshot; p != nil {
		if p.MaxBytes < 0 || p.MaxBytes > 16*1024*1024 || p.MaxArtifactBytes < 0 || p.MaxArtifactBytes > 4*1024*1024 {
			return fmt.Errorf("snapshot limits exceed supported bounds")
		}
		for _, name := range p.Files {
			if _, err := fixturePath("", name); err != nil {
				return err
			}
		}
	}
	if p := c.Controls.Dispatch; p != nil {
		if p.MaxAttempts < 0 || p.MaxAttempts > 4096 {
			return fmt.Errorf("dispatch max_attempts must not exceed 4096")
		}
		for _, m := range p.Allow {
			if (m.Kind != "task" && m.Kind != "native") || m.Name == "" || strings.ContainsAny(m.Name+m.NodePath+m.Cell, "*?[]") {
				return fmt.Errorf("dispatch allow requires exact task/native name")
			}
		}
	}
	if c.Controls.MaxEvidenceBytes < 0 || c.Controls.MaxEvidenceBytes > 256*1024 {
		return fmt.Errorf("max_evidence_bytes must be between 1 and 262144 when specified")
	}
	if c.Backend != "sqlite" {
		return fmt.Errorf("runtime controls require backend: sqlite")
	}
	if len(c.Controls.Barriers) == 0 {
		return fmt.Errorf("controls require at least one barrier")
	}
	ids := map[string]bool{}
	matches := map[PreparedMatch]bool{}
	for _, b := range c.Controls.Barriers {
		if len(b.ProbeTools) > 0 && !b.CapturePrepared {
			return fmt.Errorf("probe_tools requires capture_prepared")
		}
		for _, tool := range b.ProbeTools {
			if tool == "" || strings.ContainsAny(tool, "/\\*?[]") || tool == ".." {
				return fmt.Errorf("probe_tools requires exact executable names")
			}
		}
		for name, object := range b.Objects {
			if object.Type == "" {
				return fmt.Errorf("completion object requires type")
			}
			if _, err := fixturePath("", name); err != nil {
				return err
			}
			for part := range object.Files {
				if _, err := fixturePath("", part); err != nil {
					return err
				}
			}
		}

		if b.ID == "" || strings.Contains(b.ID, ".") || ids[b.ID] {
			return fmt.Errorf("barrier id must be nonempty, unique and contain no dots: %q", b.ID)
		}
		ids[b.ID] = true
		for _, name := range b.CaptureEnv {
			if name == "" || strings.HasPrefix(name, "C2J_") {
				return fmt.Errorf("barrier capture_env cannot select protected C2J environment: %q", name)
			}
		}
		m := b.Match
		if m.Selector == "" || m.NodePath == "" || m.Cell == "" || strings.ContainsAny(m.NodePath, "*?[]") || strings.ContainsAny(m.Cell, "*?[]") || m.Occurrence < 1 || m.Attempt < 1 {
			return fmt.Errorf("barrier %s requires exact selector/node/cell and positive occurrence/attempt", b.ID)
		}
		if matches[m] {
			return fmt.Errorf("ambiguous prepared barrier match for %s", b.ID)
		}
		matches[m] = true
		if b.Stdout != "" && b.Error != "" {
			return fmt.Errorf("barrier %s must script stdout or error, not both", b.ID)
		}
	}
	actions := map[string]bool{}
	for _, a := range c.Controls.Actions {
		id, ok := strings.CutSuffix(a.On, ".entered")
		if !ok || !ids[id] {
			return fmt.Errorf("unknown control event %q", a.On)
		}
		if a.Do == "cancel_job" && c.ExpectError != "" {
			return fmt.Errorf("cancel_job controls verify native cancellation directly; omit expect_error")
		}
		if a.Do != "release" && a.Do != "cancel_job" {
			return fmt.Errorf("unknown control action %q", a.Do)
		}
		if actions[id] {
			return fmt.Errorf("conflicting actions for barrier %s", id)
		}
		actions[id] = true
	}
	return nil
}

func (f *runtimeFixture) initControls() {
	if f.c.Runtime.Controls == nil {
		return
	}
	f.report.Controls = &ControlReport{Version: 1, Events: []ControlEvent{}, Errors: []string{}, Baselines: map[string]*EffectSnapshot{}}
	for _, b := range f.c.Runtime.Controls.Barriers {
		f.barriers = append(f.barriers, &barrierState{spec: b, seen: map[string]int{}, release: make(chan struct{}), exited: make(chan struct{})})
	}
}

func (f *runtimeFixture) controlError(err error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.report.Controls.Errors = append(f.report.Controls.Errors, err.Error())
	return err
}

// controlledContext selects using native invocation and attempt identities, not
// report call indexes. No matching control permits an external process launch.
func (f *runtimeFixture) controlledContext(deps coreops.OpDependencies, ctx context.Context, op string, in map[string]any) (context.Context, bool, error) {
	if f.c.Runtime.Controls == nil || op != "extension_execution" {
		return ctx, false, nil
	}
	g := deps.GitContext()
	cell := g.CellName
	if g.Workspace != nil {
		cell = g.Workspace.Cell
	}
	selector, _ := in["selector"].(string)
	jt, ok := deps.JobTool().(*coreops.TaskBasedJobTool)
	if !ok {
		return ctx, false, f.controlError(fmt.Errorf("controlled operation lacks native task identity"))
	}
	var selected *barrierState
	routeCount, maxOccurrence := 0, 0
	f.mu.Lock()
	for _, b := range f.barriers {
		m := b.spec.Match
		if m.NodePath != g.NodePath || m.Cell != cell || m.Selector != selector {
			continue
		}
		if m.Occurrence > maxOccurrence {
			maxOccurrence = m.Occurrence
		}
		occurrence, seen := b.seen[jt.InvocationID()]
		if !seen {
			occurrence = len(b.seen) + 1
			b.seen[jt.InvocationID()] = occurrence
		}
		if occurrence > routeCount {
			routeCount = occurrence
		}
		if occurrence != m.Occurrence {
			continue
		}
		if jt.TaskContext.AttemptNumber != m.Attempt || b.entry != nil {
			f.mu.Unlock()
			return ctx, false, f.controlError(fmt.Errorf("barrier %s unexpected retry or duplicate invocation", b.spec.ID))
		}
		if selected != nil {
			f.mu.Unlock()
			return ctx, false, f.controlError(fmt.Errorf("ambiguous prepared controls"))
		}
		selected = b
	}
	f.mu.Unlock()
	if selected == nil {
		if routeCount > maxOccurrence {
			return ctx, false, f.controlError(fmt.Errorf("unexpected additional controlled invocation occurrence %d", routeCount))
		}
		return ctx, false, nil
	}
	f.mu.Lock()
	if f.controlWorktrees == nil {
		f.controlWorktrees = map[string]string{}
	}
	f.controlWorktrees[jt.InvocationID()] = deps.WorktreePath()
	f.mu.Unlock()
	b := selected
	hook := func(local context.Context, prepared preparedtest.Invocation) ([]byte, bool, error) {
		env := map[string]string{}
		for _, name := range b.spec.CaptureEnv {
			value, exists := prepared.Env[name]
			if !exists {
				return nil, true, f.controlError(fmt.Errorf("required prepared environment value %q is unavailable", name))
			}
			env[name] = value
		}
		event := ControlEvent{Kind: "entered", Barrier: b.spec.ID, JobID: jt.GetJobKey().JobId, InvocationID: jt.InvocationID(), Attempt: jt.TaskContext.AttemptNumber, ResultOrdinal: jt.TaskOrdinal(), NodePath: g.NodePath, Selector: prepared.Selector, ResolvedSelector: prepared.ResolvedSelector, Cell: cell, Inputs: prepared.Inputs, Env: env}
		if b.spec.CapturePrepared {
			evidence, err := capturePreparedEvidence(local, deps, prepared, b.spec.ProbeTools, f.controlEvidenceLimit())
			if err != nil {
				return nil, true, f.controlError(err)
			}
			event.Prepared = evidence
		}
		var safe ControlEvent
		if err := snapshotRuntimeValue(local, event, &safe); err != nil {
			return nil, true, f.controlError(err)
		}
		encoded, err := json.Marshal(safe)
		if err != nil || len(encoded) > f.controlEvidenceLimit() {
			return nil, true, f.controlError(fmt.Errorf("prepared entry evidence exceeds limit or cannot be encoded"))
		}
		f.mu.Lock()
		b.entry = &safe
		f.report.Controls.Events = append(f.report.Controls.Events, safe)
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			f.report.Controls.Events = append(f.report.Controls.Events, ControlEvent{Kind: "completion_exited", Barrier: b.spec.ID, JobID: event.JobID})
			f.mu.Unlock()
		}()
		select {
		case <-local.Done():
			cause := context.Cause(local)
			f.mu.Lock()
			f.report.Controls.Events = append(f.report.Controls.Events, ControlEvent{Kind: "cancellation_observed", Barrier: b.spec.ID, JobID: event.JobID, Cause: fmt.Sprint(cause), NativeCancellation: errors.Is(cause, jobdb.ErrExecutionLeaseLost) || errors.Is(cause, jobdb.ErrJobCancelled)})
			f.mu.Unlock()
			return nil, true, local.Err()
		case <-b.release:
			if b.spec.Error != "" {
				return nil, true, fmt.Errorf("%s", b.spec.Error)
			}
			if len(b.spec.Objects) == 0 {
				return []byte(b.spec.Stdout), true, nil
			}
			raw, err := preparedObjectCompletion(prepared, b.spec)
			if err != nil {
				return nil, true, f.controlError(err)
			}
			return raw, true, nil
		}
	}
	return preparedtest.WithCompletion(ctx, hook), true, nil
}

// pumpControls runs on the harness controller, never inside the selected task.
// The supplied context belongs to the live controller, independent of the worker.
func (f *runtimeFixture) pumpControls(ctx context.Context, rt jobdb.WorkflowRuntime, tenant string) error {
	for _, a := range f.c.Runtime.Controls.Actions {
		id, _ := strings.CutSuffix(a.On, ".entered")
		for _, b := range f.barriers {
			f.mu.Lock()
			ready := b.spec.ID == id && b.entry != nil && !b.action
			job := ""
			if ready {
				b.action = true
				job = b.entry.JobID
				f.report.Controls.Events = append(f.report.Controls.Events, ControlEvent{Kind: a.Do + "_requested", Barrier: id, JobID: job})
			}
			f.mu.Unlock()
			if !ready {
				continue
			}
			if err := f.captureBaseline(ctx, rt, tenant, b); err != nil {
				return err
			}
			if a.Do == "release" {
				close(b.release)
			} else {
				if err := rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: jobdb.JobKey{TenantId: tenant, JobId: job}, Reason: "native recipe test control"}); err != nil {
					return f.controlError(err)
				}
			}
			f.mu.Lock()
			f.report.Controls.Events = append(f.report.Controls.Events, ControlEvent{Kind: a.Do + "_accepted", Barrier: id, JobID: job})
			f.mu.Unlock()
		}
	}
	return nil
}

func (f *runtimeFixture) waitControlledExits(ctx context.Context) error {
	for _, b := range f.barriers {
		f.mu.Lock()
		entered := b.entry != nil
		f.mu.Unlock()
		if !entered {
			continue
		}
		select {
		case <-b.exited:
		case <-ctx.Done():
			return f.controlError(fmt.Errorf("controlled operation did not exit before cleanup: %w", ctx.Err()))
		}
	}
	return nil
}

func (f *runtimeFixture) controlledExit(deps coreops.OpDependencies) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.barriers {
		if b.entry != nil && b.entry.InvocationID == deps.JobTool().InvocationID() {
			f.report.Controls.Events = append(f.report.Controls.Events, ControlEvent{Kind: "exited", Barrier: b.spec.ID, JobID: b.entry.JobID})
			close(b.exited)
		}
	}
}

func (f *runtimeFixture) captureControlFinal(ctx context.Context, rt jobdb.WorkflowRuntime, key jobdb.JobKey) {
	if f.report.Controls == nil {
		return
	}
	info, err := rt.GetJob(ctx, key)
	if err != nil {
		f.controlError(err)
		return
	}
	inspection, err := f.ctl.InspectJob(ctx, key)
	if err != nil {
		f.controlError(err)
		return
	}
	if f.c.Runtime.Controls.Snapshot != nil {
		f.report.Controls.FinalSnapshot = f.snapshotEffects(ctx, rt, key)
	}
	f.report.Controls.Final = &ControlJobState{JobID: key.JobId, Status: string(info.Status), Completion: info.CompletionStatus, FailureKind: inspection.FailureKind, HasOutput: inspection.Output != nil}
}

func (f *runtimeFixture) finishControls(result *CaseRunResult) {
	if f.report.Controls == nil {
		return
	}
	for _, b := range f.barriers {
		if b.entry == nil {
			f.report.Controls.Errors = append(f.report.Controls.Errors, "unused barrier "+b.spec.ID)
		}
		if !b.action {
			f.report.Controls.Errors = append(f.report.Controls.Errors, "unreleased barrier "+b.spec.ID)
		}
	}
	for _, a := range f.c.Runtime.Controls.Actions {
		if a.Do != "cancel_job" {
			continue
		}
		id, _ := strings.CutSuffix(a.On, ".entered")
		observed, accepted := false, false
		for _, e := range f.report.Controls.Events {
			if e.Barrier == id && e.Kind == "cancellation_observed" && e.NativeCancellation {
				observed = true
			}
			if e.Barrier == id && e.Kind == "cancel_job_accepted" {
				accepted = true
			}
		}
		final := f.report.Controls.Final
		if !observed || !accepted || final == nil || final.Status != "CANCELLED" || final.Completion != "cancelled" || final.FailureKind != "cancellation" || final.HasOutput {
			f.report.Controls.Errors = append(f.report.Controls.Errors, "native cancellation evidence incomplete for "+id)
		} else if len(f.report.Controls.Errors) == 0 && result.Status != "timed_out" {
			result.Status = "passed"
			result.FailureCategory = ""
			result.FailureReason = ""
		}
	}
	if final := f.report.Controls.FinalSnapshot; final != nil {
		preserved := final.Complete
		cancelled := false
		for _, a := range f.c.Runtime.Controls.Actions {
			if a.Do != "cancel_job" {
				continue
			}
			cancelled = true
			id, _ := strings.CutSuffix(a.On, ".entered")
			baseline := f.report.Controls.Baselines[id]
			if baseline == nil || !baseline.Complete || !reflect.DeepEqual(baseline.SuccessfulTasks, final.SuccessfulTasks) || !reflect.DeepEqual(baseline.Repositories, final.Repositories) || !reflect.DeepEqual(baseline.Children, final.Children) || baseline.DispatchCount != final.DispatchCount {
				preserved = false
			}
		}
		f.report.Controls.Preserved = cancelled && preserved
		if !preserved {
			f.report.Controls.Errors = append(f.report.Controls.Errors, "incomplete or changed baseline/final effects")
		}
	}
	f.report.Controls.DispatchComplete = f.report.Controls.DispatchDropped == 0
	for _, d := range f.report.Controls.Dispatch {
		if !d.Finished {
			f.report.Controls.DispatchComplete = false
		}
	}
	if !f.report.Controls.DispatchComplete {
		f.report.Controls.Errors = append(f.report.Controls.Errors, "dispatch evidence incomplete after drain")
	}
	f.report.Controls.Complete = len(f.report.Controls.Errors) == 0
	if !f.report.Controls.Complete {
		markFailure(result, "control_failure", strings.Join(f.report.Controls.Errors, "; "))
	}
}

// Native workers renew one-second leases in controlled cases. This makes a
// CancelJob lease invalidation observable promptly without stopping the worker.
type controlledRuntime struct{ caseRuntime }

func (r controlledRuntime) PollWork(ctx context.Context, req jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	req.LeaseDuration = time.Second
	return r.caseRuntime.PollWork(ctx, req)
}

// gateInput captures the genuine pending review before permitting its response.
// It never manufactures a receipt or completes the waiting task directly.
func (f *runtimeFixture) gateInput(ctx context.Context, response InputFixture, job string, form inputop.InputForm) (bool, error) {
	if response.After == "" {
		return true, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := false
	for _, e := range f.report.Controls.Events {
		if e.Kind == "review_pending" && e.RequestID == form.RequestID && e.JobID == job {
			seen = true
		}
	}
	if !seen {
		event := ControlEvent{Kind: "review_pending", JobID: job, RequestID: form.RequestID, NodePath: response.NodePath, Form: &form, Calls: f.report.Calls}
		var safe ControlEvent
		if err := snapshotRuntimeValue(ctx, event, &safe); err != nil {
			return false, err
		}
		encoded, err := json.Marshal(safe)
		if err != nil || len(encoded) > f.controlEvidenceLimit() {
			return false, fmt.Errorf("gated review evidence incomplete: size or encoding limit")
		}
		f.report.Controls.Events = append(f.report.Controls.Events, safe)
		return false, nil
	}
	id, kind, _ := strings.Cut(response.After, ".")
	for _, e := range f.report.Controls.Events {
		if e.Barrier == id && e.Kind == kind {
			return true, nil
		}
	}
	return false, nil
}

func (f *runtimeFixture) controlEvidenceLimit() int {
	if n := f.c.Runtime.Controls.MaxEvidenceBytes; n > 0 {
		return n
	}
	return 256 * 1024
}

// preparedObjectCompletion stages scripted checkpoint parts only after release.
// Normal extension decoding, schema checks and object publication still run.
func preparedObjectCompletion(prepared preparedtest.Invocation, spec PreparedBarrier) ([]byte, error) {
	root := prepared.Env["C2J_OBJECT_OUTBOX"]
	if root == "" {
		return nil, fmt.Errorf("prepared object staging unavailable")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("object staging directory was replaced")
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(spec.Stdout), &envelope); err != nil {
		return nil, err
	}
	if envelope == nil {
		return nil, fmt.Errorf("completion must be an object envelope")
	}
	if _, exists := envelope["objects"]; exists {
		return nil, fmt.Errorf("completion objects defined twice")
	}
	drafts := map[string]any{}
	for name, object := range spec.Objects {
		files := map[string]string{}
		for part, content := range object.Files {
			selected, err := fixturePath(root, filepath.Join(name, part))
			if err != nil {
				return nil, err
			}
			if err := writeFixture(root, filepath.Join(name, part), []byte(content)); err != nil {
				return nil, err
			}
			files[part] = selected
		}
		drafts[name] = map[string]any{"type": object.Type, "metadata": object.Metadata, "files": files}
	}
	envelope["objects"] = drafts
	return json.Marshal(envelope)
}
