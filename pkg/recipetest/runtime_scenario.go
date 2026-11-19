package recipetest

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/internal/dispatchtest"
	"github.com/colony-2/c2j/internal/scenariotest"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type scenarioGate struct {
	spec     DispatchHold
	release  chan struct{}
	entry    *dispatchtest.Attempt
	seen     map[string]bool
	released bool
	active   bool
}
type scenarioState struct {
	inspections   map[string]workflowctl.JobInspection
	report        *ScenarioReport
	gates         map[string]*scenarioGate
	workspaces    map[string]scenariotest.Workspace
	released      map[string]bool
	facts         map[string]bool
	actions       map[string]bool
	admissionSeen map[string]bool
	cancelled     map[string]bool
	stopped       bool
	session       int
	position      int
}

func (f *runtimeFixture) initScenario() {
	if f.c.Runtime.Controls.Scenario == nil {
		return
	}
	r := &ScenarioReport{Version: 1, Jobs: map[string]ScenarioJobReport{}, Retained: map[string]ScenarioRetained{}}
	s := &scenarioState{inspections: map[string]workflowctl.JobInspection{}, report: r, gates: map[string]*scenarioGate{}, workspaces: map[string]scenariotest.Workspace{}, released: map[string]bool{}, facts: map[string]bool{}, actions: map[string]bool{}, admissionSeen: map[string]bool{}, cancelled: map[string]bool{}}
	for _, h := range f.c.Runtime.Controls.Scenario.DispatchHolds {
		s.gates[h.ID] = &scenarioGate{spec: h, release: make(chan struct{}), seen: map[string]bool{}}
	}
	f.scenario = s
	f.report.Controls.Scenario = r
}
func (f *runtimeFixture) associateWorkspace(w scenariotest.Workspace) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := w.JobID + ":" + w.ScopeID
	if old, ok := f.scenario.workspaces[key]; ok && old != w {
		f.report.Controls.Errors = append(f.report.Controls.Errors, "ambiguous workspace association "+key)
		return
	}
	f.scenario.workspaces[key] = w
}
func matchesTask(q TaskSelection, a dispatchtest.Attempt, scope string) bool {
	return a.Name == q.TaskType && (q.InvocationID == "" || q.InvocationID == a.InvocationID) && (q.NodePath == "" || q.NodePath == a.NodePath) && (q.ScopeID == "" || q.ScopeID == scope) && q.Attempt == a.Attempt
}
func (f *runtimeFixture) holdDispatch(ctx context.Context, a dispatchtest.Attempt, scope string, index int) error {
	if f.scenario == nil {
		return nil
	}
	s := f.scenario
	f.mu.Lock()
	var gate *scenarioGate
	for _, g := range s.gates {
		j, ok := s.report.Jobs[g.spec.Job]
		if !ok || j.Ref.JobID != a.JobID || !matchesTask(g.spec.Task, a, scope) {
			continue
		}
		if !g.seen[a.InvocationID] {
			g.seen[a.InvocationID] = true
		}
		n := len(g.seen)
		if g.spec.Task.Occurrence > 0 && g.spec.Task.Occurrence != n {
			continue
		}
		if (g.entry != nil && (g.active || g.entry.InvocationID != a.InvocationID || g.entry.Attempt != a.Attempt)) || gate != nil {
			f.mu.Unlock()
			return f.controlError(fmt.Errorf("ambiguous or repeated dispatch hold %s", g.spec.ID))
		}
		gate = g
	}
	if gate == nil {
		f.report.Controls.Dispatch[index].Entered = true
		f.mu.Unlock()
		return ctx.Err()
	}
	gate.entry = &a
	gate.active = true
	f.report.Controls.Dispatch[index].Held = true
	f.mu.Unlock()
	defer func() { f.mu.Lock(); gate.active = false; f.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate.release:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.report.Controls.Dispatch[index].Entered = true
	f.mu.Unlock()
	return nil
}
func (f *runtimeFixture) bindScenarioJobs(ctx context.Context, rt caseRuntime, tenant string) error {
	bindings, err := f.responseBindings()
	if err != nil {
		return err
	}
	s := f.scenario
	for _, name := range sortedKeys(f.c.Runtime.Controls.Scenario.Jobs) {
		spec := f.c.Runtime.Controls.Scenario.Jobs[name]
		f.mu.Lock()
		_, bound := s.report.Jobs[name]
		receipts := append([]scenariotest.Submission(nil), s.report.Receipts...)
		caller := ScenarioJobReport{}
		if spec.Submission != nil {
			caller = s.report.Jobs[spec.Submission.Caller]
		}
		f.mu.Unlock()
		if bound {
			continue
		}
		id := ""
		var ref jobref.Ref
		switch {
		case spec.Root:
			id = f.rootJobID
		case spec.ChildBinding != "":
			id = bindings[spec.ChildBinding]
		case spec.Submission != nil:
			q := spec.Submission
			if caller.Ref.JobID == "" {
				continue
			}
			var matches []scenariotest.Submission
			invocation := ""
			occurrences := map[string]bool{}
			for _, r := range receipts {
				a := dispatchtest.Attempt{JobID: r.Caller, Name: r.TaskType, InvocationID: r.InvocationID, NodePath: r.NodePath, Attempt: r.Attempt}
				if r.Caller != caller.Ref.JobID || !matchesTask(q.Task, a, r.ScopeID) {
					continue
				}
				if !occurrences[r.InvocationID] {
					occurrences[r.InvocationID] = true
				}
				if q.Task.Occurrence > 0 && len(occurrences) != q.Task.Occurrence {
					continue
				}
				if invocation != "" && invocation != r.InvocationID {
					return fmt.Errorf("ambiguous submission target %s", name)
				}
				invocation = r.InvocationID
				matches = append(matches, r)
			}
			if len(matches) < q.Index {
				continue
			}
			ref = matches[q.Index-1].Ref
			id = ref.JobID
			if q.OutputPointer != "" {
				record, found, err := f.selectScenarioTask(ctx, rt, tenant, caller.Ref.JobID, q.Task)
				if err != nil {
					return err
				}
				if !found {
					continue
				}
				if record.attempt.Output == nil {
					return fmt.Errorf("submission output unavailable")
				}
				var output any
				if err = json.Unmarshal(record.attempt.Output.Data, &output); err != nil {
					return err
				}
				value, err := scenarioPointer(output, q.OutputPointer)
				if err != nil {
					return err
				}
				raw, err := json.Marshal(value)
				if err != nil {
					return err
				}
				var got jobref.Ref
				if json.Unmarshal(raw, &got) != nil || got != ref {
					return fmt.Errorf("submission output does not reconcile with native receipt for %s", name)
				}
			}
		}
		if id == "" {
			continue
		}
		info, err := rt.GetJob(ctx, jobdb.JobKey{TenantId: tenant, JobId: id})
		if err != nil {
			return err
		}
		if ref.JobID == "" {
			ref = jobref.Ref{TenantID: tenant, JobID: id, GoalID: info.GoalID, Cell: info.Cell, CellID: info.CellID, CellRepo: info.CellRepo}
		}
		if ref.TenantID != tenant || ref.Cell != info.Cell || ref.GoalID != info.GoalID || ref.CellID != info.CellID || ref.CellRepo != info.CellRepo {
			return fmt.Errorf("named job %s native identity mismatch", name)
		}
		f.mu.Lock()
		for alias, other := range s.report.Jobs {
			if other.Ref.JobID == ref.JobID && f.c.Runtime.Controls.Scenario.Outcomes[alias] != f.c.Runtime.Controls.Scenario.Outcomes[name] {
				f.mu.Unlock()
				return fmt.Errorf("conflicting outcomes for aliases %s and %s", alias, name)
			}
		}
		s.report.Jobs[name] = ScenarioJobReport{Ref: ref}
		// Arming a gate after its task was dispatched cannot establish ordering.
		for _, g := range s.gates {
			if g.spec.Job != name {
				continue
			}
			for _, a := range f.report.Controls.Dispatch {
				if a.JobID == id && matchesTask(g.spec.Task, a.Attempt, a.ScopeID) {
					f.mu.Unlock()
					return fmt.Errorf("dispatch hold %s bound after target dispatched", g.spec.ID)
				}
			}
		}
		f.mu.Unlock()
		if err := f.reconcileDispatchForbids(); err != nil {
			return err
		}
	}
	return nil
}
func scenarioPointer(v any, p string) (any, error) {
	if p == "" {
		return v, nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("output pointer must be a JSON pointer")
	}
	for _, part := range strings.Split(p[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = x[part]
			if !ok {
				return nil, fmt.Errorf("output pointer missing %s", p)
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, fmt.Errorf("invalid output pointer %s", p)
			}
			v = x[i]
		default:
			return nil, fmt.Errorf("invalid output pointer %s", p)
		}
	}
	return v, nil
}

type selectedScenarioTask struct {
	task    swfutil.ExecutionTask
	attempt jobdb.TaskAttempt
	origin  string
}

func (f *runtimeFixture) selectScenarioTask(ctx context.Context, rt caseRuntime, tenant, id string, q TaskSelection) (selectedScenarioTask, bool, error) {
	run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: tenant, JobId: id}, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true, IncludeAttemptInputs: true})
	if err != nil {
		return selectedScenarioTask{}, false, err
	}
	tasks, err := swfutil.ExecutionTasks(run)
	if err != nil {
		return selectedScenarioTask{}, false, err
	}
	var matches []selectedScenarioTask
	for _, t := range tasks {
		for _, a := range t.Attempts {
			if a.Input == nil {
				continue
			}
			var envelope struct {
				Context struct {
					NodePath  string `json:"node_path"`
					Workspace *struct {
						ScopeID string `json:"scope_id"`
					} `json:"workspace"`
				} `json:"context"`
				ScopeID string `json:"scope_id"`
			}
			if err := json.Unmarshal(a.Input.Data, &envelope); err != nil {
				return selectedScenarioTask{}, false, err
			}
			node := envelope.Context.NodePath
			taskScope := envelope.ScopeID
			if envelope.Context.Workspace != nil {
				taskScope = envelope.Context.Workspace.ScopeID
			}
			if envelope.ScopeID != "" {
				f.mu.Lock()
				w := f.scenario.workspaces[id+":"+envelope.ScopeID]
				f.mu.Unlock()
				node = w.NodePath
			}
			if !matchesTask(q, dispatchtest.Attempt{Name: t.TaskType, NodePath: node, InvocationID: t.InvocationID, Attempt: a.Attempt}, taskScope) {
				continue
			}
			origin := id
			for _, e := range run.Execution {
				if e.Record.Kind == "result" && e.Record.ID == fmt.Sprintf("%s/attempt:%d", t.InvocationID, a.Attempt) && e.Record.Source != nil {
					origin = e.Record.Source.JobId
				}
			}
			matches = append(matches, selectedScenarioTask{task: t, attempt: a, origin: origin})
		}
	}
	if q.Occurrence > 0 {
		if len(matches) < q.Occurrence {
			return selectedScenarioTask{}, false, nil
		}
		return matches[q.Occurrence-1], true, nil
	}
	if len(matches) > 1 {
		return selectedScenarioTask{}, false, fmt.Errorf("ambiguous persisted task %s in %s", q.TaskType, id)
	}
	if len(matches) == 0 {
		return selectedScenarioTask{}, false, nil
	}
	return matches[0], true, nil
}
func (f *runtimeFixture) observeScenarioJobs(ctx context.Context, rt caseRuntime, tenant string) error {
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	for _, v := range f.c.Runtime.Secrets {
		if e := redactor.Register(v); e != nil {
			return e
		}
	}
	secretSession := redactor.NewSession()
	defer secretSession.Close()
	safeCtx := secrets.WithSession(ctx, secretSession)
	s := f.scenario
	observed := map[string]ScenarioJobReport{}
	for _, name := range sortedKeys(s.report.Jobs) {
		f.mu.Lock()
		j := s.report.Jobs[name]
		f.mu.Unlock()
		key := j.Ref.Key()
		if same, ok := observed[key.JobId]; ok {
			f.mu.Lock()
			s.report.Jobs[name] = same
			f.mu.Unlock()
			continue
		}

		info, err := rt.GetJob(ctx, key)
		if err != nil {
			return err
		}
		inspection, known := s.inspections[key.JobId]
		if !known || !inspection.Terminal {
			inspection, err = f.ctl.InspectJob(ctx, key)
			if err != nil {
				return err
			}
			s.inspections[key.JobId] = inspection
		}
		goal, err := rt.GetGoal(ctx, jobdb.GoalKey{TenantId: tenant, GoalID: info.GoalID})
		if err != nil {
			return err
		}
		j.State = ControlJobState{JobID: key.JobId, Status: string(info.Status), Completion: info.CompletionStatus, FailureKind: inspection.FailureKind, HasOutput: inspection.Output != nil}
		j.FailureMessage = inspection.FailureMessage
		j.Goal = &goal
		j.BlockingWait = info.BlockingWait
		f.mu.Lock()
		var safe ScenarioJobReport
		if e := snapshotRuntimeValue(safeCtx, j, &safe); e != nil {
			f.mu.Unlock()
			return e
		}
		s.report.Jobs[name] = safe
		observed[key.JobId] = safe
		f.mu.Unlock()
	}
	return nil
}
func (f *runtimeFixture) scenarioFact(fact ScenarioFact) error {
	s := f.scenario
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.facts[fact.ID] {
		return nil
	}
	if len(s.report.Events) >= f.c.Runtime.Controls.Scenario.limits().Events {
		return fmt.Errorf("scenario event limit exceeded")
	}
	s.position++
	fact.Position = s.position
	s.facts[fact.ID] = true
	s.report.Events = append(s.report.Events, fact)
	return nil
}
func (f *runtimeFixture) pumpScenario(ctx context.Context, rt caseRuntime, tenant string, lifecycle func(ScenarioAction) error) (bool, error) {
	if err := f.bindScenarioJobs(ctx, rt, tenant); err != nil {
		return false, f.controlError(err)
	}
	if err := f.observeScenarioJobs(ctx, rt, tenant); err != nil {
		return false, f.controlError(err)
	}
	s := f.scenario
	spec := f.c.Runtime.Controls.Scenario
	for name, want := range spec.Outcomes {
		j, ok := s.report.Jobs[name]
		if !ok {
			continue
		}
		v := s.inspections[j.Ref.JobID]
		if v.Terminal && (v.Status != want.Status || (want.FailureKind != "" && v.FailureKind != want.FailureKind) || (want.Message != "" && !strings.Contains(v.FailureMessage, want.Message))) {
			return false, f.controlError(fmt.Errorf("unexpected terminal outcome for %s: %s (%s: %s)", name, v.Status, v.FailureKind, j.FailureMessage))
		}
	}
	for _, e := range spec.Events {
		if s.facts[e.ID] {
			continue
		}
		fact := ScenarioFact{ID: e.ID}
		j := s.report.Jobs[e.Job]
		fact.JobID = j.Ref.JobID
		ready := false
		switch e.Kind {
		case "submission":
			ready = j.Ref.JobID != ""
		case "dispatch":
			f.mu.Lock()
			g := s.gates[e.Hold]
			if g.entry != nil {
				a := *g.entry
				ready = true
				fact.JobID = a.JobID
				fact.InvocationID = a.InvocationID
				fact.Attempt = a.Attempt
				fact.ResultOrdinal = a.ResultOrdinal
			}
			f.mu.Unlock()
		case "settled":
			if j.Ref.JobID != "" {
				v := s.inspections[j.Ref.JobID]
				ready = v.Terminal && v.Status == e.Status
				fact.Status = v.Status
			}
		case "task_result":
			if j.Ref.JobID != "" {
				r, ok, err := f.selectScenarioTask(ctx, rt, tenant, j.Ref.JobID, *e.Task)
				if err != nil {
					return false, f.controlError(err)
				}
				ready = ok && r.attempt.State == e.Status
				if ready {
					fact.InvocationID = r.task.InvocationID
					fact.Attempt = r.attempt.Attempt
					fact.ResultOrdinal = r.attempt.Ordinal
					fact.Status = r.attempt.State
				}
			}
		}
		if ready {
			if err := f.scenarioFact(fact); err != nil {
				return false, f.controlError(err)
			}
		}
	}
	for _, a := range spec.Actions {
		if s.actions[a.ID] {
			continue
		}
		ready := true
		for _, p := range a.AfterAll {
			ready = ready && (s.facts[p] || s.actions[p])
		}
		if !ready {
			continue
		}
		receipt := ScenarioActionReceipt{ID: a.ID, Do: a.Do, Target: a.Target, AfterAll: append([]string(nil), a.AfterAll...)}
		var err error
		switch a.Do {
		case "release":
			err = f.releaseScenarioHold(ctx, rt, a.Target)
		case "cancel_job":
			j := s.report.Jobs[a.Target]
			if j.Ref.JobID == "" {
				err = fmt.Errorf("cancellation target %s unresolved", a.Target)
				break
			}
			err = rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: j.Ref.Key(), Reason: "native recipe scenario control"})
			if err == nil {
				s.cancelled[j.Ref.JobID] = true
				receipt.SourceJobID = j.Ref.JobID
			}
		case "restart_job":
			err = f.restartScenarioJob(ctx, rt, tenant, a, &receipt)
		default:
			err = lifecycle(a)
		}
		if err != nil {
			return false, f.controlError(fmt.Errorf("scenario action %s: %w", a.ID, err))
		}
		f.mu.Lock()
		s.position++
		receipt.Position = s.position
		s.actions[a.ID] = true
		s.report.Actions = append(s.report.Actions, receipt)
		f.mu.Unlock()
		if err = f.checkScenarioSize(); err != nil {
			return false, f.controlError(err)
		}
		// Reopen replaces runtime-bound readers. Resume inspection on the next pass.
		if a.Do == "reopen_runtime" || a.Do == "start_worker" || a.Do == "stop_worker" {
			return false, nil
		}
	}
	for _, e := range spec.Events {
		if !s.facts[e.ID] {
			return false, nil
		}
	}
	for _, a := range spec.Actions {
		if !s.actions[a.ID] {
			return false, nil
		}
	}
	for name, want := range spec.Outcomes {
		j, ok := s.report.Jobs[name]
		if !ok {
			return false, nil
		}
		v := s.inspections[j.Ref.JobID]
		if !v.Terminal {
			return false, nil
		}
		if v.Status != want.Status || (want.FailureKind != "" && v.FailureKind != want.FailureKind) || (want.Message != "" && !strings.Contains(v.FailureMessage, want.Message)) {
			return false, f.controlError(fmt.Errorf("job %s outcome: got %s (%s: %s), require %+v", name, v.Status, v.FailureKind, j.FailureMessage, want))
		}
	}
	for _, h := range spec.AdmissionHolds {
		if !s.released[h.ID] {
			return false, nil
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range s.gates {
		if g.entry == nil {
			return false, nil
		}
		if g.released {
			continue
		}
		if !(g.spec.Disposition == "cancelled" && s.cancelled[g.entry.JobID] && s.report.Jobs[g.spec.Job].State.Status == "CANCELLED") {
			return false, nil
		}
		native := false
		for _, d := range f.report.Controls.Dispatch {
			if d.JobID == g.entry.JobID && d.InvocationID == g.entry.InvocationID && d.Held && d.Finished && !d.Entered && d.NativeCancellation {
				native = true
			}
		}
		if !native {
			return false, nil
		}
	}

	return true, nil
}
func (f *runtimeFixture) releaseScenarioHold(ctx context.Context, rt caseRuntime, id string) error {
	// Validate and release atomically with respect to native acquisition and
	// submission. No selected target can slip into execution during this check.
	f.nativeMu.Lock()
	defer f.nativeMu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.scenario
	if s.released[id] {
		return fmt.Errorf("hold %s released twice", id)
	}
	if g := s.gates[id]; g != nil {
		if g.entry == nil {
			return fmt.Errorf("dispatch hold %s has not arrived", id)
		}
		g.released = true
		close(g.release)
	} else {
		found := false
		for _, h := range f.c.Runtime.Controls.Scenario.AdmissionHolds {
			if h.ID != id {
				continue
			}
			found = true
			for _, name := range h.Jobs {
				j, ok := s.report.Jobs[name]
				if !ok {
					return fmt.Errorf("admission hold %s has unresolved job %s", id, name)
				}
				if j.Ref.Cell != h.Cell || j.Ref.CellRepo != f.seededRepos[h.Cell] {
					return fmt.Errorf("admission alias %s outside selected repository", name)
				}
				page, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{j.Ref.TenantID}, JobKeys: []jobdb.JobKey{j.Ref.Key()}, IncludeHidden: true})
				if err != nil {
					return fmt.Errorf("admission hold %s target %s: %w", id, name, err)
				}
				if len(page.Jobs) != 1 || page.Jobs[0].JobKey != j.Ref.Key() {
					return fmt.Errorf("admission hold %s target %s unavailable", id, name)
				}
				target := page.Jobs[0]
				if target.Cell != h.Cell || target.CellRepo != f.seededRepos[h.Cell] {
					return fmt.Errorf("admission alias %s native repository mismatch", name)
				}
				var meta struct {
					Recipe string `json:"recipe"`
				}
				if json.Unmarshal(target.Metadata, &meta) != nil || meta.Recipe != h.Recipe {
					return fmt.Errorf("admission hold %s target %s recipe mismatch: require %s", id, name, h.Recipe)
				}
				if target.NextRoute == nil || *target.NextRoute != h.Route {
					return fmt.Errorf("admission hold %s target %s route mismatch: require %+v, got %+v", id, name, h.Route, target.NextRoute)
				}
			}
		}
		if !found {
			return fmt.Errorf("unknown hold %s", id)
		}
	}
	s.released[id] = true
	return nil
}
func (f *runtimeFixture) checkScenarioSize() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := json.Marshal(f.scenario.report)
	if err != nil {
		return err
	}
	if len(raw) > f.c.Runtime.Controls.Scenario.limits().ReportBytes {
		return fmt.Errorf("scenario report byte limit exceeded")
	}
	return nil
}
func (f *runtimeFixture) finishScenario(ctx context.Context, rt caseRuntime, tenant string) error {
	f.scenario.report.Complete = false
	if err := f.observeScenarioJobs(ctx, rt, tenant); err != nil {
		return err
	}
	for name, want := range f.c.Runtime.Controls.Scenario.Outcomes {
		j, ok := f.scenario.report.Jobs[name]
		if !ok {
			return fmt.Errorf("unresolved scenario job %s", name)
		}
		state := f.scenario.inspections[j.Ref.JobID]
		if !state.Terminal || state.Status != want.Status || (want.FailureKind != "" && state.FailureKind != want.FailureKind) || (want.Message != "" && !strings.Contains(state.FailureMessage, want.Message)) {
			return fmt.Errorf("scenario job %s has not reached required outcome %s", name, want.Status)
		}
	}
	for _, hold := range f.c.Runtime.Controls.Scenario.AdmissionHolds {
		if !f.scenario.released[hold.ID] {
			return fmt.Errorf("unreleased admission hold %s", hold.ID)
		}
	}
	// Recheck unqualified selectors after every peer has settled: a second
	// match arriving after the first fact must still fail uniqueness.
	for _, e := range f.c.Runtime.Controls.Scenario.Events {
		if e.Kind == "task_result" {
			j := f.scenario.report.Jobs[e.Job]
			if _, _, err := f.selectScenarioTask(ctx, rt, tenant, j.Ref.JobID, *e.Task); err != nil {
				return err
			}
		}
	}
	for name, spec := range f.c.Runtime.Controls.Scenario.Jobs {
		q := spec.Submission
		if q == nil || q.Task.Occurrence != 0 {
			continue
		}
		caller := f.scenario.report.Jobs[q.Caller]
		invocations := map[string]bool{}
		for _, r := range f.scenario.report.Receipts {
			if r.Caller == caller.Ref.JobID && matchesTask(q.Task, dispatchtest.Attempt{Name: r.TaskType, InvocationID: r.InvocationID, NodePath: r.NodePath, Attempt: r.Attempt}, r.ScopeID) {
				invocations[r.InvocationID] = true
			}
		}
		if len(invocations) > 1 {
			return fmt.Errorf("ambiguous submission target %s", name)
		}
	}
	if err := f.retainScenario(ctx, rt, tenant, true); err != nil {
		return err
	}
	if err := f.checkScenarioSize(); err != nil {
		return err
	}
	for _, a := range f.c.Runtime.Controls.Scenario.Actions {
		if !f.scenario.actions[a.ID] {
			return fmt.Errorf("unresolved scenario action %s", a.ID)
		}
	}
	for _, e := range f.c.Runtime.Controls.Scenario.Events {
		if !f.scenario.facts[e.ID] {
			return fmt.Errorf("unresolved scenario event %s", e.ID)
		}
	}
	for _, g := range f.scenario.gates {
		if g.entry == nil {
			return fmt.Errorf("unresolved dispatch hold %s", g.spec.ID)
		}
		if g.released {
			continue
		}
		if g.spec.Disposition != "cancelled" {
			return fmt.Errorf("unreleased dispatch hold %s", g.spec.ID)
		}
		native := false
		for _, d := range f.report.Controls.Dispatch {
			if d.JobID == g.entry.JobID && d.InvocationID == g.entry.InvocationID && d.Held && d.Finished && !d.Entered && d.NativeCancellation {
				native = true
			}
		}
		if !native {
			return fmt.Errorf("hold %s lacks native lease cancellation evidence", g.spec.ID)
		}
	}
	f.scenario.report.Complete = len(f.report.Controls.Errors) == 0
	return nil
}
