package recipetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/internal/scenariotest"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Scenario is deliberately a fixture contract, not a recipe or worker API.
type Scenario struct {
	Jobs           map[string]ScenarioJob     `json:"jobs"`
	AdmissionHolds []AdmissionHold            `json:"admission_holds,omitempty"`
	DispatchHolds  []DispatchHold             `json:"dispatch_holds,omitempty"`
	Events         []ScenarioEvent            `json:"events,omitempty"`
	Actions        []ScenarioAction           `json:"actions,omitempty"`
	Outcomes       map[string]ScenarioOutcome `json:"outcomes"`
	Retain         []ScenarioRetention        `json:"retain,omitempty"`
	Limits         ScenarioLimits             `json:"limits,omitempty"`
}
type ScenarioJob struct {
	Root         bool                 `json:"root,omitempty"`
	ChildBinding string               `json:"child_binding,omitempty"`
	Submission   *SubmissionSelection `json:"submission,omitempty"`
}
type SubmissionSelection struct {
	Caller        string        `json:"caller"`
	Task          TaskSelection `json:"task"`
	Index         int           `json:"index"`
	OutputPointer string        `json:"output_pointer,omitempty"`
}
type TaskSelection struct {
	TaskType     string `json:"task_type"`
	NodePath     string `json:"node_path,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
	ScopeID      string `json:"scope_id,omitempty"`
	Occurrence   int    `json:"occurrence,omitempty"`
	Attempt      int    `json:"attempt"`
}
type AdmissionHold struct {
	ID     string      `json:"id"`
	Cell   string      `json:"cell"`
	Recipe string      `json:"recipe"`
	Route  jobdb.Route `json:"route"`
	Jobs   []string    `json:"jobs"`
}
type DispatchHold struct {
	ID          string        `json:"id"`
	Job         string        `json:"job"`
	Task        TaskSelection `json:"task"`
	Disposition string        `json:"disposition,omitempty"`
}
type ScenarioEvent struct {
	ID     string         `json:"id"`
	Kind   string         `json:"kind"`
	Job    string         `json:"job,omitempty"`
	Hold   string         `json:"hold,omitempty"`
	Task   *TaskSelection `json:"task,omitempty"`
	Status string         `json:"status,omitempty"`
}
type ScenarioAction struct {
	ID       string           `json:"id"`
	AfterAll []string         `json:"after_all"`
	Do       string           `json:"do"`
	Target   string           `json:"target,omitempty"`
	Restart  *ScenarioRestart `json:"restart,omitempty"`
}
type ScenarioRestart struct {
	Path           []string        `json:"path"`
	Occurrence     int             `json:"occurrence,omitempty"`
	RequiredPrefix []TaskSelection `json:"required_prefix"`
	RequestID      string          `json:"request_id"`
	Continuation   string          `json:"continuation"`
}
type ScenarioOutcome struct {
	Status      string `json:"status"`
	FailureKind string `json:"failure_kind,omitempty"`
	Message     string `json:"message,omitempty"`
}
type ScenarioRetention struct {
	Part         string        `json:"part,omitempty"`
	ID           string        `json:"id"`
	Job          string        `json:"job"`
	Task         TaskSelection `json:"task"`
	Artifact     string        `json:"artifact,omitempty"`
	CompareTo    string        `json:"compare_to,omitempty"`
	ExpectBase64 string        `json:"expect_base64,omitempty"`
}
type ScenarioLimits struct {
	Jobs        int `json:"jobs,omitempty"`
	Events      int `json:"events,omitempty"`
	Receipts    int `json:"receipts,omitempty"`
	Actions     int `json:"actions,omitempty"`
	ReportBytes int `json:"report_bytes,omitempty"`
}
type ScenarioReport struct {
	Version   int                          `json:"version"`
	Jobs      map[string]ScenarioJobReport `json:"jobs"`
	Receipts  []scenariotest.Submission    `json:"receipts"`
	Events    []ScenarioFact               `json:"events"`
	Actions   []ScenarioActionReceipt      `json:"actions"`
	Sessions  []ScenarioSession            `json:"sessions"`
	Admission []AdmissionReceipt           `json:"admission"`
	Retained  map[string]ScenarioRetained  `json:"retained"`
	Complete  bool                         `json:"complete"`
}
type ScenarioJobReport struct {
	Ref            jobref.Ref                `json:"ref"`
	State          ControlJobState           `json:"state"`
	FailureMessage string                    `json:"failure_message,omitempty"`
	Goal           *jobdb.GoalInfo           `json:"goal,omitempty"`
	BlockingWait   *jobdb.DependencyWaitInfo `json:"blocking_wait,omitempty"`
}
type ScenarioFact struct {
	ID            string `json:"id"`
	Position      int    `json:"position"`
	JobID         string `json:"job_id,omitempty"`
	InvocationID  string `json:"invocation_id,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	ResultOrdinal int64  `json:"result_ordinal,omitempty"`
	Status        string `json:"status,omitempty"`
}
type ScenarioActionReceipt struct {
	ContextSHA256 string                   `json:"context_sha256,omitempty"`
	ID            string                   `json:"id"`
	Do            string                   `json:"do"`
	Target        string                   `json:"target,omitempty"`
	AfterAll      []string                 `json:"after_all"`
	Position      int                      `json:"position"`
	SourceJobID   string                   `json:"source_job_id,omitempty"`
	Ref           *jobref.Ref              `json:"ref,omitempty"`
	Scope         *jobrestart.RestartScope `json:"scope,omitempty"`
	RequestID     string                   `json:"request_id,omitempty"`
	Fingerprint   string                   `json:"fingerprint,omitempty"`
}
type ScenarioSession struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"`
}
type AdmissionReceipt struct {
	Position int         `json:"position"`
	Session  int         `json:"session"`
	LeaseID  string      `json:"lease_id,omitempty"`
	Route    jobdb.Route `json:"route"`
	JobID    string      `json:"job_id"`
	Hold     string      `json:"hold,omitempty"`
	Kind     string      `json:"kind"`
}
type ScenarioRetained struct {
	OriginJobID   string             `json:"origin_job_id"`
	ObservedJobID string             `json:"observed_job_id"`
	InvocationID  string             `json:"invocation_id"`
	Attempt       int                `json:"attempt"`
	ResultOrdinal int64              `json:"result_ordinal"`
	State         string             `json:"state"`
	ArtifactKey   *jobdb.ArtifactKey `json:"artifact_key,omitempty"`
	Content       RetainedContent    `json:"content"`
}

func (s *Scenario) UnmarshalJSON(raw []byte) error {
	// Inspect tokens before decoding maps: encoding/json otherwise silently
	// overwrites duplicate aliases and fields, losing the selected identity.
	d := json.NewDecoder(bytes.NewReader(raw))
	var member func() error
	member = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return fmt.Errorf("scenario members must not be null")
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("invalid scenario object member")
				}
				if seen[name] {
					return fmt.Errorf("duplicate scenario member %q", name)
				}
				seen[name] = true
				if err = member(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := member(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid scenario delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := member(); err != nil {
		return err
	}
	type plain Scenario
	var v plain
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return fmt.Errorf("scenario: %w", err)
	}
	*s = Scenario(v)
	return nil
}
func (s *Scenario) limits() ScenarioLimits {
	l := s.Limits
	if l.Jobs == 0 {
		l.Jobs = 128
	}
	if l.Events == 0 {
		l.Events = 1024
	}
	if l.Receipts == 0 {
		l.Receipts = 256
	}
	if l.Actions == 0 {
		l.Actions = 128
	}
	if l.ReportBytes == 0 {
		l.ReportBytes = 1024 * 1024
	}
	return l
}
func exact(s string) bool { return strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "*?[]") }
func validateTask(s TaskSelection) error {
	if !exact(s.TaskType) || s.Attempt < 1 || s.Occurrence < 0 || (!exact(s.NodePath) && !exact(s.InvocationID) && !exact(s.ScopeID)) {
		return fmt.Errorf("task requires exact type and node_path, invocation_id or scope_id, positive attempt and nonnegative occurrence")
	}
	for _, v := range []string{s.NodePath, s.InvocationID, s.ScopeID} {
		if v != "" && !exact(v) {
			return fmt.Errorf("task selector must be exact")
		}
	}
	return nil
}
func validateScenario(c *RuntimeCase) error {
	s := c.Controls.Scenario
	if s == nil {
		return nil
	}
	if c.ExpectError != "" {
		return fmt.Errorf("scenario outcomes cannot combine with expect_error")
	}
	l := s.limits()
	if l.Jobs < 1 || l.Jobs > 1024 || l.Events < 1 || l.Events > 4096 || l.Receipts < 1 || l.Receipts > 4096 || l.Actions < 1 || l.Actions > 1024 || l.ReportBytes < 1 || l.ReportBytes > 8*1024*1024 {
		return fmt.Errorf("scenario limits exceed supported bounds")
	}
	if len(s.Jobs) > l.Jobs || len(s.Events) > l.Events || len(s.Actions) > l.Actions {
		return fmt.Errorf("scenario declarations exceed limits")
	}
	jobs := map[string]bool{}
	roots := 0
	for name, j := range s.Jobs {
		if !exact(name) {
			return fmt.Errorf("invalid job alias %q", name)
		}
		jobs[name] = true
		n := 0
		if j.Root {
			n++
			roots++
		}
		if j.ChildBinding != "" {
			n++
		}
		if j.Submission != nil {
			n++
		}
		if n != 1 {
			return fmt.Errorf("job %s requires exactly one source", name)
		}
	}
	if roots != 1 {
		return fmt.Errorf("scenario requires exactly one root alias")
	}
	for _, a := range s.Actions {
		if r := a.Restart; r != nil {
			if !exact(r.Continuation) || jobs[r.Continuation] {
				return fmt.Errorf("duplicate or invalid continuation %s", r.Continuation)
			}
			jobs[r.Continuation] = true
		}
	}
	if len(jobs) > l.Jobs {
		return fmt.Errorf("scenario jobs exceed limit")
	}
	job := func(n string) error {
		if !jobs[n] {
			return fmt.Errorf("unknown job %q", n)
		}
		return nil
	}
	if policy := c.Controls.Dispatch; policy != nil {
		for _, m := range append(append([]DispatchMatcher(nil), policy.Allow...), policy.Forbid...) {
			if m.Job != "" {
				if err := job(m.Job); err != nil {
					return err
				}
			}
		}
	}
	for n, j := range s.Jobs {
		if q := j.Submission; q != nil {
			if err := job(q.Caller); err != nil {
				return err
			}
			if q.Caller == n || q.Index < 1 {
				return fmt.Errorf("invalid submission selection %s", n)
			}
			if err := validateTask(q.Task); err != nil {
				return err
			}
		}
	}
	jobVisit, jobDone := map[string]bool{}, map[string]bool{}
	var visitJob func(string) error
	visitJob = func(n string) error {
		if jobVisit[n] {
			return fmt.Errorf("job source cycle at %s", n)
		}
		if jobDone[n] {
			return nil
		}
		jobVisit[n] = true
		if j, ok := s.Jobs[n]; ok && j.Submission != nil {
			if err := visitJob(j.Submission.Caller); err != nil {
				return err
			}
		}
		jobVisit[n] = false
		jobDone[n] = true
		return nil
	}
	for n := range s.Jobs {
		if err := visitJob(n); err != nil {
			return err
		}
	}
	holds := map[string]bool{}
	for _, h := range s.AdmissionHolds {
		if holds[h.ID] || !exact(h.ID) || !exact(h.Cell) || !exact(h.Recipe) || h.Route.JobType == "" || len(h.Jobs) == 0 {
			return fmt.Errorf("invalid admission hold %s", h.ID)
		}
		if _, ok := c.Cells[h.Cell]; !ok && h.Cell != c.Cell && !(c.Cell == "" && h.Cell == "root") {
			return fmt.Errorf("unseeded admission cell %s", h.Cell)
		}
		holds[h.ID] = true
		for _, n := range h.Jobs {
			if err := job(n); err != nil {
				return err
			}
		}
	}
	dispatch := map[string]bool{}
	for _, h := range s.DispatchHolds {
		if holds[h.ID] || !exact(h.ID) {
			return fmt.Errorf("duplicate or invalid hold %s", h.ID)
		}
		holds[h.ID] = true
		dispatch[h.ID] = true
		if err := job(h.Job); err != nil {
			return err
		}
		if err := validateTask(h.Task); err != nil {
			return err
		}
		if h.Disposition != "" && h.Disposition != "cancelled" {
			return fmt.Errorf("invalid hold disposition")
		}
	}
	refs := map[string]bool{}
	for _, e := range s.Events {
		if !exact(e.ID) || refs[e.ID] {
			return fmt.Errorf("duplicate or invalid event %s", e.ID)
		}
		refs[e.ID] = true
		switch e.Kind {
		case "submission", "settled", "task_result":
			if err := job(e.Job); err != nil {
				return err
			}
		case "dispatch":
			if !dispatch[e.Hold] {
				return fmt.Errorf("unknown dispatch hold %s", e.Hold)
			}
		default:
			return fmt.Errorf("unknown event kind %s", e.Kind)
		}
		if e.Kind == "task_result" {
			if e.Task == nil {
				return fmt.Errorf("task_result requires task")
			}
			if err := validateTask(*e.Task); err != nil {
				return err
			}
			if e.Status != "SUCCEEDED" && e.Status != "FAILED" {
				return fmt.Errorf("task_result requires SUCCEEDED or FAILED")
			}
		}
		if e.Kind == "settled" && e.Status != "completed" && e.Status != "failed" && e.Status != "cancelled" {
			return fmt.Errorf("settled requires terminal status")
		}
	}
	for _, a := range s.Actions {
		if !exact(a.ID) || refs[a.ID] {
			return fmt.Errorf("duplicate or invalid action %s", a.ID)
		}
		refs[a.ID] = true
	}
	graph := map[string][]string{}
	for _, a := range s.Actions {
		if len(a.AfterAll) == 0 {
			return fmt.Errorf("action %s requires after_all", a.ID)
		}
		for _, r := range a.AfterAll {
			if !refs[r] {
				return fmt.Errorf("unknown prerequisite %s", r)
			}
		}
		graph[a.ID] = a.AfterAll
		switch a.Do {
		case "release":
			if !holds[a.Target] {
				return fmt.Errorf("unknown hold %s", a.Target)
			}
		case "cancel_job", "restart_job":
			if err := job(a.Target); err != nil {
				return err
			}
		case "stop_worker", "reopen_runtime", "start_worker":
			if a.Target != "" {
				return fmt.Errorf("lifecycle action has no target")
			}
		default:
			return fmt.Errorf("unknown scenario action %s", a.Do)
		}
		if a.Do == "restart_job" {
			r := a.Restart
			if r == nil || !exact(r.RequestID) || len(r.Path) == 0 || len(r.RequiredPrefix) == 0 || r.Occurrence < 0 {
				return fmt.Errorf("restart requires path, request_id, continuation and required_prefix")
			}
			for _, t := range r.RequiredPrefix {
				if err := validateTask(t); err != nil {
					return err
				}
			}
		} else if a.Restart != nil {
			return fmt.Errorf("restart selection on non-restart action")
		}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(n string) error {
		if visiting[n] {
			return fmt.Errorf("scenario action cycle at %s", n)
		}
		if done[n] {
			return nil
		}
		visiting[n] = true
		for _, d := range graph[n] {
			if err := visit(d); err != nil {
				return err
			}
		}
		visiting[n] = false
		done[n] = true
		return nil
	}
	for n := range graph {
		if err := visit(n); err != nil {
			return err
		}
	}
	for n := range jobs {
		v, ok := s.Outcomes[n]
		if !ok {
			return fmt.Errorf("missing outcome for %s", n)
		}
		if v.Status != "completed" && v.Status != "failed" && v.Status != "cancelled" {
			return fmt.Errorf("invalid outcome %s", n)
		}
	}
	for n := range s.Outcomes {
		if err := job(n); err != nil {
			return err
		}
	}
	retained := map[string]bool{}
	for _, r := range s.Retain {
		if !exact(r.ID) || retained[r.ID] {
			return fmt.Errorf("duplicate or invalid retention %s", r.ID)
		}
		retained[r.ID] = true
		if r.Part != "" && r.Part != "input" && r.Part != "output" {
			return fmt.Errorf("retention part requires input or output")
		}
		if err := job(r.Job); err != nil {
			return err
		}
		if err := validateTask(r.Task); err != nil {
			return err
		}
	}
	for _, r := range s.Retain {
		if r.CompareTo != "" && !retained[r.CompareTo] {
			return fmt.Errorf("unknown retention comparison %s", r.CompareTo)
		}
	}
	return nil
}
