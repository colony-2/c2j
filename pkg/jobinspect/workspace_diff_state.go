package jobinspect

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Positions are logical execution positions, not chapter ordinals. Several
// reconstructed outcomes may occupy the same physical chapter.
type diffAttempt struct {
	*attempt
	start, end int
	contract   int
	command    json.RawMessage
	input      json.RawMessage
	ordinal    int64
}
type diffScope struct {
	id, parent string
	n          int
	start, end int
	status     string
}
type diffState struct {
	w      coretask.WorkspaceState
	a      *diffAttempt
	before bool
	cut    int
	issue  string
}
type diffHistory struct {
	h                *history
	tasks            []*diffAttempt
	scopes           map[string]map[int]*diffScope
	mainRepo, mainID string
	mainIssue        bool
}

// splitNativePath respects slashes and escaped quotes inside JSON-quoted sites.
// It does not reinterpret node paths as native occurrence identities.
func splitNativePath(id string) []string {
	var parts []string
	quoted, escaped, start := false, false, 0
	for i, c := range id {
		if escaped {
			escaped = false
			continue
		}
		if quoted && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			quoted = !quoted
		}
		if c == '/' && !quoted {
			if i > start {
				parts = append(parts, id[start:i])
			}
			start = i + 1
		}
	}
	if start < len(id) {
		parts = append(parts, id[start:])
	}
	return parts
}

func newDiffHistory(h *history) *diffHistory {
	d := &diffHistory{h: h, scopes: map[string]map[int]*diffScope{}}
	var mainErr error
	d.mainRepo, d.mainID, mainErr = mainWorkspace(h, "")
	d.mainIssue = mainErr != nil
	// Cell resolution is itself a persisted internal task. For legacy unscoped
	// submissions it is the compiler's authoritative owning repository, which
	// may differ from the caller's alias/local source URL.
	var submitted struct {
		Context struct {
			Workspace *json.RawMessage `json:"workspace"`
		} `json:"context"`
	}
	if h.run.Start.Input != nil {
		_ = json.Unmarshal(h.run.Start.Input.Data, &submitted)
	}
	if submitted.Context.Workspace == nil {
		for _, as := range h.attempts {
			for _, a := range as {
				if a.task.Type == "recipe_cell_context" && a.task.Status == "succeeded" && len(splitNativePath(a.task.ID)) == 3 {
					var result struct {
						Repository string `json:"cellRepo"`
					}
					if json.Unmarshal(a.raw, &result) == nil && result.Repository != "" {
						d.mainRepo = result.Repository
					}
				}
			}
		}
	}
	if submitted.Context.Workspace == nil {
		for _, as := range h.attempts {
			for _, a := range as {
				if a.task.Type == "recipe_workspace_resolve" && a.task.Status == "succeeded" && len(splitNativePath(a.task.ID)) == 3 {
					var resolved struct {
						Workspace contextual.WorkspaceContext `json:"workspace"`
						Git       contextual.GitBaseContext   `json:"git"`
					}
					if json.Unmarshal(a.raw, &resolved) == nil && resolved.Workspace.ScopeID != "" && resolved.Workspace.ParentScopeID == "" {
						d.mainRepo = resolved.Git.BaseRepo
						d.mainID = resolved.Workspace.ScopeID
						d.mainIssue = d.mainRepo == ""
					}
				}
			}
		}
	}
	decl := map[string]jobdb.ExecutionRecord{}
	starts, ends := map[string]int{}, map[string]int{}
	for i, e := range h.run.Execution {
		r := e.Record
		if r.Kind == "task" {
			decl[r.ID] = r
		}
		if r.Kind == "attempt" || r.Kind == "wait" {
			if _, ok := starts[r.ID]; !ok {
				starts[r.ID] = i
			}
		}
		if r.Kind == "result" {
			ends[r.ID] = i
		}
		parts := splitNativePath(r.ID)
		for j, p := range parts {
			if !strings.HasPrefix(p, "scope:") {
				continue
			}
			id := "/" + strings.Join(parts[:j+1], "/")
			n := 0
			if j+1 < len(parts) && strings.HasPrefix(parts[j+1], "attempt:") {
				n, _ = strconv.Atoi(strings.TrimPrefix(parts[j+1], "attempt:"))
			}
			if d.scopes[id] == nil {
				d.scopes[id] = map[int]*diffScope{}
			}
			if n == 0 {
				continue
			}
			sc := d.scopes[id][n]
			if sc == nil {
				parent := ""
				if j > 0 {
					parent = "/" + strings.Join(parts[:j], "/")
				}
				sc = &diffScope{id: id, parent: parent, n: n, start: i, end: -1, status: "in_progress"}
				d.scopes[id][n] = sc
			}
			if r.ID == id+"/attempt:"+strconv.Itoa(n) && r.Kind == "result" {
				sc.end = i
				sc.status = "succeeded"
				if r.ErrorKind != "" {
					sc.status = "failed"
				}
			}
		}
	}
	changesWaitingInputs(h)
	for _, id := range h.tasks {
		for _, a := range h.attempts[id] {
			key := id + "/attempt:" + strconv.Itoa(a.task.Attempt)
			x := &diffAttempt{attempt: a, start: -1, end: -1}
			if i, ok := starts[key]; ok {
				x.start = i
			}
			if i, ok := ends[key]; ok {
				x.end = i
				x.ordinal = h.run.Execution[i].Ordinal
				if h.run.Execution[i].Record.Input != nil {
					x.input = h.run.Execution[i].Record.Input.Data
				}
			}
			if x.input == nil && x.start >= 0 && h.run.Execution[x.start].Record.Input != nil {
				x.input = h.run.Execution[x.start].Record.Input.Data
			}
			var desc struct {
				Call struct {
					Contract int             `json:"contract_version"`
					Command  json.RawMessage `json:"command"`
				} `json:"call"`
			}
			_ = json.Unmarshal(decl[id].Descriptor, &desc)
			x.contract = desc.Call.Contract
			x.command = desc.Call.Command
			d.tasks = append(d.tasks, x)
		}
	}
	sort.Slice(d.tasks, func(i, j int) bool { return d.tasks[i].end < d.tasks[j].end })
	// Compact organizational scopes omit completion records. Bound their
	// progress by the first recorded continuation outside this attempt; do not
	// label that evidence as a recorded successful scope outcome.
	for _, as := range d.scopes {
		for _, sc := range as {
			prefix := sc.id + "/attempt:" + strconv.Itoa(sc.n) + "/"
			last := sc.start
			for i, e := range h.run.Execution {
				if strings.HasPrefix(e.Record.ID, prefix) && i > last {
					last = i
				}
			}
			if sc.end < 0 {
				for i := last + 1; i < len(h.run.Execution); i++ {
					r := h.run.Execution[i].Record
					if r.Kind == "attempt" || r.Kind == "result" || r.Kind == "wait" {
						sc.end = i - 1
						sc.status = "completion_not_recorded"
						break
					}
				}
			}
		}
	}
	return d
}

func diffStateValid(w coretask.WorkspaceState) bool {
	return w.Repository != "" && !(w.EmptyBase && w.BaseCommit != "") &&
		(w.EmptyBase || fullHash(w.BaseCommit)) && (fullHash(w.HeadCommit) || (w.EmptyBase && w.HeadCommit == ""))
}
func sameDiffState(a, b coretask.WorkspaceState) bool {
	return sameChangesRepository(a.Repository, b.Repository) && a.Cell == b.Cell && a.ScopeID == b.ScopeID && a.BaseCommit == b.BaseCommit && a.HeadCommit == b.HeadCommit && a.EmptyBase == b.EmptyBase
}
func incomingDiffState(a *diffAttempt) *coretask.WorkspaceState {
	if a.activity.WorkspaceBefore != nil {
		w := *a.activity.WorkspaceBefore
		return &w
	}
	g := a.in.Git
	w := coretask.WorkspaceState{Repository: g.BaseRepo, Cell: g.GetWorkspaceCellName(), BaseRef: g.BaseRef, BaseCommit: g.ResolvedBaseHash, HeadCommit: g.PersistHash}
	if w.BaseCommit == "" && fullHash(g.ParentHash) && fullHash(g.PersistHash) {
		w.BaseCommit = g.ParentHash
	}
	if g.Workspace != nil {
		w.ScopeID = g.Workspace.ScopeID
		w.EmptyBase = g.Workspace.HasEmptyBase(g.ResolvedBaseHash, g.ParentHash)
	}
	// An unresolved/floating ref is not an observed endpoint. A fixed base is
	// sufficient only when the invocation explicitly pins its incoming head.
	if !diffStateValid(w) {
		return nil
	}
	return &w
}
func (d *diffHistory) state(a *diffAttempt, before bool) *diffState {
	w := a.activity.WorkspaceAfter
	cut := a.end
	if before {
		w = incomingDiffState(a)
		cut = a.start
		if cut < 0 {
			cut = a.end
		}
	}
	if w == nil || cut < 0 {
		return nil
	}
	copy := *w
	v := &diffState{w: copy, a: a, before: before, cut: cut}
	if _, conflict := incomingBundleEvidence(a); conflict {
		v.issue = "conflicting incoming workspace snapshot references"
	}
	if !before {
		entry := incomingDiffState(a)
		if entry != nil {
			if !sameChangesRepository(entry.Repository, w.Repository) || entry.ScopeID != w.ScopeID || entry.Cell != w.Cell {
				v.issue = "recorded boundary has conflicting workspace identity"
			} else if (entry.BaseCommit != w.BaseCommit || entry.EmptyBase != w.EmptyBase) && !trustedDiffRebase(v) {
				v.issue = "active base changed without a recognized persisted rebase contract"
			}
		}
		if a.activity.Scope != w.ScopeID {
			v.issue = "recorded active workspace evidence is conflicting"
		}
	}
	return v
}
func (d *diffHistory) accepted(a *diffAttempt, cut int) bool {
	return d.eligible(a, cut, false)
}

// Failed state is diagnostic only at a selected failed scope's exit. It must
// never advance the default successful workspace or survive a discarded retry.
func (d *diffHistory) eligible(a *diffAttempt, cut int, diagnostic bool) bool {
	if a.end < 0 || a.end > cut || (a.task.Status != "succeeded" && !(diagnostic && a.task.Status == "failed")) {
		return false
	}
	if a.task.Status == "failed" {
		for _, later := range d.tasks {
			if later.task.ID == a.task.ID && later.task.Attempt > a.task.Attempt && later.start >= 0 && later.start <= cut {
				return false
			}
		}
	}
	for id, as := range d.scopes {
		for n, sc := range as {
			if strings.HasPrefix(a.task.ID, id+"/attempt:"+strconv.Itoa(n)+"/") && sc.end >= 0 && sc.end <= cut && sc.status == "failed" {
				// A later attempt proves that this branch was discarded. A job's final
				// failure alone does not erase successful persisted prefixes.
				for later, next := range as {
					if later > n && next.start <= cut {
						return false
					}
				}
			}
		}
	}
	return true
}
func (d *diffHistory) knownWorkspace(id string) bool {
	if id == d.mainID {
		return true
	}
	for _, a := range d.tasks {
		if a.in.Git.Workspace != nil && a.in.Git.Workspace.ScopeID == id {
			return true
		}
		for _, w := range []*coretask.WorkspaceState{a.activity.WorkspaceBefore, a.activity.WorkspaceAfter} {
			if w != nil && w.ScopeID == id {
				return true
			}
		}
	}
	return false
}
func (d *diffHistory) matches(w coretask.WorkspaceState, id string) bool {
	return w.ScopeID == id && (id != d.mainID || sameChangesRepository(w.Repository, d.mainRepo))
}
func diffBundle(a *diffAttempt) *jobdb.ArtifactKey {
	var key *jobdb.ArtifactKey
	for _, art := range a.task.Artifacts {
		if art.Name == gitstate.ThinPackArtifactName && art.Reference.Stored != nil {
			k := art.Reference.Stored.Key
			if key != nil && *key != k {
				return nil
			}
			key = &k
		}
	}
	return key
}
func incomingBundle(a *diffAttempt) *jobdb.ArtifactKey {
	key, _ := incomingBundleEvidence(a)
	return key
}

func incomingBundleEvidence(a *diffAttempt) (*jobdb.ArtifactKey, bool) {
	key := a.in.Restore
	for _, art := range a.inputArtifacts {
		if art.Name == gitstate.ThinPackArtifactName && art.Reference.Stored != nil {
			k := art.Reference.Stored.Key
			if key != nil && *key != k {
				return nil, true
			}
			key = &k
		}
	}
	return key, false
}

// lineage returns only producers linked by a recorded exact incoming snapshot.
// Equal hashes alone do not establish an edge, including between siblings.
func (d *diffHistory) predecessor(v *diffState) *diffState {
	before := incomingDiffState(v.a)
	if before == nil {
		return nil
	}
	anchor := v.cut
	if v.a.start >= 0 && v.a.start < anchor {
		anchor = v.a.start
	}
	key := incomingBundle(v.a)
	if key == nil {
		// Empty comparisons need no byte source. Their fixed incoming endpoints
		// can carry an unchanged state, or a recognized no-work rebase can reset
		// that state to a new base. This never licenses nonempty patch reuse.
		priorEmpty := before.BaseCommit != "" && before.BaseCommit == before.HeadCommit || before.EmptyBase && before.HeadCommit == ""
		if !diffEmpty(v) || !priorEmpty || (!sameDiffState(v.w, *before) && !trustedDiffRebase(v)) {
			return nil
		}
		var found *diffState
		for _, a := range d.tasks {
			if a == v.a || a.end < 0 || a.end >= anchor || !d.accepted(a, anchor) {
				continue
			}
			state := d.state(a, false)
			if state != nil && diffEmpty(state) && sameDiffState(state.w, *before) {
				found = state
			}
		}
		return found
	}
	var found *diffState
	for _, a := range d.tasks {
		if a == v.a || a.end < 0 || a.end >= anchor || a.task.Status != "succeeded" {
			continue
		}
		k := diffBundle(a)
		if k == nil || *k != *key || k.JobId != d.h.job.ID || k.TaskOrdinal != a.ordinal {
			continue
		}
		state := d.state(a, false)
		if state == nil || !sameDiffState(state.w, *before) {
			continue
		}
		if found != nil && found.a != a {
			return nil
		}
		found = state
	}
	return found
}

func (d *diffHistory) latest(candidates []*diffState) (*diffState, string) {
	if len(candidates) == 0 {
		return nil, "no persisted state at this boundary"
	}
	latest := candidates[len(candidates)-1]
	// A subsequent fixed incoming snapshot proves adoption even when parallel
	// branches finished in a different order. Without it, competing sibling
	// workspace exits are not a chronological last-writer policy.
	for _, v := range candidates[:len(candidates)-1] {
		if v.a.end == latest.a.end {
			return nil, "conflicting persisted boundaries"
		}
		if diffConcurrent(v.a.task.ID, latest.a.task.ID) && !d.reaches(latest, v) {
			return nil, "workspace adoption between branches is unproven"
		}
	}
	return latest, ""
}
func (d *diffHistory) reaches(v, target *diffState) bool {
	for v != nil {
		if v.a == target.a {
			return true
		}
		// A recognized successful no-work rebase establishes a new active
		// empty base, including a floating workspace resolved during the task.
		// Older empty observations of that workspace cannot contradict it.
		if diffEmpty(v) && diffEmpty(target) && trustedDiffRebase(v) && sameChangesRepository(v.w.Repository, target.w.Repository) && v.w.ScopeID == target.w.ScopeID && v.w.Cell == target.w.Cell {
			return true
		}
		if sameDiffState(v.w, target.w) {
			// No artifact lineage is needed for a proven empty comparison:
			// the immutable incoming endpoints prove the unchanged handoff.
			incoming := incomingDiffState(v.a)
			if diffEmpty(v) && diffEmpty(target) && incoming != nil && sameDiffState(*incoming, target.w) {
				return true
			}
			before := incomingDiffState(target.a)
			if before != nil && sameDiffState(*before, target.w) {
				a, b := diffBundle(v.a), incomingBundle(target.a)
				if a != nil && b != nil && *a == *b {
					return true
				}
			}
		}
		v = d.predecessor(v)
	}
	return false
}
func diffConcurrent(a, b string) bool {
	x, y := splitNativePath(a), splitNativePath(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return strings.HasPrefix(x[i], "scope:") || strings.HasPrefix(y[i], "scope:")
		}
	}
	return false
}

// scopeWorkspace follows recorded workspace declarations on this native
// ancestry, including the selected scope itself. Descendant declarations cannot
// redefine their enclosing scope. Resolution results are immutable observation
// evidence; no repository lookup or guess from a sibling's endpoints is needed.
func (d *diffHistory) scopeWorkspace(sc *diffScope, cut int) (string, string) {
	workspace, depth := d.mainID, 0
	issue := ""
	if d.mainIssue {
		issue = "scope active workspace identity is not recorded unambiguously"
	}
	prefix := sc.id + "/attempt:" + strconv.Itoa(sc.n) + "/"
	for _, a := range d.tasks {
		if a.task.Type != "recipe_workspace_resolve" || a.task.Status != "succeeded" || a.end < 0 || a.end > cut {
			continue
		}
		parts := splitNativePath(a.task.ID)
		if len(parts) < 2 {
			continue
		}
		parent := "/" + strings.Join(parts[:len(parts)-1], "/") + "/"
		if !strings.HasPrefix(prefix, parent) || len(parts) < depth {
			continue
		}
		var result struct {
			Workspace contextual.WorkspaceContext `json:"workspace"`
		}
		if json.Unmarshal(a.raw, &result) != nil || result.Workspace.ScopeID == "" {
			return "", "scope workspace resolution evidence is invalid"
		}
		if len(parts) == depth && workspace != result.Workspace.ScopeID {
			return "", "scope workspace resolution evidence conflicts"
		}
		workspace, depth, issue = result.Workspace.ScopeID, len(parts), ""
	}
	return workspace, issue
}

func (d *diffHistory) selectState(q WorkspaceDiffRequest, m *WorkspaceDiff) (*diffState, error) {
	invalid := func(detail string) (*diffState, error) {
		return nil, WorkspaceDiffDiagnostic("invalid_request", detail+"; use job log and job story for exact occurrence IDs and attempts", nil)
	}
	workspace := d.mainID
	if q.Workspace != nil && *q.Workspace != "main" {
		workspace = *q.Workspace
	}
	if q.Workspace != nil && !d.knownWorkspace(workspace) {
		return invalid("unknown workspace ID")
	}
	boundary := "after"
	if q.Boundary != nil {
		boundary = *q.Boundary
	}
	m.Resolved.Boundary = boundary
	if d.mainIssue && q.Task == nil && q.Scope == nil && (q.Workspace == nil || *q.Workspace == "main") {
		if q.Workspace == nil {
			return invalid("active workspace is ambiguous; supply an exact workspace selector")
		}
		m.Resolved.Status = "unavailable"
		m.Reasons = []string{"main workspace identity is not recorded unambiguously"}
		return nil, nil
	}
	cut := len(d.h.run.Execution) - 1
	if q.Task != nil {
		n := 0
		if q.Attempt != nil {
			n = *q.Attempt
		}
		var a *diffAttempt
		for _, x := range d.tasks {
			if x.task.ID == *q.Task && (n == 0 || x.task.Attempt == n) && (a == nil || x.task.Attempt > a.task.Attempt) {
				a = x
			}
		}
		if a == nil {
			return invalid("unknown task occurrence or attempt")
		}
		m.Resolved.Task = a.task.ID
		m.Resolved.Attempt = a.task.Attempt
		m.Resolved.Status = a.task.Status
		v := d.state(a, boundary == "before")
		if boundary == "after" && a.task.Status == "failed" {
			m.Resolved.Status = "diagnostic"
		}
		if v == nil {
			m.Reasons = []string{"selected task boundary was not persisted"}
			return nil, nil
		}
		if q.Workspace == nil {
			workspace = v.w.ScopeID
		}
		m.Resolved.Workspace = workspace
		m.Cutoff = workspaceCutoff(m.Observation, v.cut)
		if q.Workspace != nil && !d.matches(v.w, workspace) {
			m.Reasons = []string{"known workspace has no recorded state at selected task boundary"}
			return nil, nil
		}
		return v, nil
	}
	if q.Scope != nil {
		as, ok := d.scopes[*q.Scope]
		if !ok {
			return invalid("unknown scope occurrence")
		}
		n := 0
		if q.Attempt != nil {
			n = *q.Attempt
		} else {
			for k := range as {
				if k > n {
					n = k
				}
			}
		}
		sc := as[n]
		if sc == nil && q.Attempt == nil && len(as) == 0 {
			m.Resolved.Scope = *q.Scope
			m.Resolved.Status = "pending"
			m.Reasons = []string{"scope attempt has not been recorded"}
			return nil, nil
		}
		if sc == nil {
			return invalid("unknown scope attempt")
		}
		m.Resolved.Scope = sc.id
		m.Resolved.Attempt = n
		m.Resolved.Status = sc.status
		prefix := sc.id + "/attempt:" + strconv.Itoa(n) + "/"
		if sc.end >= 0 {
			cut = sc.end
		}
		if q.Workspace == nil {
			var issue string
			workspace, issue = d.scopeWorkspace(sc, cut)
			if issue != "" {
				m.Reasons = []string{issue}
				return nil, nil
			}
		}
		if boundary == "before" {
			cut = sc.start
			// The first recorded inherited input is the actual entry for this
			// attempt. It may deliberately carry a persisted prefix of a failed
			// earlier attempt. A descendant override cannot redefine entry.
			var first *diffState
			for _, a := range d.tasks {
				if !strings.HasPrefix(a.task.ID, prefix) {
					continue
				}
				v := d.state(a, true)
				if v != nil && (first == nil || v.cut < first.cut) {
					first = v
				}
			}
			if first != nil && d.matches(first.w, workspace) {
				first.cut = cut
				m.Resolved.Workspace = workspace
				m.Cutoff = workspaceCutoff(m.Observation, cut)
				return first, nil
			}
			// Otherwise retain only proven enclosing state, never a child override.
			var prior []*diffState
			for _, a := range d.tasks {
				if a.end >= cut || !d.accepted(a, cut) {
					continue
				}
				v := d.state(a, false)
				if v != nil && d.matches(v.w, workspace) {
					prior = append(prior, v)
				}
			}
			v, reason := d.latest(prior)
			if v == nil && len(prior) == 0 && workspace == d.mainID {
				for _, a := range d.tasks {
					if strings.HasPrefix(a.task.ID, prefix) {
						before := d.state(a, true)
						if before != nil && d.matches(before.w, workspace) && diffStateValid(before.w) {
							v = before
							v.cut = cut
							break
						}
					}
				}
			}
			if v == nil {
				m.Reasons = []string{"scope entry state is not recorded: " + reason}
			} else {
				v.cut = cut
			}
			m.Resolved.Workspace = workspace
			m.Cutoff = workspaceCutoff(m.Observation, cut)
			return v, nil
		}
		var candidates []*diffState
		for _, a := range d.tasks {
			if !strings.HasPrefix(a.task.ID, prefix) || !d.eligible(a, cut, sc.status == "failed") {
				continue
			}
			v := d.state(a, false)
			if v == nil && a.task.Status == "failed" {
				if incoming := incomingDiffState(a); incoming != nil && d.matches(*incoming, workspace) {
					m.Resolved.Status = "diagnostic"
					m.Resolved.Workspace = workspace
					m.Cutoff = workspaceCutoff(m.Observation, cut)
					m.Reasons = []string{"failed task has no persisted diagnostic exit state"}
					return nil, nil
				}
			}
			if v != nil && d.matches(v.w, workspace) {
				candidates = append(candidates, v)
			}
		}
		v, reason := d.latest(candidates)
		if v == nil && len(candidates) == 0 {
			var entry *diffState
			for _, a := range d.tasks {
				if a.end >= sc.start || !d.accepted(a, sc.start) {
					continue
				}
				state := d.state(a, false)
				if state != nil && d.matches(state.w, workspace) {
					entry = state
				}
			}
			if entry != nil {
				for _, a := range d.tasks {
					if a.start <= cut {
						continue
					}
					incoming := incomingDiffState(a)
					k, b := incomingBundle(a), diffBundle(entry.a)
					if incoming != nil && sameDiffState(*incoming, entry.w) && k != nil && b != nil && *k == *b {
						v = entry
						break
					}
				}
			}
		}
		if v == nil {
			m.Reasons = []string{"scope exit state is not recorded: " + reason}
		} else {
			v.cut = cut
		}
		if sc.status == "failed" {
			m.Resolved.Status = "diagnostic"
			if v == nil {
				m.Reasons = []string{"failed scope has no recorded diagnostic boundary"}
			}
		}
		m.Resolved.Workspace = workspace
		m.Cutoff = workspaceCutoff(m.Observation, cut)
		return v, nil
	}
	m.Resolved.Status = "persisted"
	m.Resolved.Workspace = workspace
	m.Cutoff = workspaceCutoff(m.Observation, cut)
	var candidates []*diffState
	for _, a := range d.tasks {
		if !d.accepted(a, cut) {
			continue
		}
		v := d.state(a, false)
		if v != nil && d.matches(v.w, workspace) {
			candidates = append(candidates, v)
		}
	}
	v, reason := d.latest(candidates)
	if v == nil {
		m.Reasons = []string{reason}
		return nil, nil
	}
	m.Resolved.Task = v.a.task.ID
	m.Resolved.Attempt = v.a.task.Attempt
	return v, nil
}

func diffProducer(job string, a *diffAttempt) *WorkspaceDiffProducer {
	return &WorkspaceDiffProducer{JobID: job, Task: a.task.ID, Attempt: a.task.Attempt}
}
func diffUnavailable(m *WorkspaceDiff, reason string) {
	m.Status = "unavailable"
	m.Source = nil
	m.Bytes = nil
	m.Reasons = []string{reason}
}

func (d *diffHistory) savedPatch(v *diffState) (*Artifact, string) {
	patchEvidence := hasSavedPatchEvidence(v.a)
	if v.a.contract != 1 || v.a.activity.Git.PersistHash != v.w.HeadCommit {
		return nil, "invalid saved patch producer/workspace binding"
	}
	if patchEvidence || v.a.activity.Git.ParentHash != "" {
		if v.a.activity.Git.ParentHash != v.w.BaseCommit {
			return nil, "invalid saved patch producer/workspace binding"
		}
	} else if v.w.BaseCommit != "" && !unchangedObserverWithoutParent(v) {
		return nil, "invalid unchanged workspace observer proof"
	}
	// A recognized no-work rebase can follow a newer base during restore,
	// before its recorded observations. Preserve its separate empty-state proof.
	if !patchEvidence && !(diffEmpty(v) && trustedDiffRebase(v)) {
		if before := incomingDiffState(v.a); before != nil && sameDiffState(*before, v.w) && conflictingObserverMetadata(v) {
			return nil, "conflicting unchanged workspace observer metadata"
		}
	}
	// Explicit references in the envelope must agree with physical output
	// membership; the general inspection view intentionally merges by name.
	for name, ref := range v.a.activity.Refs {
		if name != "diff_from_base.diff" && name != "diff_from_parent.diff" {
			continue
		}
		for _, art := range v.a.task.Artifacts {
			if art.Name == name && !sameRef(art.Reference, ref) {
				return nil, "conflicting saved patch references"
			}
		}
	}
	var base, parent *Artifact
	for _, art := range v.a.task.Artifacts {
		if art.Name != "diff_from_base.diff" && art.Name != "diff_from_parent.diff" {
			continue
		}
		if art.Reference.Stored == nil {
			return nil, "invalid saved patch reference"
		}
		p := &base
		if art.Name == "diff_from_parent.diff" {
			p = &parent
		}
		if *p != nil && !sameRef((*p).Reference, art.Reference) {
			return nil, "conflicting saved patch references"
		}
		copy := art
		*p = &copy
	}
	if base != nil {
		return base, ""
	}
	if parent == nil {
		return nil, "no saved cumulative patch for selected state"
	}
	if !trustedDiffParent(v) {
		return nil, "parent-only patch has no trusted matching-base endpoint proof"
	}
	return parent, ""
}
func trustedDiffParent(v *diffState) bool {
	a := v.a
	if a.contract != 1 {
		return false
	}
	var command struct {
		Op     string `json:"op"`
		Step   string `json:"step"`
		Recipe string `json:"recipe"`
		Node   string `json:"node"`
	}
	if json.Unmarshal(a.command, &command) != nil || command.Step != a.task.Type || !fullHash(command.Recipe) || command.Node == "" || command.Node != a.in.Git.NodePath {
		return false
	}
	var in struct {
		Input struct {
			RootHash string `json:"root_hash"`
			BaseHash string `json:"base_hash"`
			RepoPath string `json:"repo_path"`
		} `json:"input"`
	}
	// Read only persisted invocation fields; local paths are never accessed.
	// a.in intentionally excludes business input; recover it from the snapshot.
	if json.Unmarshal(a.input, &in) != nil || (in.Input.RepoPath != contextual.WorktreePathSentinel && in.Input.RepoPath != contextual.OpWorktreePathSentinel) {
		return false
	}
	before := incomingDiffState(a)
	if before == nil || before.BaseCommit != v.w.BaseCommit || before.EmptyBase != v.w.EmptyBase {
		return false
	}
	if in.Input.BaseHash == contextual.GitExecutionBaseSentinel {
		in.Input.BaseHash = before.BaseCommit
	}
	if in.Input.RootHash == contextual.GitExecutionBaseSentinel {
		in.Input.RootHash = before.BaseCommit
	}
	if command.Op == "git_persist_commit" && a.task.Type == "git_persist_commit:git_persist_commit" {
		var out struct {
			CommitHash string `json:"commit_hash"`
			ParentHash string `json:"parent_hash"`
		}
		return json.Unmarshal(a.raw, &out) == nil && out.CommitHash == v.w.HeadCommit && out.ParentHash == v.w.BaseCommit && in.Input.RootHash == v.w.BaseCommit && (out.ParentHash != "" || v.w.EmptyBase)
	}
	if command.Op == "squash" && a.task.Type == "squash:squash" {
		var out struct {
			Hash  string `json:"squashed_hash"`
			Range struct {
				Base string `json:"base_hash"`
			} `json:"squashed_commits"`
		}
		return json.Unmarshal(a.raw, &out) == nil && out.Hash == v.w.HeadCommit && out.Range.Base == v.w.BaseCommit && in.Input.BaseHash == v.w.BaseCommit && (out.Range.Base != "" || v.w.EmptyBase)
	}
	return false
}

// The public managed rebase result binds a new active base and head. It is
// sufficient for a no-pending-work empty boundary, never parent-diff proof.
func trustedDiffRebase(v *diffState) bool {
	a := v.a
	var command struct{ Op, Step, Recipe, Node string }
	if a.contract != 1 || json.Unmarshal(a.command, &command) != nil || !fullHash(command.Recipe) || command.Node == "" || command.Node != a.in.Git.NodePath || command.Step != a.task.Type {
		return false
	}
	if command.Op != "rebase" && command.Op != "thinpackrebase" {
		return false
	}
	if a.task.Type != command.Op+":"+command.Op {
		return false
	}
	var in struct {
		Input struct {
			RepoPath string `json:"repo_path"`
		} `json:"input"`
	}
	if json.Unmarshal(a.input, &in) != nil || (in.Input.RepoPath != contextual.WorktreePathSentinel && in.Input.RepoPath != contextual.OpWorktreePathSentinel) {
		return false
	}
	var out struct {
		Base string `json:"new_base_hash"`
		Head string `json:"new_persist_hash"`
	}
	return json.Unmarshal(a.raw, &out) == nil && out.Base == v.w.BaseCommit && out.Head == v.w.HeadCommit && fullHash(out.Base)
}

func (d *diffHistory) discovery() []Scope {
	ids := make([]string, 0, len(d.scopes))
	for id := range d.scopes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []Scope
	for _, id := range ids {
		sc := Scope{ID: id, Status: "completion_not_recorded"}
		ns := []int{}
		for n := range d.scopes[id] {
			ns = append(ns, n)
		}
		sort.Ints(ns)
		for _, n := range ns {
			s := d.scopes[id][n]
			sc.Status = s.status
			q := WorkspaceDiffRequest{JobID: d.h.job.ID, WorkspaceDiffOptions: WorkspaceDiffOptions{Scope: &id, Attempt: &n}}
			m := &WorkspaceDiff{Observation: "discovery"}
			v, _ := d.selectState(q, m)
			after := v != nil && v.issue == "" && diffStateValid(v.w)
			boundary := "before"
			q.Boundary = &boundary
			m = &WorkspaceDiff{Observation: "discovery"}
			v, _ = d.selectState(q, m)
			sc.Attempts = append(sc.Attempts, ScopeAttempt{Attempt: n, Status: s.status, Before: v != nil && v.issue == "" && diffStateValid(v.w), After: after})
			sc.Parent = s.parent
		}
		parts := splitNativePath(id)
		if len(parts) > 0 {
			sc.Site = parts[len(parts)-1]
		}
		out = append(out, sc)
	}
	return out
}
