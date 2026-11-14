package jobinspect

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Service struct {
	Runtime jobdb.WorkflowRuntime
	Tenant  string
	// Authorize guards detail/byte reads and discovery. Discovery passes the
	// selected parent, or an empty ID for an unfiltered list. The task runtime
	// checks operation and lease lifetime; JobDB controls read permissions.
	Authorize func(context.Context, string) error
	// Parent is the default discovery filter, overridden by Request.Parent.
	// It is not an authorization boundary.
	Parent string
}

type activity struct {
	Output          json.RawMessage             `json:"output"`
	Git             contextual.GitCommitContext `json:"git"`
	WorkspaceBefore *coretask.WorkspaceState    `json:"workspace_before"`
	WorkspaceAfter  *coretask.WorkspaceState    `json:"workspace_after"`
	Scope           string                      `json:"workspace_scope_id"`
	Refs            map[string]artifacts.Ref    `json:"artifact_refs"`
}
type invocation struct {
	Git     gitstate.GlobalGitTaskContext `json:"context"`
	Restore *jobdb.ArtifactKey            `json:"restore_artifact"`
}
type attempt struct {
	task           Task
	in             invocation
	activity       activity
	raw            json.RawMessage
	inputArtifacts []Artifact
	order          int
}
type history struct {
	run             jobdb.GetJobRunResponse
	job             Job
	tasks           []string
	attempts        map[string][]*attempt
	scopes          []Scope
	resultRaw       json.RawMessage
	resultArtifacts []Artifact
	warnings        []string
}

func (s *Service) load(ctx context.Context, id string) (*history, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("job ID is required")
	}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, id); err != nil {
			return nil, err
		}
	}
	run, err := jobdb.GetJobRun(ctx, s.Runtime, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: s.Tenant, JobId: id}, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true, IncludeAttemptInputs: true})
	if err != nil {
		return nil, err
	}
	h := &history{run: run, attempts: map[string][]*attempt{}}
	var meta starter.JobMetadata
	_ = json.Unmarshal(run.Job.Metadata, &meta)
	h.job = Job{ID: id, Tenant: s.Tenant, Goal: run.Job.GoalID, Parent: meta.ParentJobID, Status: string(run.Job.Status), CompletionStatus: run.Job.CompletionStatus, CompletionDetail: run.Job.CompletionDetail, CreatedAt: run.Job.CreatedAt}
	if page, e := s.Runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{s.Tenant}, JobKeys: []jobdb.JobKey{run.Job.JobKey}, IncludeHidden: true, PageSize: 1}); e == nil && len(page.Jobs) == 1 {
		h.job.Cell = page.Jobs[0].Cell
		h.job.Parent = page.Jobs[0].ParentJobID
	}
	// Use JobDB's persisted execution projection; no worker or recipe is loaded.
	projected, err := swfutil.ExecutionTasks(run)
	if err != nil {
		return nil, err
	}
	resultRecords := map[string]jobdb.ExecutionRecord{}
	startRecords := map[string]jobdb.ExecutionRecord{}
	resultOrder := map[string]int{}
	scopeIndex := map[string]int{}
	for i, e := range run.Execution {
		r := e.Record
		switch r.Kind {
		case "attempt":
			startRecords[r.ID] = r
		case "result":
			resultRecords[r.ID] = r
			resultOrder[r.ID] = i
			if split := strings.LastIndex(r.ID, "/attempt:"); split >= 0 {
				if j, ok := scopeIndex[r.ID[:split]]; ok {
					h.scopes[j].Status = "succeeded"
					if r.ErrorKind != "" {
						h.scopes[j].Status = "failed"
					}
				}
			}
		case "scope":
			var d struct {
				Parent string `json:"parent"`
				Call   struct {
					Site string `json:"site"`
				} `json:"call"`
			}
			_ = json.Unmarshal(r.Descriptor, &d)
			scopeIndex[r.ID] = len(h.scopes)
			h.scopes = append(h.scopes, Scope{ID: r.ID, Parent: d.Parent, Site: d.Call.Site, Status: "completion_not_recorded"})
		case "end":
			if j, ok := scopeIndex[r.ID]; ok {
				h.scopes[j].Status = "succeeded"
				if r.ErrorKind != "" {
					h.scopes[j].Status = "failed"
				}
			}
		}
	}
	for _, p := range projected {
		h.tasks = append(h.tasks, p.InvocationID)
		for _, a := range p.Attempts {
			key := p.InvocationID + "/attempt:" + strconv.Itoa(a.Attempt)
			x := &attempt{task: Task{ID: p.InvocationID, Type: p.TaskType, Attempt: a.Attempt, Status: strings.ToLower(a.State)}, order: resultOrder[key]}
			if a.Input != nil {
				_ = json.Unmarshal(a.Input.Data, &x.in)
				x.inputArtifacts = artifactInfos(a.Input.Artifacts, p.InvocationID, a.Attempt)
			}
			if a.Output != nil {
				x.raw, x.activity, err = decodeOutput(a.Output.Data)
				if err != nil {
					h.warnings = append(h.warnings, p.InvocationID+": "+err.Error())
				}
				x.task.Artifacts = artifactInfos(a.Output.Artifacts, p.InvocationID, a.Attempt)
				x.task.Artifacts = mergeRefs(x.task.Artifacts, x.activity.Refs, p.InvocationID, a.Attempt)
			}
			if r, ok := resultRecords[key]; ok {
				at := r.At
				x.task.FinishedAt = &at
				x.task.Error = compactError(r.Error)
			}
			if r, ok := startRecords[key]; ok {
				at := r.At
				x.task.StartedAt = &at
			}
			x.finish()
			h.attempts[p.InvocationID] = append(h.attempts[p.InvocationID], x)
		}
		// Include the live attempt using its durable identity, before a result exists.
		for key, r := range startRecords {
			if !strings.HasPrefix(key, p.InvocationID+"/attempt:") || strings.Contains(strings.TrimPrefix(key, p.InvocationID+"/attempt:"), "/") {
				continue
			}
			if _, done := resultRecords[key]; done {
				continue
			}
			at := r.At
			x := &attempt{task: Task{ID: p.InvocationID, Type: p.TaskType, Attempt: r.Attempt, Status: "running", StartedAt: &at}, order: len(run.Execution)}
			if r.Input != nil {
				_ = json.Unmarshal(r.Input.Data, &x.in)
			}
			x.finish()
			h.attempts[p.InvocationID] = append(h.attempts[p.InvocationID], x)
		}
		if len(h.attempts[p.InvocationID]) == 0 {
			x := &attempt{task: Task{ID: p.InvocationID, Type: p.TaskType, Status: "pending"}, order: len(run.Execution)}
			x.finish()
			h.attempts[p.InvocationID] = []*attempt{x}
		}
		sort.Slice(h.attempts[p.InvocationID], func(i, j int) bool {
			return h.attempts[p.InvocationID][i].task.Attempt < h.attempts[p.InvocationID][j].task.Attempt
		})
	}
	// Legacy stored tasks remain inspectable using the native TaskRunID.
	if len(run.Execution) == 0 {
		for _, ja := range run.Attempts {
			for _, p := range ja.Tasks {
				h.tasks = append(h.tasks, p.TaskRunID)
				for _, a := range p.Attempts {
					x := &attempt{task: Task{ID: p.TaskRunID, Type: p.TaskType, Attempt: a.Attempt, Status: strings.ToLower(a.State)}, order: int(a.Ordinal)}
					if a.Input != nil {
						_ = json.Unmarshal(a.Input.Data, &x.in)
						x.inputArtifacts = artifactInfos(a.Input.Artifacts, p.TaskRunID, a.Attempt)
					}
					if a.Output != nil {
						x.raw, x.activity, err = decodeOutput(a.Output.Data)
						if err != nil {
							h.warnings = append(h.warnings, err.Error())
						}
						x.task.Artifacts = artifactInfos(a.Output.Artifacts, p.TaskRunID, a.Attempt)
					}
					x.finish()
					h.attempts[p.TaskRunID] = append(h.attempts[p.TaskRunID], x)
				}
			}
		}
	}
	if run.Job.ArchivedAt == nil && run.Job.Status != jobdb.JobStatusCancelled {
		h.warnings = append(h.warnings, "Running tasks appear when their occurrence is durably recorded; the latest in-process work may not yet be visible.")
	}
	if len(run.Attempts) > 0 {
		a := run.Attempts[len(run.Attempts)-1]
		if a.Output != nil && a.Outcome.Status == jobdb.TaskOutcomeStatusSucceeded {
			h.resultRaw = a.Output.Data
			h.resultArtifacts = artifactInfos(a.Output.Artifacts, "", a.Attempt)
			if run.Job.JobType == starter.RecipeJobType {
				var result struct {
					Refs map[string]artifacts.Ref `json:"artifact_refs"`
				}
				if json.Unmarshal(a.Output.Data, &result) == nil {
					h.resultArtifacts = mergeRefs(h.resultArtifacts, result.Refs, "", a.Attempt)
				}
			}
		}
	}
	return h, nil
}

func decodeOutput(raw json.RawMessage) (json.RawMessage, activity, error) {
	var env coretask.OutputEnvelope
	if json.Unmarshal(raw, &env) != nil || env.Kind == "" {
		return raw, activity{}, nil
	}
	// Other job types can legitimately return a field named "kind". Only a
	// versioned envelope belongs to the c2j task-output protocol.
	if env.Version == 0 {
		var object map[string]json.RawMessage
		_ = json.Unmarshal(raw, &object)
		if _, versioned := object["v"]; !versioned {
			return raw, activity{}, nil
		}
	}
	if env.Version != coretask.OutputEnvelopeVersion || env.Kind != coretask.OutputKindActivityInvocationOutput {
		return nil, activity{}, fmt.Errorf("unsupported stored task output %q version %d", env.Kind, env.Version)
	}
	var a activity
	if err := env.DecodePayload(&a); err != nil {
		return nil, a, err
	}
	return a.Output, a, nil
}
func (a *attempt) finish() {
	a.task.Path = a.in.Git.NodePath
	a.task.Cell = a.in.Git.GetWorkspaceCellName()
	a.task.Workspace = a.activity.Scope
	if a.task.Workspace == "" && a.in.Git.Workspace != nil {
		a.task.Workspace = a.in.Git.Workspace.ScopeID
	}
	if a.activity.WorkspaceBefore != nil {
		a.task.Cell = a.activity.WorkspaceBefore.Cell
		a.task.Workspace = a.activity.WorkspaceBefore.ScopeID
	}
	a.task.OutputAvailability = "not_produced"
	if a.task.Status == "running" || a.task.Status == "pending" || a.task.Status == "leased" || a.task.Status == "ready" || a.task.Status == "waiting" {
		a.task.OutputAvailability = "pending"
	}
	if len(a.raw) > 0 {
		a.task.OutputAvailability = "available"
	}
	a.task.Fields = fields(a.raw)
}
func (h *history) selected(id string, n int) (*attempt, error) {
	as := h.attempts[id]
	if len(as) == 0 {
		return nil, fmt.Errorf("task %q not found; use job log to obtain its exact ID", id)
	}
	if n == 0 {
		return as[len(as)-1], nil
	}
	for _, a := range as {
		if a.task.Attempt == n {
			return a, nil
		}
	}
	return nil, fmt.Errorf("task %q attempt %d not found", id, n)
}
func (s *Service) Inspect(ctx context.Context, q Request) (*Response, error) {
	if q.Operation == "family-node" || q.Operation == "family-children" {
		return s.family(ctx, q)
	}
	if q.Family != nil {
		return nil, fmt.Errorf("family options require a family operation")
	}
	if q.Operation == "events" {
		page, err := s.ListJobLeaseEvents(ctx, q.JobID, q.Events)
		if err != nil {
			return nil, err
		}
		return &Response{Version: Version, LeaseEvents: &page}, nil
	}
	if q.Limit == 0 {
		q.Limit = 25
	}
	if q.Limit < 1 || q.Limit > 100 {
		return nil, fmt.Errorf("limit must be between 1 and 100")
	}
	if q.Attempt < 0 {
		return nil, fmt.Errorf("attempt must be positive")
	}
	if q.Attempt != 0 && q.Task == "" {
		return nil, fmt.Errorf("attempt selection requires a task")
	}
	if q.Operation == "list" {
		return s.list(ctx, q)
	}
	h, err := s.load(ctx, q.JobID)
	if err != nil {
		return nil, err
	}
	r := &Response{Version: Version, Job: &h.job, Warnings: h.warnings}
	var a *attempt
	if q.Task != "" {
		a, err = h.selected(q.Task, q.Attempt)
		if err != nil {
			return nil, err
		}
	}
	if q.Field != nil {
		raw := h.resultRaw
		if a != nil {
			raw = a.raw
		}
		if q.Operation != "log" && q.Operation != "result" {
			return nil, fmt.Errorf("field selection requires log or result")
		}
		if q.Operation == "log" && a == nil {
			return nil, fmt.Errorf("field selection requires --task")
		}
		r.Value, err = Pointer(raw, *q.Field)
		return r, err
	}
	switch q.Operation {
	case "show":
		return r, nil
	case "log":
		if a != nil {
			v := h.publicTask(a)
			r.Task = &v
			return r, nil
		}
		start, end, next, e := page(q, h.tasks)
		if e != nil {
			return nil, e
		}
		r.NextCursor = next
		for _, id := range h.tasks[start:end] {
			a, e := h.selected(id, 0)
			if e != nil {
				continue
			}
			r.Tasks = append(r.Tasks, h.publicTask(a))
		}
	case "story":
		ids := make([]string, len(h.scopes))
		for i, sc := range h.scopes {
			ids[i] = sc.ID
		}
		start, end, next, e := page(q, ids)
		if e != nil {
			return nil, e
		}
		r.Scopes = h.scopes[start:end]
		r.NextCursor = next
	case "result":
		availability := "not_produced"
		if len(h.resultRaw) > 0 {
			availability = "available"
		} else if h.run.Job.ArchivedAt == nil && h.run.Job.Status != jobdb.JobStatusCancelled {
			availability = "pending"
		}
		r.Result = &Result{Availability: availability, Fields: fields(h.resultRaw), Artifacts: h.resultArtifacts}
		r.Result.Fields, r.Result.Artifacts, r.Result.InventoryTruncated = boundInventory(r.Result.Fields, r.Result.Artifacts)
	case "fields":
		raw := h.resultRaw
		if a != nil {
			raw = a.raw
		}
		fs := fields(raw)
		ids := make([]string, len(fs))
		for i := range fs {
			ids[i] = fs[i].Pointer
		}
		start, end, next, e := page(q, ids)
		if e != nil {
			return nil, e
		}
		r.Fields, r.NextCursor = fs[start:end], next
	case "artifacts":
		arts := h.resultArtifacts
		if a != nil {
			arts = a.task.Artifacts
		}
		ids := make([]string, len(arts))
		for i := range arts {
			ids[i] = strconv.Itoa(i) + ":" + arts[i].Name
		}
		start, end, next, e := page(q, ids)
		if e != nil {
			return nil, e
		}
		r.Artifacts = arts[start:end]
		r.NextCursor = next
	case "snapshot":
		r.Snapshot, err = s.snapshot(ctx, h, q, a)
	default:
		return nil, fmt.Errorf("unknown inspection operation %q", q.Operation)
	}
	return r, err
}
func (h *history) publicTask(a *attempt) Task {
	v := a.task
	for _, x := range h.attempts[v.ID] {
		v.Attempts = append(v.Attempts, x.task.Attempt)
	}
	v.Fields, v.Artifacts, v.InventoryTruncated = boundInventory(v.Fields, v.Artifacts)
	return v
}
func boundInventory(f []Field, a []Artifact) ([]Field, []Artifact, bool) {
	truncated := len(f) > 25 || len(a) > 25
	if len(f) > 25 {
		f = f[:25]
	}
	if len(a) > 25 {
		a = a[:25]
	}
	return f, a, truncated
}
func page(q Request, ids []string) (int, int, string, error) {
	start := 0
	if q.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil {
			return 0, 0, "", fmt.Errorf("invalid cursor")
		}
		var c struct {
			Job, Operation, Task, Last string
			Attempt                    int
		}
		if json.Unmarshal(raw, &c) != nil || c.Job != q.JobID || c.Operation != q.Operation || c.Task != q.Task || c.Attempt != q.Attempt {
			return 0, 0, "", fmt.Errorf("cursor does not match request")
		}
		found := false
		for i, id := range ids {
			if id == c.Last {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return 0, 0, "", fmt.Errorf("cursor is no longer available")
		}
	}
	end := min(start+q.Limit, len(ids))
	next := ""
	if end < len(ids) {
		raw, _ := json.Marshal(struct {
			Job, Operation, Task, Last string
			Attempt                    int
		}{q.JobID, q.Operation, q.Task, ids[end-1], q.Attempt})
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return start, end, next, nil
}
func fields(raw json.RawMessage) []Field {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return []Field{field("", raw)}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Field, 0, len(keys))
	for _, k := range keys {
		out = append(out, field("/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), m[k]))
	}
	return out
}
func field(p string, raw json.RawMessage) Field {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	_ = d.Decode(&v)
	typ := "null"
	switch v.(type) {
	case string:
		typ = "string"
	case bool:
		typ = "boolean"
	case json.Number:
		typ = "number"
	case []any:
		typ = "array"
	case map[string]any:
		typ = "object"
	}
	preview := string(raw)
	truncated := len(preview) > 160
	if truncated {
		preview = string([]rune(preview)[:min(160, len([]rune(preview)))]) + "…"
	}
	return Field{Pointer: p, Type: typ, Bytes: len(raw), Preview: preview, Truncated: truncated}
}
func Pointer(raw json.RawMessage, p string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("output not produced")
	}
	if p == "" {
		return raw, nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("field must be a JSON Pointer starting with /")
	}
	for _, key := range strings.Split(p[1:], "/") {
		for i := 0; i < len(key); i++ {
			if key[i] == '~' {
				if i+1 >= len(key) || (key[i+1] != '0' && key[i+1] != '1') {
					return nil, fmt.Errorf("invalid JSON Pointer escape")
				}
				i++
			}
		}
		key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil && obj != nil {
			var ok bool
			raw, ok = obj[key]
			if !ok {
				return nil, fmt.Errorf("field %q not found", p)
			}
			continue
		}
		var array []json.RawMessage
		if json.Unmarshal(raw, &array) == nil && array != nil {
			n, e := strconv.Atoi(key)
			if e != nil || n < 0 || n >= len(array) || strconv.Itoa(n) != key {
				return nil, fmt.Errorf("field %q not found", p)
			}
			raw = array[n]
			continue
		}
		return nil, fmt.Errorf("field %q not found", p)
	}
	return raw, nil
}
func compactError(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v struct {
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	if len(v.Message) > 2048 {
		v.Message = v.Message[:2048] + "…"
	}
	b, _ := json.Marshal(v)
	return b
}
func artifactInfos(in []jobdb.ArtifactInfo, task string, n int) []Artifact {
	var out []Artifact
	for _, a := range in {
		v := Artifact{Name: a.Name, Task: task, Attempt: n, Size: a.SizeBytes, Availability: "unavailable"}
		if a.Key != nil {
			v.Reference = artifacts.NewStoredRef(*a.Key)
			v.Availability = "stored"
		}
		out = append(out, v)
	}
	return out
}
func mergeRefs(in []Artifact, refs map[string]artifacts.Ref, task string, n int) []Artifact {
	names := map[string]bool{}
	for _, a := range in {
		names[a.Name] = true
	}
	keys := make([]string, 0, len(refs))
	for name := range refs {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if names[name] {
			continue
		}
		ref := refs[name]
		v := Artifact{Name: name, Task: task, Attempt: n, Reference: ref, Size: -1, Availability: "external"}
		if ref.Stored != nil {
			v.Size = ref.Stored.Key.SizeBytes
			v.Availability = "stored"
		}
		if ref.Kind == artifacts.RefKindValue {
			v.Availability = "available"
		}
		in = append(in, v)
	}
	return in
}
func (s *Service) Download(ctx context.Context, q Request) (io.ReadCloser, error) {
	h, err := s.load(ctx, q.JobID)
	if err != nil {
		return nil, err
	}
	var candidates []Artifact
	if q.Reference != nil {
		candidates = append(candidates, h.resultArtifacts...)
		for _, as := range h.attempts {
			for _, a := range as {
				candidates = append(candidates, a.task.Artifacts...)
				candidates = append(candidates, a.inputArtifacts...)
				if a.in.Restore != nil {
					candidates = append(candidates, Artifact{Reference: artifacts.NewStoredRef(*a.in.Restore)})
				}
			}
		}
	} else if q.Task != "" {
		a, e := h.selected(q.Task, q.Attempt)
		if e != nil {
			return nil, e
		}
		candidates = a.task.Artifacts
	} else {
		candidates = h.resultArtifacts
	}
	var match *Artifact
	for i := range candidates {
		a := &candidates[i]
		ok := a.Name == q.Name
		if q.Reference != nil {
			ok = a.Reference.Stored != nil && sameKey(a.Reference.Stored.Key, *q.Reference)
		}
		if !ok {
			continue
		}
		if match != nil && q.Reference == nil && !sameRef(match.Reference, a.Reference) {
			return nil, fmt.Errorf("artifact name %q is ambiguous; use its reference", q.Name)
		}
		match = a
	}
	if match == nil {
		return nil, fmt.Errorf("artifact not found in the selected job/task")
	}
	if match.Reference.Kind == artifacts.RefKindValue {
		b, e := match.Reference.ValueBytes()
		return io.NopCloser(bytes.NewReader(b)), e
	}
	if match.Reference.Stored == nil {
		return nil, fmt.Errorf("artifact %q is %s; no stored bytes available", match.Name, match.Availability)
	}
	k := match.Reference.Stored.Key
	if s.Authorize != nil && k.JobId != q.JobID {
		if err := s.Authorize(ctx, k.JobId); err != nil {
			return nil, err
		}
	}
	// A durable reference carried by an authorized outcome is provenance, not an
	// arbitrary cross-job key supplied by the caller. Runtime policy still applies.
	a, e := s.Runtime.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: s.Tenant, JobId: k.JobId}, Ordinal: k.TaskOrdinal, Name: k.Name})
	if e != nil {
		return nil, e
	}
	return a.Open()
}
func sameKey(a, b jobdb.ArtifactKey) bool {
	return a.JobId == b.JobId && a.TaskOrdinal == b.TaskOrdinal && a.Name == b.Name
}
func sameRef(a, b artifacts.Ref) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func (s *Service) list(ctx context.Context, q Request) (*Response, error) {
	parent := q.Parent
	if parent == "" {
		parent = s.Parent
	}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, parent); err != nil {
			return nil, err
		}
	}
	req := jobdb.ListJobsRequest{TenantIds: []string{s.Tenant}, PageSize: q.Limit, PageToken: q.Cursor, Statuses: q.Status}
	if parent != "" {
		req.ParentJobIDs = []string{parent}
	}
	page, err := s.Runtime.ListJobs(ctx, req)
	if err != nil {
		return nil, err
	}
	r := &Response{Version: Version, NextCursor: page.NextPageToken, Jobs: []Job{}}
	for _, j := range page.Jobs {
		r.Jobs = append(r.Jobs, Job{ID: j.JobKey.JobId, Tenant: j.JobKey.TenantId, Cell: j.Cell, Goal: j.GoalID, Parent: j.ParentJobID, Status: string(j.Status), CompletionStatus: j.CompletionStatus, CreatedAt: j.CreatedAt})
	}
	return r, nil
}
