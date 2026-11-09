package input

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func historyID(prefix string, parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return prefix + hex.EncodeToString(h[:])
}
func historyFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	err := json.Unmarshal(raw, &m)
	if err == nil && m == nil {
		err = fmt.Errorf("expected JSON object")
	}
	return m, err
}
func historyString(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func historyField(m map[string]json.RawMessage, name, source string) HistoryValue {
	v, ok := m[name]
	return HistoryValue{Present: ok, Raw: v, Source: source}
}
func historyTaskID(id string) string {
	if i := strings.LastIndex(id, "/attempt:"); i >= 0 {
		return id[:i]
	}
	return id
}
func historyScope(id string) string {
	if i := strings.LastIndex(historyTaskID(id), "/task:"); i >= 0 {
		return historyTaskID(id)[:i]
	}
	return ""
}
func historyAttempt(id string) int {
	i := strings.LastIndex(id, "/attempt:")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(id[i+9:])
	return n
}
func historyEqual(a, b json.RawMessage) bool {
	// Decode with UseNumber; this comparison never rounds a number through float64.
	var x, y any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	if da.Decode(&x) != nil || db.Decode(&y) != nil {
		return false
	}
	aa, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(aa, bb)
}

// Frozen form transport expands Go zero fields when preparing a collection.
// Normalize only known struct zero defaults for lineage comparison; never use
// this normalized value for presentation. Interface-valued numbers stay exact.
func historyFrozenMatches(a, b json.RawMessage) bool {
	normalize := func(raw json.RawMessage) any {
		var value any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&value) != nil {
			return nil
		}
		return historyNormalizeForm(value, reflect.TypeOf(InputForm{}))
	}
	x, y := normalize(a), normalize(b)
	if x == nil || y == nil {
		return false
	}
	aa, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(aa, bb)
}
func historyNormalizeForm(value any, typ reflect.Type) any {
	if typ.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		return historyNormalizeForm(value, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		m, ok := value.(map[string]any)
		if !ok {
			return value
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			v, exists := m[name]
			if !exists {
				continue
			}
			v = historyNormalizeForm(v, f.Type)
			zero := v == nil
			switch f.Type.Kind() {
			case reflect.String:
				zero = zero || v == ""
			case reflect.Bool:
				zero = zero || v == false
			case reflect.Int, reflect.Int64:
				zero = zero || v == json.Number("0")
			case reflect.Map, reflect.Struct:
				if obj, ok := v.(map[string]any); ok {
					zero = len(obj) == 0
				}
			case reflect.Slice:
				if arr, ok := v.([]any); ok {
					zero = len(arr) == 0
				}
			}
			if zero {
				delete(m, name)
			} else {
				m[name] = v
			}
		}
		return m
	case reflect.Slice:
		if arr, ok := value.([]any); ok {
			for i := range arr {
				arr[i] = historyNormalizeForm(arr[i], typ.Elem())
			}
			return arr
		}
	case reflect.Map:
		if m, ok := value.(map[string]any); ok {
			for k, v := range m {
				m[k] = historyNormalizeForm(v, typ.Elem())
			}
			return m
		}
	}
	return value
}

// inputSnapshot restores only the private raw input payload. It intentionally
// does not call ActivityInvocationRequest.RestoreInput, whose map[string]any
// decoder is appropriate for execution but can round numbers in history.
func (s *historyService) inputSnapshot(ctx context.Context, key jobdb.JobKey, snap *jobdb.ExecutionSnapshot) (json.RawMessage, map[string]json.RawMessage, error) {
	if snap == nil {
		return nil, nil, fmt.Errorf("missing frozen input snapshot")
	}
	m, err := historyFields(snap.Data)
	if err != nil {
		return nil, nil, err
	}
	private := historyString(m["input_artifact"])
	if private != "" {
		var stored *jobdb.ArtifactKey
		for _, a := range snap.Artifacts {
			if a.Name == private {
				stored = a.Key
				break
			}
		}
		if stored == nil {
			return nil, nil, fmt.Errorf("missing private input attachment %q", private)
		}
		a := s.reader.GetArtifactLazy(ctx, key.TenantId, *stored)
		if a == nil {
			return nil, nil, fmt.Errorf("private input bytes unavailable")
		}
		b, e := a.Bytes(ctx)
		if e != nil {
			if errors.Is(e, jobdb.ErrChapterNotFound) || errors.Is(e, jobdb.ErrJobNotFound) {
				return nil, nil, fmt.Errorf("private input bytes unavailable: %w", e)
			}
			return nil, nil, runtimeError(ErrStorage, "read private history input", e)
		}
		payload, e := historyFields(b)
		if e != nil {
			return nil, nil, e
		}
		for _, name := range []string{"input", "artifacts", "preparation"} {
			m[name] = payload[name]
		}
	}
	form := m["input"]
	if _, err = historyFields(form); err != nil {
		return nil, nil, fmt.Errorf("invalid frozen form: %w", err)
	}
	return form, m, nil
}
func historyOutput(snap *jobdb.ExecutionSnapshot) (json.RawMessage, map[string]json.RawMessage, error) {
	if snap == nil {
		return nil, nil, fmt.Errorf("missing committed output snapshot")
	}
	m, err := historyFields(snap.Data)
	if err != nil {
		return nil, nil, err
	}
	if v, ok := m["v"]; ok {
		if string(v) != "1" || historyString(m["kind"]) != "activity_output" {
			return nil, nil, &HistoryError{Kind: ErrHistoryUnsupported, Capability: "activity_output_version"}
		}
		m, err = historyFields(m["payload"])
		if err != nil {
			return nil, nil, err
		}
	}
	raw, ok := m["output"]
	if !ok {
		return nil, nil, fmt.Errorf("missing activity output")
	}
	if _, err := historyFields(raw); err != nil {
		return nil, nil, err
	}
	return raw, m, nil
}

type historyTaskDescriptor struct {
	Call struct {
		TaskType string          `json:"task_type"`
		Command  json.RawMessage `json:"command"`
	} `json:"call"`
}
type historyGeneration struct {
	id, scope string
	ordinal   int64
	raw       json.RawMessage
	entry     JobInputStoryEntryDetail
	uses      int
}

func historyEntry(key jobdb.JobKey, id string, raw json.RawMessage, ordinal int64) JobInputStoryEntryDetail {
	m, _ := historyFields(raw)
	kind := "input"
	if historyString(m["kind"]) == "review" {
		kind = "review"
	} else if v, ok := m["response_schema"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		kind = "structured"
	}
	if v := historyString(m["kind"]); v != "" && v != "review" {
		kind = "unknown"
	}
	return JobInputStoryEntryDetail{JobInputStoryEntry: JobInputStoryEntry{
		EntryID: historyID("ihe1_", key.TenantId, key.JobId, id), RequestID: historyString(m["request_id"]), Kind: kind,
		Title: historyField(m, "title", "/title"), Summary: HistoryValue{}, RequestedAt: historyField(m, "requested_at", "/requested_at"),
		State: "prepared", StateEvidence: []string{id}, Locator: HistoryLocator{InvocationID: id, TaskAttempt: historyAttempt(id), JobAttempt: historyJobAttempt(id)}, FormFields: m,
		ResponseSource: "unknown", Actor: HistoryActor{Provenance: "unavailable"},
	}, RawForm: raw, RequestManifest: []HistoryArtifactMember{}, ResponseManifest: []HistoryArtifactMember{}}
}

func (s *historyService) project(ctx context.Context, key jobdb.JobKey, run jobdb.GetJobRunResponse) ([]JobInputStoryEntryDetail, []HistoryDiagnostic, error) {
	tasks := map[string]historyTaskDescriptor{}
	dispatch := map[string]string{}
	var gens []*historyGeneration
	var diagnostics []HistoryDiagnostic
	diag := func(code string, e jobdb.ExecutionHistoryEntry, err error) {
		diagnostics = append(diagnostics, HistoryDiagnostic{Code: code, Message: err.Error(), Ordinal: e.Ordinal})
	}
	for _, e := range run.Execution {
		r := e.Record
		if r.Version != jobdb.ExecutionPatternVersion {
			return nil, nil, &HistoryError{Kind: ErrHistoryUnsupported, Capability: "execution_version", Cause: fmt.Errorf("version %d", r.Version)}
		}
		if r.Kind == "task" {
			var d historyTaskDescriptor
			if err := json.Unmarshal(r.Descriptor, &d); err != nil {
				diag("malformed_task", e, err)
				continue
			}
			tasks[r.ID] = d
		}
		if r.Kind != "result" || tasks[historyTaskID(r.ID)].Call.TaskType != "input:generate_form" {
			continue
		}
		if r.ErrorKind != "" {
			diag("failed_generation", e, fmt.Errorf("form generation did not succeed: %s", r.ID))
			continue
		}
		raw, _, err := historyOutput(r.Output)
		if err != nil {
			if _, ok := err.(*HistoryError); ok {
				return nil, nil, err
			}
			diag("missing_form", e, err)
			continue
		}
		d := historyEntry(key, r.ID, raw, e.Ordinal)
		d.EntryID = historyID("ihe1_", key.TenantId, key.JobId, r.ID, strconv.FormatInt(e.Ordinal, 10))
		ord := e.Ordinal
		d.Locator.GenerationOrdinal = &ord
		if cmd, err := historyFields(tasks[historyTaskID(r.ID)].Call.Command); err == nil {
			d.Locator.Path = historyString(cmd["node"])
		}
		if _, m, err := s.inputSnapshot(ctx, key, r.Input); err == nil {
			d.RequestManifest = historyManifest(d.EntryID, raw, m, r.Input, "request")
		} else {
			if errors.Is(err, ErrStorage) {
				return nil, nil, err
			}
			diag("missing_generation_input", e, err)
		}
		d.RequestManifest = historyMergeMembers(d.RequestManifest, historyManifest(d.EntryID, raw, nil, r.Output, "request"))
		// Artifacts produced by generation belong to the frozen request even when
		// they are not documents or structured refs. Inherited payloads do not.
		if r.Output != nil {
			direct := *r.Output
			direct.Artifacts = nil
			for _, a := range r.Output.Artifacts {
				if a.Key != nil && a.Key.JobId == key.JobId && a.Key.TaskOrdinal == e.Ordinal {
					direct.Artifacts = append(direct.Artifacts, a)
				}
			}
			d.RequestManifest = historyMergeMembers(d.RequestManifest, historyManifest(d.EntryID, nil, nil, &direct, "response"))
		}
		gens = append(gens, &historyGeneration{id: r.ID, scope: historyScope(r.ID), ordinal: e.Ordinal, raw: raw, entry: d})
	}
	entries := []JobInputStoryEntryDetail{}
	byCollection := map[string]int{}
	ordinals := map[string]int64{}
	usedGen := map[string]int{}
	for _, e := range run.Execution {
		r := e.Record
		taskID := historyTaskID(r.ID)
		typ := tasks[taskID].Call.TaskType
		if r.Kind == "dispatch" {
			dispatch[r.ID] = r.ExecutionTaskType
			continue
		}
		if typ != "input:collect_user_input" && typ != "auto-fill-input:echo" && typ != "input-alternate:complete" {
			continue
		}
		if r.Kind != "wait" && r.Kind != "result" {
			continue
		}
		index, exists := byCollection[taskID]
		if !exists {
			raw, m, err := s.inputSnapshot(ctx, key, r.Input)
			if err != nil {
				if errors.Is(err, ErrStorage) {
					return nil, nil, err
				}
				diag("missing_frozen_input", e, err)
				continue
			}
			var candidates []*historyGeneration
			scopedGenerations := 0
			for _, g := range gens {
				if g.ordinal <= e.Ordinal && g.scope != "" && g.scope == historyScope(r.ID) {
					scopedGenerations++
				}
				if g.ordinal <= e.Ordinal && g.scope != "" && g.scope == historyScope(r.ID) && historyFrozenMatches(g.raw, raw) {
					candidates = append(candidates, g)
				}
			}
			var d JobInputStoryEntryDetail
			creation := e.Ordinal
			if len(candidates) == 1 {
				g := candidates[0]
				d = historyCopy(g.entry)
				creation = g.ordinal
				if g.uses > 0 {
					d.EntryID = historyID("ihe1_", key.TenantId, key.JobId, g.id, strconv.FormatInt(g.ordinal, 10), taskID)
					creation = e.Ordinal
				}
				g.uses++
				usedGen[g.id]++
			} else {
				d = historyEntry(key, "collection:"+taskID, raw, e.Ordinal)
				if len(candidates) == 0 && scopedGenerations > 0 {
					d.Diagnostics = append(d.Diagnostics, HistoryDiagnostic{Code: "frozen_form_mismatch", Message: "enclosing generation exists but no saved form matches; collection retained separately", EntryID: d.EntryID, Ordinal: e.Ordinal})
				}
				if len(candidates) > 1 {
					d.Diagnostics = append(d.Diagnostics, HistoryDiagnostic{Code: "ambiguous_lineage", Message: "multiple successful generations share frozen form and enclosing invocation", EntryID: d.EntryID, Ordinal: e.Ordinal})
				}
			}
			for i := range d.RequestManifest {
				d.RequestManifest[i].ArtifactID = historyID("iha1_", d.EntryID, d.RequestManifest[i].Ref.Identity())
			}
			if d.Locator.GenerationOrdinal == nil || len(candidates) == 1 && candidates[0].uses > 1 {
				d.RequestManifest = historyMergeMembers(d.RequestManifest, historyManifest(d.EntryID, raw, m, r.Input, "request"))
			}
			d.Locator.CollectionInvocationIDs = []string{taskID}
			index = len(entries)
			entries = append(entries, d)
			byCollection[taskID] = index
			ordinals[d.EntryID] = creation
		}
		d := &entries[index]
		d.Locator.CollectionOrdinals = append(d.Locator.CollectionOrdinals, e.Ordinal)
		if r.Kind == "wait" && !d.AcceptedResponse.Present {
			d.State = "pending"
			d.StateEvidence = []string{r.ID + ":wait"}
		}
		if r.Kind != "result" {
			continue
		}
		if r.ErrorKind != "" {
			if !d.AcceptedResponse.Present {
				d.State = "unknown"
				d.StateEvidence = []string{r.ID + ":" + r.ErrorKind}
			}
			continue
		}
		raw, outer, err := historyOutput(r.Output)
		if err != nil {
			if _, ok := err.(*HistoryError); ok {
				return nil, nil, err
			}
			diag("missing_accepted_output", e, err)
			continue
		}
		if d.AcceptedResponse.Present {
			d.Diagnostics = append(d.Diagnostics, HistoryDiagnostic{Code: "multiple_accepted_results", Message: "collection has more than one committed success", EntryID: d.EntryID, Ordinal: e.Ordinal})
			continue
		}
		accepted, _ := historyFields(raw)
		d.AcceptedResponse = HistoryValue{Present: true, Raw: raw, Source: r.ID + "/output"}
		d.RawAcceptedOutput = raw
		d.State = "answered"
		d.StateEvidence = []string{r.ID + ":result"}
		ord := e.Ordinal
		d.Locator.AcceptedOrdinal = &ord
		actual := typ
		if dispatch[r.ID] != "" {
			actual = dispatch[r.ID]
		}
		switch actual {
		case "auto-fill-input:echo":
			d.ResponseSource = "autofill"
		case "input-alternate:complete":
			d.ResponseSource = "unanswered_fallback"
			d.State = "defaulted"
		default:
			d.ResponseSource = "external_submission"
		}
		if receipt, ok := accepted["receipt"]; ok && string(receipt) != "null" {
			// Provenance follows the committed producer, never caller-chosen actor
			// fields. Ordinary autofill returns form.Output (including Receipt)
			// unchanged; guarded autofill and alternate generate their receipts.
			provenance := "reported_receipt"
			switch d.ResponseSource {
			case "external_submission":
				provenance = "submission_receipt"
			case "unanswered_fallback":
				provenance = "automation_receipt"
			case "autofill":
				if d.Kind == "review" || d.Kind == "structured" {
					provenance = "automation_receipt"
				}
			}
			d.Actor = HistoryActor{Receipt: receipt, Provenance: provenance}
		} else {
			d.Actor.LegacyUserID = historyField(accepted, "user_id", "/user_id")
			if d.Actor.LegacyUserID.Present {
				d.Actor.Provenance = "reported_legacy"
			}
		}
		direct := *r.Output
		direct.Artifacts = nil
		for _, a := range r.Output.Artifacts {
			if a.Key != nil && a.Key.JobId == key.JobId && a.Key.TaskOrdinal == e.Ordinal {
				direct.Artifacts = append(direct.Artifacts, a)
			}
		}
		d.ResponseManifest = historyManifest(d.EntryID, raw, outer, &direct, "response")
	}
	for _, g := range gens {
		if usedGen[g.id] == 0 {
			entries = append(entries, g.entry)
			ordinals[g.entry.EntryID] = g.ordinal
		}
	}
	if len(run.Execution) == 0 {
		diagnostics = append(diagnostics, HistoryDiagnostic{Code: "execution_unavailable", Message: "no retained versioned execution records; history coverage is unknown"})
	}
	if s.mapNode != nil {
		for i := range entries {
			node, err := s.mapNode(ctx, key, entries[i].Locator)
			if err == nil {
				entries[i].Locator.StoryNodeID = node
			} else {
				entries[i].Diagnostics = append(entries[i].Diagnostics, HistoryDiagnostic{Code: "node_mapping_unavailable", Message: err.Error(), EntryID: entries[i].EntryID})
			}
		}
	}
	for i := range entries {
		d := &entries[i]
		if d.Kind == "unknown" {
			d.Diagnostics = append(d.Diagnostics, HistoryDiagnostic{Code: "unknown_form_kind", Message: "recorded form kind is unsupported", EntryID: d.EntryID})
		}
		diagnostics = append(diagnostics, d.Diagnostics...)
	}
	historySort(entries, ordinals)
	return entries, diagnostics, nil
}

func (s *historyService) observe(ctx context.Context, key jobdb.JobKey, d JobInputStoryEntryDetail) HistoryActionability {
	a := HistoryActionability{Observation: "unknown", ObservedAt: time.Now().UTC(), Reason: "authoritative occurrence unavailable"}
	if d.Kind != "review" && d.Kind != "structured" || d.RequestID == "" {
		a.Observation = "read_only"
		a.Reason = "no guarded historical submission contract"
		return a
	}
	// Load a fresh prefix before capturing the exact task handle. An intervening
	// advancement changes the completion ordinal, even when RequestID is reused.
	run, err := s.reader.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true})
	if err != nil || run.Job.JobKey != key {
		return a
	}
	var high int64
	for _, e := range run.Execution {
		high = max(high, e.Ordinal)
	}
	task, err := s.reader.GetWaitingTask(ctx, key)
	if err != nil || task == nil || task.JobKey() != key || task.TaskType() != "input:collect_user_input" || task.TaskOrdinalToComplete() != high+1 {
		return a
	}
	latest, _, err := s.project(ctx, key, run)
	if err != nil {
		return a
	}
	match := false
	for _, e := range latest {
		if e.EntryID == d.EntryID && e.RequestID == d.RequestID && e.State == "pending" {
			match = true
		}
	}
	if !match {
		a.Observation = "not_current"
		a.Reason = "occurrence does not match the current wait"
		return a
	}
	td, err := task.Data()
	if err != nil {
		return a
	}
	raw, err := td.GetData()
	if err != nil {
		return a
	}
	attached, err := td.GetArtifacts()
	if err != nil {
		return a
	}
	snap := &jobdb.ExecutionSnapshot{Data: raw}
	for _, at := range attached {
		k, e := at.ArtifactKey()
		if e != nil {
			return a
		}
		snap.Artifacts = append(snap.Artifacts, jobdb.ExecutionArtifact{Name: at.Name(), Key: &k})
	}
	form, _, err := s.inputSnapshot(ctx, key, snap)
	if err != nil {
		return a
	}
	fields, _ := historyFields(form)
	if historyString(fields["request_id"]) != d.RequestID || !historyFrozenMatches(form, d.RawForm) {
		return a
	}
	a.Actionable = true
	a.Observation = "matching_wait"
	a.Reason = "authoritative occurrence and frozen request match; submit must recheck"
	a.ObservedAt = time.Now().UTC()
	return a
}

func historyMergeMembers(a, b []HistoryArtifactMember) []HistoryArtifactMember {
	for _, m := range b {
		found := false
		for i := range a {
			if a[i].Ref.Identity() == m.Ref.Identity() {
				for _, assoc := range m.Associations {
					present := false
					for _, v := range a[i].Associations {
						if v == assoc {
							present = true
						}
					}
					if !present {
						a[i].Associations = append(a[i].Associations, assoc)
					}
				}
				found = true
				break
			}
		}
		if !found {
			a = append(a, m)
		}
	}
	sort.Slice(a, func(i, j int) bool { return a[i].ArtifactID < a[j].ArtifactID })
	return a
}
func historyManifest(entry string, raw json.RawMessage, outer map[string]json.RawMessage, snap *jobdb.ExecutionSnapshot, phase string) []HistoryArtifactMember {
	out := []HistoryArtifactMember{}
	add := func(ref artifacts.Ref, kind, pointer string) {
		if key, ok := ref.StoredKey(); ok && historyPrivateArtifact(key.Name) {
			return
		}
		availability, reason := "unsupported", "reference is not a retained stored snapshot"
		if k, ok := ref.StoredKey(); ok && k.Validate() == nil {
			availability = "retained"
			reason = ""
		}
		if ref.Kind == artifacts.RefKindAttached {
			availability = "unavailable"
			reason = "containing result attachment map unavailable"
		}
		m := HistoryArtifactMember{ArtifactID: historyID("iha1_", entry, ref.Identity()), Ref: ref, Associations: []HistoryAssociation{{Kind: kind, Source: pointer}}, Availability: availability, Reason: reason}
		out = historyMergeMembers(out, []HistoryArtifactMember{m})
	}
	var walk func(json.RawMessage, string, string, map[string]artifacts.Ref, string)
	walk = func(raw json.RawMessage, pointer, kind string, attachmentMap map[string]artifacts.Ref, mapSource string) {
		if m, err := historyFields(raw); err == nil {
			if _, ok := m["kind"]; ok {
				var ref artifacts.Ref
				if json.Unmarshal(raw, &ref) == nil && (ref.Kind == artifacts.RefKindStored || ref.Kind == artifacts.RefKindExternal || ref.Kind == artifacts.RefKindAttached || ref.Kind == artifacts.RefKindValue) {
					if ref.Kind == artifacts.RefKindAttached {
						if resolved, ok := attachmentMap[ref.NameValue()]; ok {
							if _, stored := resolved.StoredKey(); stored {
								add(resolved, kind, pointer)
								for i := range out {
									if out[i].Ref.Identity() == resolved.Identity() {
										original := ref
										out[i].OriginalRef = &original
										out[i].ResolutionSource = mapSource
									}
								}
								return
							}
						}
					}
					add(ref, kind, pointer)
					return
				}
			}
			// recipeJobOutput is persisted with its exported Outputs/Artifacts
			// fields. A child marker resolves only inside that containing result.
			if _, ok := m["Outputs"]; ok {
				var recorded map[string]artifacts.Ref
				if json.Unmarshal(m["Artifacts"], &recorded) == nil && recorded != nil {
					attachmentMap = recorded
					mapSource = pointer + "/Artifacts"
				} else {
					attachmentMap = nil
					mapSource = ""
				}
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(m[k], pointer+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), kind, attachmentMap, mapSource)
			}
			return
		}
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) == nil {
			for i, v := range list {
				walk(v, fmt.Sprintf("%s/%d", pointer, i), kind, attachmentMap, mapSource)
			}
		}
	}
	m, _ := historyFields(raw)
	if phase == "request" {
		walk(m["documents"], "/documents", "document", nil, "")
		walk(m["request"], "/request", "request_attachment", nil, "")
		walk(outer["artifacts"], "/invocation/artifacts", "step_input", nil, "")
		if context, err := historyFields(m["context"]); err == nil {
			for _, k := range []string{"artifacts", "artifacts_glob", "artifacts_from_output"} {
				if v, ok := context[k]; ok {
					add(artifacts.Ref{Kind: artifacts.RefKindValue, Name: "context:" + k, Format: "json", Value: v}, "request_attachment", "/context/"+k)
				}
			}
		}
	} else {
		walk(raw, "/accepted_output", "response_attachment", nil, "")
		// The invocation wrapper forwards inherited bindings. Accepted output refs
		// and attachments produced at this result define response membership.
	}
	// Only attachments explicitly named by invocation artifact_keys are step
	// input attachments. Other snapshot attachments may be inherited/private.
	var keys []jobdb.ArtifactKey
	_ = json.Unmarshal(outer["artifact_keys"], &keys)
	if phase == "request" {
		for _, k := range keys {
			add(artifacts.NewStoredRef(k), "step_input", "/invocation/artifact_keys")
		}
	}
	if phase == "response" && snap != nil {
		for _, a := range snap.Artifacts {
			if a.Key != nil && a.Key.JobId != "" && !historyPrivateArtifact(a.Name) {
				add(artifacts.NewStoredRef(*a.Key), "step_output", "/output/attachments/"+a.Name)
			}
		}
	}
	return out
}

func historyPrivateArtifact(name string) bool {
	if strings.HasPrefix(name, "__c2j_objects__/") {
		return true
	}
	switch name {
	case "c2j-invocation-input.json", "__git_state_thin_pack__", "diff_from_parent.diff", "diff_from_base.diff":
		return true
	}
	return false
}

func historyJobAttempt(id string) int {
	i := strings.Index(id, "/attempt:")
	if i < 0 {
		return 0
	}
	part := id[i+9:]
	if n := strings.IndexByte(part, '/'); n >= 0 {
		part = part[:n]
	}
	v, _ := strconv.Atoi(part)
	return v
}
