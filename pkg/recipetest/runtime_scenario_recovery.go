package recipetest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	restartservice "github.com/colony-2/c2j/pkg/jobrestart/service"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/starter"

	"github.com/colony-2/c2j/pkg/task"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

func (f *runtimeFixture) restartScenarioJob(ctx context.Context, rt caseRuntime, tenant string, a ScenarioAction, receipt *ScenarioActionReceipt) error {
	s := f.scenario
	j, ok := s.report.Jobs[a.Target]
	if !ok {
		return fmt.Errorf("unresolved restart source %s", a.Target)
	}
	state, err := f.ctl.InspectJob(ctx, j.Ref.Key())
	if err != nil {
		return err
	}
	if !state.Terminal {
		return fmt.Errorf("restart source must be settled")
	}
	spec := a.Restart
	service := restartservice.Service{Engine: f.ctl.Engine, Tenant: tenant, Submit: func(ctx context.Context, k jobdb.JobKey, scope string, patch *task.ContextPatch, opts starter.RestartOptions) (jobref.Ref, error) {
		return starter.RestartRecipeJob(ctx, f.ctl.Engine, k, scope, patch, opts)
	}}
	points, err := service.Restart(ctx, jobrestart.Request{JobID: j.Ref.JobID, Operation: "points"})
	if err != nil {
		return err
	}
	// Path is the exact native scope-site chain, including $job. Decode the
	// canonical identity grammar; never match display text or invent ordinals.
	var matches []jobrestart.RestartScope
	for _, p := range points.Points.RestartScopes {
		path, e := nativeScopePath(p.ScopeID)
		if e == nil && reflect.DeepEqual(path, spec.Path) {
			matches = append(matches, p)
		}
	}

	if spec.Occurrence > 0 {
		if len(matches) < spec.Occurrence {
			return fmt.Errorf("restart path %v occurrence %d unavailable", spec.Path, spec.Occurrence)
		}
		matches = matches[spec.Occurrence-1 : spec.Occurrence]
	}
	if len(matches) != 1 {
		paths := [][]string{}
		for _, p := range points.Points.RestartScopes {
			path, e := nativeScopePath(p.ScopeID)
			if e == nil {
				paths = append(paths, path)
			}
		}
		return fmt.Errorf("restart path %v matches %d recorded scopes; available paths: %v", spec.Path, len(matches), paths)
	}
	point := matches[0]
	selector := jobrestart.Selector{Scope: point.ScopeID}
	if _, err = restartservice.Select(points.Points.RestartScopes, selector); err != nil {
		return err
	}
	// Require native successful prefix results before the selected checkpoint.
	for _, q := range spec.RequiredPrefix {
		r, found, e := f.selectScenarioTask(ctx, rt, tenant, j.Ref.JobID, q)
		if e != nil {
			return e
		}
		if !found || r.attempt.State != jobdb.TaskAttemptStateSucceeded || r.attempt.Ordinal >= point.ChapterOrdinal {
			return fmt.Errorf("restart retained prefix %s is missing, failed or after selected scope", q.NodePath)
		}
	}
	contextResponse, err := service.Restart(ctx, jobrestart.Request{JobID: j.Ref.JobID, Operation: "context", Selector: selector})
	if err != nil {
		return fmt.Errorf("inspect selected restart context: %w", err)
	}
	contextRaw, err := json.Marshal(contextResponse.Context)
	if err != nil {
		return err
	}
	receipt.ContextSHA256 = contentDigest(contextRaw)
	request := jobrestart.Request{Operation: "submit", JobID: j.Ref.JobID, Selector: selector, RequestID: spec.RequestID}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	fingerprint := contentDigest(raw)
	for _, old := range s.report.Actions {
		if old.RequestID == spec.RequestID && old.Fingerprint != fingerprint {
			return fmt.Errorf("restart request ID reused with changed selection")
		}
	}
	f.nativeMu.Lock()
	defer f.nativeMu.Unlock()
	response, err := service.Restart(ctx, request)
	if err != nil {
		return err
	}
	ref := *response.Ref
	if ref.JobID == j.Ref.JobID || (ref.GoalID != "" && ref.GoalID == j.Ref.GoalID) {
		return fmt.Errorf("restart must create a distinct independent job")
	}
	f.mu.Lock()
	s.report.Jobs[spec.Continuation] = ScenarioJobReport{Ref: ref}
	f.mu.Unlock()
	receipt.SourceJobID = j.Ref.JobID
	receipt.Ref = &ref
	receipt.Scope = &point
	receipt.RequestID = spec.RequestID
	receipt.Fingerprint = fingerprint
	return nil
}
func (f *runtimeFixture) retainScenario(ctx context.Context, rt caseRuntime, tenant string, required bool) error {
	spec := f.c.Runtime.Controls.Scenario
	s := f.scenario
	limit := 256 * 1024
	if f.c.Runtime.Controls.Snapshot != nil && f.c.Runtime.Controls.Snapshot.MaxArtifactBytes > 0 {
		limit = f.c.Runtime.Controls.Snapshot.MaxArtifactBytes
	}
	redactor := secrets.NewRedactor()
	defer redactor.Close()
	for _, v := range f.c.Runtime.Secrets {
		if err := redactor.Register(v); err != nil {
			return err
		}
	}
	for _, q := range spec.Retain {
		j, ok := s.report.Jobs[q.Job]
		if !ok {
			if !required {
				continue
			}
			return fmt.Errorf("retention %s job unresolved", q.ID)
		}
		r, found, err := f.selectScenarioTask(ctx, rt, tenant, j.Ref.JobID, q.Task)
		if err != nil {
			return err
		}
		if !found {
			if !required {
				continue
			}
			return fmt.Errorf("retention %s task unavailable", q.ID)
		}
		io := r.attempt.Output
		if q.Part == "input" {
			io = r.attempt.Input
		}
		if io == nil {
			return fmt.Errorf("retention %s output unavailable", q.ID)
		}
		data := []byte(io.Data)
		var key *jobdb.ArtifactKey
		if q.Artifact != "" {
			matches := 0
			for _, artifact := range io.Artifacts {
				if artifact.Name != q.Artifact {
					continue
				}
				matches++
				if artifact.Key == nil {
					return fmt.Errorf("retention %s missing native artifact key", q.ID)
				}
				k := *artifact.Key
				key = &k
			}
			if matches != 1 {
				return fmt.Errorf("retention %s expected one artifact, found %d", q.ID, matches)
			}
			data, err = readEffectArtifact(ctx, rt, tenant, *key, limit)
			if err != nil {
				return err
			}
		}
		if len(data) > limit {
			return fmt.Errorf("retention %s exceeds byte limit", q.ID)
		}
		safe := redactor.Redact(string(data))
		if safe != string(data) {
			return fmt.Errorf("retention %s exact bytes require secret redaction", q.ID)
		}
		if q.ExpectBase64 != "" {
			want, err := base64.StdEncoding.DecodeString(q.ExpectBase64)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(data, want) {
				return fmt.Errorf("retention %s bytes mismatch", q.ID)
			}
		}
		if key != nil {
			r.origin = key.JobId
		}
		item := ScenarioRetained{OriginJobID: r.origin, ObservedJobID: j.Ref.JobID, InvocationID: r.task.InvocationID, Attempt: r.attempt.Attempt, ResultOrdinal: r.attempt.Ordinal, State: r.attempt.State, ArtifactKey: key, Content: RetainedContent{SHA256: contentDigest(data), ContentBase64: base64.StdEncoding.EncodeToString(data)}}
		if previous, ok := s.report.Retained[q.ID]; ok && !reflect.DeepEqual(previous, item) {
			return fmt.Errorf("retention %s changed across runtime lifetimes", q.ID)
		}
		s.report.Retained[q.ID] = item
		if err := f.checkScenarioSize(); err != nil {
			return err
		}
	}
	for _, q := range spec.Retain {
		if q.CompareTo != "" {
			a, b := s.report.Retained[q.ID], s.report.Retained[q.CompareTo]
			if !required && (a.Content.SHA256 == "" || b.Content.SHA256 == "") {
				continue
			}
			if a.Content != b.Content {
				return fmt.Errorf("retention %s differs from %s", q.ID, q.CompareTo)
			}
		}
	}
	return nil
}

// Native execution IDs encode JSON string sites followed by occurrence and
// attempt coordinates. JSON decoding preserves embedded slashes and quotes.
func nativeScopePath(id string) ([]string, error) {
	var path []string
	for id != "" {
		if strings.HasPrefix(id, "/scope:") {
			id = strings.TrimPrefix(id, "/scope:")
			d := json.NewDecoder(strings.NewReader(id))
			var site string
			if err := d.Decode(&site); err != nil {
				return nil, err
			}
			id = id[d.InputOffset():]
			if !strings.HasPrefix(id, "#") {
				return nil, fmt.Errorf("native scope occurrence missing")
			}
			id = id[1:]
			end := strings.IndexByte(id, '/')
			if end < 0 {
				end = len(id)
			}
			n, err := strconv.Atoi(id[:end])
			if err != nil || n < 1 {
				return nil, fmt.Errorf("invalid native scope occurrence")
			}
			id = id[end:]
			path = append(path, site)
		} else if strings.HasPrefix(id, "/attempt:") {
			id = strings.TrimPrefix(id, "/attempt:")
			end := strings.IndexByte(id, '/')
			if end < 0 {
				end = len(id)
			}
			n, err := strconv.Atoi(id[:end])
			if err != nil || n < 1 {
				return nil, fmt.Errorf("invalid native scope attempt")
			}
			id = id[end:]
		} else {
			return nil, fmt.Errorf("unsupported native scope identity")
		}
	}
	if len(path) == 0 {
		return nil, fmt.Errorf("missing native scope path")
	}
	return path, nil
}
