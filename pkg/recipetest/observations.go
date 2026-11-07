package recipetest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// ObservationConfig selects exact authored paths. Limits apply to a whole case,
// including child jobs. Identity counters are test-local, not JobDB native IDs.
type ObservationConfig struct {
	Scopes      []ScopeObservationSelector      `json:"scopes,omitempty"`
	Transitions []TransitionObservationSelector `json:"transitions,omitempty"`
	Entries     []EntryObservationSelector      `json:"entries,omitempty"`
	Limits      ObservationLimits               `json:"limits,omitempty"`
}
type ScopeObservationSelector struct {
	NodePath        string   `json:"node_path"`
	Cell            string   `json:"cell,omitempty"`
	Outputs         []string `json:"outputs"`
	RetainArtifacts []string `json:"retain_artifacts,omitempty"`
}
type TransitionObservationSelector struct {
	MachinePath string `json:"machine_path"`
	Cell        string `json:"cell,omitempty"`
	From        string `json:"from"`
	To          string `json:"to"`
}
type EntryObservationSelector struct {
	MachinePath string `json:"machine_path"`
	Cell        string `json:"cell,omitempty"`
	State       string `json:"state"`
}
type ObservationLimits struct {
	Records int `json:"records"`
	Bytes   int `json:"bytes"`
}
type ObservationCollection struct {
	Complete    bool              `json:"complete"`
	Limits      ObservationLimits `json:"limits"`
	Records     int               `json:"records"`
	Bytes       int               `json:"bytes"`
	Dropped     int               `json:"dropped"`
	Reasons     map[string]int    `json:"reasons"`
	Diagnostics []map[string]any  `json:"diagnostics"`
}
type ObservationArtifact struct {
	Name          string `json:"name"`
	ObservationID string `json:"observation_id"`
	Ref           any    `json:"ref"`
	Path          string `json:"path,omitempty"`
	Size          int    `json:"size_bytes"`
	Status        string `json:"status"`
}

func (c ObservationConfig) limits() ObservationLimits {
	l := c.Limits
	if l.Records == 0 {
		l.Records = 256
	}
	if l.Bytes == 0 {
		l.Bytes = 1048576
	}
	return l
}
func validateObservations(c *ObservationConfig) error {
	if c == nil {
		return nil
	}
	l := c.limits()
	if l.Records < 1 || l.Records > 4096 || l.Bytes < 1 || l.Bytes > 8*1024*1024 {
		return fmt.Errorf("observation limits require 1..4096 records and 1..8388608 bytes")
	}
	exact := func(s string) bool { return strings.TrimSpace(s) != "" && !strings.ContainsAny(s, "*?[]") }
	if len(c.Scopes)+len(c.Transitions)+len(c.Entries) == 0 {
		return fmt.Errorf("observation selectors must not be empty")
	}
	for _, s := range c.Scopes {
		if !exact(s.NodePath) || len(s.Outputs) == 0 {
			return fmt.Errorf("scope observation requires exact node_path and outputs")
		}
		for _, k := range s.Outputs {
			if k == "" {
				return fmt.Errorf("observation output must not be empty")
			}
		}
		for _, n := range s.RetainArtifacts {
			if _, err := fixturePath("", n); err != nil {
				return err
			}
		}
	}
	for _, s := range c.Transitions {
		if !exact(s.MachinePath) || !exact(s.From) || !exact(s.To) {
			return fmt.Errorf("transition observation requires exact machine_path, from and to")
		}
	}
	for _, s := range c.Entries {
		if !exact(s.MachinePath) || !exact(s.State) {
			return fmt.Errorf("entry observation requires exact machine_path and state")
		}
	}
	return nil
}

// observationScopeContext wraps only native test execution. Each parent counts
// calls per site, each scope counts callbacks (including catch callbacks). This
// full path is scoped by job_id and explicitly carries identity_kind=test_local.
// It is deterministic on replay but is not advertised as a JobDB inspection ID.
type observationIdentity struct {
	Path    string
	Parent  *observationIdentity
	Site    string
	Visit   int
	Attempt int
}
type observationContextKey struct{}
type observationScopeContext struct {
	jobworkflow.ExecutionContext
	identity *observationIdentity
	calls    map[string]int
}

func (c *observationScopeContext) Context() context.Context {
	return context.WithValue(c.ExecutionContext.Context(), observationContextKey{}, c.identity)
}
func (c *observationScopeContext) DoScope(site string, opts jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	c.calls[site]++
	visit := c.calls[site]
	attempt := 0
	wrap := func(exec jobworkflow.ExecutionContext) *observationScopeContext {
		attempt++
		id := &observationIdentity{Path: fmt.Sprintf("%s/scope:%q#%d/callback:%d", c.identity.Path, site, visit, attempt), Parent: c.identity, Site: site, Visit: visit, Attempt: attempt}
		return &observationScopeContext{exec, id, map[string]int{}}
	}
	opts.Catch = append([]jobworkflow.CatchRule(nil), opts.Catch...)
	for i := range opts.Catch {
		handle := opts.Catch[i].Handle
		opts.Catch[i].Handle = func(exec jobworkflow.ExecutionContext, err error) (jobworkflow.CatchResult, error) {
			return handle(wrap(exec), err)
		}
	}
	return c.ExecutionContext.DoScope(site, opts, func(exec jobworkflow.ExecutionContext) (jobdb.JobData, error) { return body(wrap(exec)) })
}

type observationExecutor struct {
	compiler.DefaultRecipeExecutor
	collector *observationCollector
}

func (e observationExecutor) ExecuteRecipe(ctx workflow.Context, r recipe.Recipe, in map[string]any, job contextual.JobContext, git contextual.GitCommitContext, opts ...compiler.ExecutionOptions) (map[string]any, []jobdb.Artifact, error) {
	key := ctx.GetJobKey()
	e.collector.mu.Lock()
	if e.collector.jobs[key.JobId] || len(e.collector.jobs) < e.collector.config.limits().Records {
		e.collector.jobs[key.JobId] = true
	} else {
		e.collector.omit(key.JobId, "job_limit")
	}
	e.collector.mu.Unlock()
	ctx.JobContext = &observationScopeContext{ctx.JobContext, &observationIdentity{Path: "$test_job", Visit: 1, Attempt: 1}, map[string]int{}}
	ctx.OnObservation = func(c context.Context, event workflow.Observation) { e.collector.observe(c, key, event) }
	return e.DefaultRecipeExecutor.ExecuteRecipe(ctx, r, in, job, git, opts...)
}

type artifactOpener interface {
	OpenArtifact(context.Context, jobdb.ArtifactRef) (jobdb.ArtifactReader, error)
}
type observationCollector struct {
	mu             sync.Mutex
	config         ObservationConfig
	caseID         string
	report         *RuntimeReport
	redactor       *secrets.Redactor
	artifacts      map[string][]byte
	opener         artifactOpener
	artifactLimit  int
	jobs           map[string]bool
	seen           map[string]bool
	recordIDs      map[string]string
	lastState      map[string]string
	lastTransition map[string]string
	order          int
}

func newObservationCollector(c ObservationConfig, caseID string, report *RuntimeReport, values map[string]string, opener artifactOpener, artifactLimit int) *observationCollector {
	r := secrets.NewRedactor()
	for _, v := range values {
		_ = r.Register(v)
	}
	report.ObservationsVersion = 1
	report.Observations = []map[string]any{}
	report.ObservationCollection = &ObservationCollection{Complete: true, Limits: c.limits(), Reasons: map[string]int{}, Diagnostics: []map[string]any{}}
	return &observationCollector{config: c, caseID: caseID, report: report, redactor: r, artifacts: map[string][]byte{}, opener: opener, artifactLimit: artifactLimit, jobs: map[string]bool{}, seen: map[string]bool{}, recordIDs: map[string]string{}, lastState: map[string]string{}, lastTransition: map[string]string{}}
}
func (c *observationCollector) omit(id, reason string) {
	s := c.report.ObservationCollection
	s.Complete = false
	s.Dropped++
	s.Reasons[reason]++
	if len(s.Diagnostics) < 16 {
		s.Diagnostics = append(s.Diagnostics, map[string]any{"id": boundedObservationID(c.redactor.Redact(id)), "reason": reason})
	}
}
func machineContext(scope *template.ResolutionContext) *template.ResolutionContext {
	for cur := scope; cur != nil; cur = cur.Parent {
		if cur.ScopeType == template.ScopeStateMachine {
			return cur
		}
	}
	return nil
}
func identityMachine(id *observationIdentity, machine *template.ResolutionContext) string {
	if machine == nil {
		return ""
	}
	path := machine.TaskExecutionContext().Invocation.NodePath
	name := path[strings.LastIndex(path, "/")+1:]
	for cur := id; cur != nil; cur = cur.Parent {
		if cur.Site == "state_machine:"+name {
			return cur.Path
		}
	}
	return ""
}
func (c *observationCollector) observe(ctx context.Context, key jobdb.JobKey, event workflow.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, _ := ctx.Value(observationContextKey{}).(*observationIdentity)
	if id == nil || event.Scope == nil {
		c.omit("", "identity_unavailable")
		return
	}
	exec := event.Scope.TaskExecutionContext()
	cell := exec.Workflow.CellName
	path := exec.Invocation.NodePath
	machine := machineContext(event.Scope)
	machinePath := ""
	if machine != nil {
		machinePath = machine.TaskExecutionContext().Invocation.NodePath
	}
	machineID := identityMachine(id, machine)
	if len(id.Path) > 8192 {
		c.omit(id.Path, "identity_limit")
		return
	}
	machineKey := key.JobId + "|" + machineID
	// Keep only bounded per-selected-machine linking state; selectors restrict it.
	selected := false
	var scopeSelector *ScopeObservationSelector
	for i := range c.config.Scopes {
		s := &c.config.Scopes[i]
		if event.Kind == "scope_completed" && s.NodePath == path && (s.Cell == "" || s.Cell == cell) {
			selected = true
			scopeSelector = s
			break
		}
	}
	transitionMachine := false
	for _, s := range c.config.Transitions {
		if s.MachinePath == machinePath && (s.Cell == "" || s.Cell == cell) {
			transitionMachine = true
			if (event.Kind == "transition_selected" || event.Kind == "route_selected") && s.From == event.Transition.From && s.To == event.Transition.To {
				selected = true
			}
		}
	}
	for _, s := range c.config.Entries {
		if event.Kind == "state_entered" && s.MachinePath == machinePath && s.State == event.Transition.To && (s.Cell == "" || s.Cell == cell) {
			selected = true
		}
	}
	// Link accepted states separately from retained event slots. A child
	// completion cannot supply the enclosing state's acceptance identity.
	stateCompletion := event.Kind == "scope_completed" && strings.HasPrefix(id.Site, "state:") && transitionMachine
	if stateCompletion {
		if _, exists := c.lastState[machineKey]; exists || len(c.lastState) < c.config.limits().Records {
			c.lastState[machineKey] = id.Path
		} else {
			c.omit(id.Path, "link_limit")
		}
	}
	if !selected && (event.Kind == "transition_selected" || event.Kind == "route_selected") {
		delete(c.lastTransition, machineKey)
	}
	if !selected {
		return
	}
	if machine != nil && machineID == "" {
		c.omit(id.Path, "machine_identity_unavailable")
		return
	}
	identity := key.JobId + "|" + id.Path + "|" + event.Kind
	if event.Kind == "transition_selected" || event.Kind == "route_selected" {
		identity += "|" + c.lastState[machineKey] + "|" + event.Transition.From + "|" + event.Transition.To
	}
	if c.seen[identity] {
		if recordID := c.recordIDs[identity]; recordID != "" && (event.Kind == "transition_selected" || event.Kind == "route_selected") {
			c.lastTransition[machineKey] = recordID
		}
		return
	}
	if len(c.seen) >= c.config.limits().Records {
		c.omit(identity, "record_limit")
		return
	}
	c.seen[identity] = true
	c.order++
	recordID := fmt.Sprintf("observation-%d", c.order)
	rec := map[string]any{"id": recordID, "order": c.order, "kind": event.Kind, "case_id": c.caseID, "job_id": key.JobId, "cell": cell, "identity_kind": "test_local_scope_callback", "scope_id": id.Path, "scope_visit": id.Visit, "callback_attempt": id.Attempt, "node_path": path, "invocation": exec.Invocation, "machine_path": machinePath, "machine_scope_id": machineID}
	if id.Parent != nil {
		rec["parent_scope_id"] = id.Parent.Path
	}
	rec["state_scope_id"] = ""
	for cur := id; cur != nil; cur = cur.Parent {
		if strings.HasPrefix(cur.Site, "state:") {
			rec["state_scope_id"] = cur.Path
			break
		}
	}
	if scopeSelector != nil {
		outputs := map[string]any{}
		presence := map[string]bool{}
		for _, k := range scopeSelector.Outputs {
			v, ok := event.Output.Outputs[k]
			presence[k] = ok
			if ok {
				outputs[k] = v
			}
		}
		rec["outputs"] = outputs
		rec["output_presence"] = presence
	}
	if event.Kind == "transition_selected" || event.Kind == "route_selected" {
		rec["from"] = event.Transition.From
		rec["to"] = event.Transition.To
		rec["payload"] = event.Transition.Payload
		rec["source_scope_id"] = c.lastState[machineKey]
		if event.Kind == "route_selected" {
			rec["source_scope_id"] = ""
			rec["failure"] = event.Transition.Failure
		}
	}
	if event.Kind == "state_entered" {
		rec["incoming_transition"] = event.Transition.AsMap()
		rec["transition_id"] = ""
		rec["source_scope_id"] = ""
		for _, previous := range c.report.Observations {
			if previous["id"] == c.lastTransition[machineKey] && previous["from"] == event.Transition.From && previous["to"] == event.Transition.To {
				rec["transition_id"] = previous["id"]
				rec["source_scope_id"] = previous["source_scope_id"]
			}
		}
	}
	if !observationValueFits(reflect.ValueOf(rec), 256*1024, 0) {
		c.omit(identity, "event_limit")
		return
	}
	var snapshot map[string]any
	if err := snapshotRuntimeValue(ctx, rec, &snapshot); err != nil {
		c.omit(identity, "snapshot_error")
		return
	}
	// Composite outputs may contain fixture secret values outside a task session.
	// Use the same redactor rules, seeded only with this case's synthetic secrets.
	snapshot = redactObservation(snapshot, c.redactor).(map[string]any)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		c.omit(identity, "snapshot_error")
		return
	}
	status := c.report.ObservationCollection
	if len(raw) > 256*1024 {
		c.omit(identity, "event_limit")
		return
	}
	if status.Bytes+len(raw) > status.Limits.Bytes {
		c.omit(identity, "byte_limit")
		return
	}
	c.report.Observations = append(c.report.Observations, snapshot)
	status.Bytes += len(raw)
	status.Records++
	c.recordIDs[identity] = recordID
	if event.Kind == "transition_selected" || event.Kind == "route_selected" {
		c.lastTransition[machineKey] = recordID
	}
	if scopeSelector != nil {
		for _, name := range scopeSelector.RetainArtifacts {
			c.retain(ctx, key, recordID, name, event.Output)
		}
	}
}
func redactObservation(v any, r *secrets.Redactor) any {
	switch x := v.(type) {
	case string:
		return r.Redact(x)
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			out[r.Redact(k)] = redactObservation(v, r)
		}
		return out
	case []any:
		for i := range x {
			x[i] = redactObservation(x[i], r)
		}
		return x
	default:
		return v
	}
}
func (c *observationCollector) retain(ctx context.Context, key jobdb.JobKey, id, name string, out template.ExecutionOutput) {
	ref, ok := out.ArtifactRefs[name]
	safeName := c.redactor.Redact(secrets.FromContext(ctx).Redact(name))
	manifest := ObservationArtifact{Name: safeName, ObservationID: id, Status: "unavailable"}
	if !observationValueFits(reflect.ValueOf(ref), 256*1024, 0) {
		c.omit(id, "artifact_limit")
		return
	}
	var normalized any
	_ = snapshotRuntimeValue(ctx, ref, &normalized)
	manifest.Ref = redactObservation(normalized, c.redactor)
	data := []byte(nil)
	var err error
	if !ok {
		err = fmt.Errorf("missing ref")
	} else {
		switch ref.Kind {
		case recipeartifacts.RefKindValue:
			data, err = ref.ValueBytes()
		case recipeartifacts.RefKindStored:
			k, valid := ref.StoredKey()
			if !valid || !c.jobs[k.JobId] || k.SizeBytes < 0 || k.SizeBytes > int64(c.artifactLimit) {
				err = fmt.Errorf("unavailable stored artifact")
				break
			}
			var reader jobdb.ArtifactReader
			reader, err = c.opener.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: key.TenantId, JobId: k.JobId}, Ordinal: k.TaskOrdinal, Name: k.Name})
			if err == nil {
				stream, openErr := reader.Open()
				if openErr != nil {
					err = openErr
				} else {
					data, err = io.ReadAll(io.LimitReader(stream, int64(c.artifactLimit)+1))
					_ = stream.Close()
				}
			}
		default:
			err = fmt.Errorf("unsupported artifact kind")
		}
	}
	if err != nil {
		c.omit(id, "artifact_unavailable")
	} else if len(data) > c.artifactLimit {
		manifest.Status = "omitted"
		c.omit(id, "artifact_limit")
	} else {
		data = []byte(c.redactor.Redact(secrets.FromContext(ctx).Redact(string(data))))
		path := "observations/" + id + "/" + name
		if _, e := fixturePath("", path); e != nil {
			manifest.Status = "omitted"
			c.omit(id, "artifact_path")
		} else {
			// Use one opaque namespace for every binding; literal names must
			// never overlap generated keys. Refuse even a digest collision or
			// duplicate selection rather than silently replacing retained bytes.
			path = fmt.Sprintf("observations/%s/artifact-%x", id, sha256.Sum256([]byte(name)))
			if _, exists := c.artifacts[path]; exists {
				manifest.Status = "omitted"
				c.omit(id, "artifact_collision")
				data = nil
			} else {
				manifest.Path = "artifacts/" + path
				manifest.Size = len(data)
				manifest.Status = "retained"
			}
		}
	}
	raw, _ := json.Marshal(manifest)
	status := c.report.ObservationCollection
	if status.Bytes+len(raw)+len(data) > status.Limits.Bytes {
		c.omit(id, "artifact_byte_limit")
		return
	}
	status.Bytes += len(raw)
	if manifest.Status == "retained" {
		c.artifacts[strings.TrimPrefix(manifest.Path, "artifacts/")] = data
		status.Bytes += len(data)
	}
	c.report.ObservationArtifacts = append(c.report.ObservationArtifacts, manifest)
}

// Reject obviously oversized/deep inputs before JSON normalization allocates a
// second copy. The encoded byte check remains authoritative for retained bytes.
func observationValueFits(v reflect.Value, budget, depth int) bool {
	_, ok := observationValueSize(v, budget, depth)
	return ok
}
func observationValueSize(v reflect.Value, budget, depth int) (int, bool) {
	if budget < 0 || depth > 64 {
		return 0, false
	}
	if !v.IsValid() {
		return 4, budget >= 4
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return 4, budget >= 4
		}
		return observationValueSize(v.Elem(), budget, depth+1)
	case reflect.String:
		n := v.Len()
		return n, n <= budget
	case reflect.Map:
		total := 2
		iter := v.MapRange()
		for iter.Next() {
			n, ok := observationValueSize(iter.Key(), budget-total, depth+1)
			if !ok {
				return 0, false
			}
			total += n
			n, ok = observationValueSize(iter.Value(), budget-total, depth+1)
			if !ok {
				return 0, false
			}
			total += n + 1
		}
		return total, total <= budget
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return v.Len(), v.Len() <= budget
		}
		total := 2
		for i := 0; i < v.Len(); i++ {
			n, ok := observationValueSize(v.Index(i), budget-total, depth+1)
			if !ok {
				return 0, false
			}
			total += n + 1
		}
		return total, total <= budget
	case reflect.Struct:
		total := 2
		for i := 0; i < v.NumField(); i++ {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			n, ok := observationValueSize(v.Field(i), budget-total, depth+1)
			if !ok {
				return 0, false
			}
			total += n
		}
		return total, total <= budget
	default:
		return 1, budget >= 1
	}
}

func boundedObservationID(id string) string {
	if len(id) > 1024 {
		return id[:1024] + "...[identity truncated]"
	}
	return id
}
