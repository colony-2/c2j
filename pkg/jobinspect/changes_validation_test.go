package jobinspect

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type changesUnusedRuntime struct{ jobdb.ConsumerRuntime }

func TestChangesU01Selectors(t *testing.T) {
	h := strings.Repeat("a", 40)
	for _, q := range []DiffRequest{{All: true}, {}, {Commit: h}, {From: h, Through: h}} {
		require.NoError(t, validateDiffRequest(q))
	}
	for _, q := range []DiffRequest{{All: true, Commit: h}, {All: true, From: h, Through: h}, {Commit: h, From: h, Through: h}, {Commit: "abc"}, {From: h}, {Through: h}, {ChangesRequest: ChangesRequest{Cursor: "x"}}, {ChangesRequest: ChangesRequest{Limit: 1}}} {
		require.Error(t, validateDiffRequest(q))
	}
}

func TestChangesU02SelectionIdentity(t *testing.T) {
	h := strings.Repeat("a", 40)
	id := ChangesIdentity{Version: 1, Tenant: "tenant", JobID: "job", Repository: "example.test/repo", Scope: "main", State: ChangesProducer{JobID: "job", Task: "task", Attempt: 1}, Boundary: "after", BaseOrigin: "active", ActiveBaseOrigin: "workspace_after.base_commit", Base: h, Head: h, BaseTree: h, HeadTree: h, ObjectFormat: "sha1", Sources: []ChangesSource{{Producer: ChangesProducer{JobID: "job", Task: "task", Attempt: 1}, Key: jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 2, Name: "source", SizeBytes: 123}}}}
	before := changesDigest(id)
	raw, e := json.Marshal(id)
	require.NoError(t, e)
	var same ChangesIdentity
	require.NoError(t, json.Unmarshal(raw, &same))
	require.Equal(t, before, changesDigest(same))
	for name, mutate := range map[string]func(*ChangesIdentity){
		"version": func(v *ChangesIdentity) { v.Version++ }, "tenant": func(v *ChangesIdentity) { v.Tenant = "other" }, "job": func(v *ChangesIdentity) { v.JobID = "other" },
		"repository": func(v *ChangesIdentity) { v.Repository = "other" }, "scope": func(v *ChangesIdentity) { v.Scope = "other" }, "task": func(v *ChangesIdentity) { v.State.Task = "other" },
		"attempt": func(v *ChangesIdentity) { v.State.Attempt++ }, "boundary": func(v *ChangesIdentity) { v.Boundary = "before" }, "active": func(v *ChangesIdentity) { v.ActiveBase = h },
		"base": func(v *ChangesIdentity) { v.Base = strings.Repeat("b", 40) }, "head": func(v *ChangesIdentity) { v.Head = strings.Repeat("b", 40) }, "tree": func(v *ChangesIdentity) { v.HeadTree = strings.Repeat("b", 40) },
		"format": func(v *ChangesIdentity) { v.ObjectFormat = "sha256" }, "empty": func(v *ChangesIdentity) { v.EmptyBase = true }, "unborn": func(v *ChangesIdentity) { v.Unborn = true },
		"source": func(v *ChangesIdentity) { v.Sources[0].Key.TaskOrdinal++ }, "source_attempt": func(v *ChangesIdentity) { v.Sources[0].Producer.Attempt++ }, "repository_objects": func(v *ChangesIdentity) { v.RepositoryObjects = []string{h} },
	} {
		t.Run(name, func(t *testing.T) {
			var v ChangesIdentity
			require.NoError(t, json.Unmarshal(raw, &v))
			mutate(&v)
			require.NotEqual(t, before, changesDigest(v))
		})
	}
}

func TestChangesU04EveryConfigurationBudget(t *testing.T) {
	s := &Service{Runtime: &changesUnusedRuntime{}, Tenant: "tenant"}
	api, e := NewChangesService(s, ChangesConfig{})
	require.NoError(t, e)
	require.Equal(t, DefaultChangesLimits(), api.config.Limits)
	for i := 0; i < reflect.TypeOf(ChangesLimits{}).NumField(); i++ {
		t.Run(reflect.TypeOf(ChangesLimits{}).Field(i).Name, func(t *testing.T) {
			l := ChangesLimits{}
			reflect.ValueOf(&l).Elem().Field(i).SetInt(-1)
			_, e := NewChangesService(s, ChangesConfig{Limits: l})
			require.Equal(t, "invalid_request", ChangesDiagnostic(e).Code)
		})
	}
	for _, l := range []ChangesLimits{{PageSize: 501}, {ArtifactBytes: 10, DownloadBytes: 9}, {ObjectBytes: 10, InflatedBytes: 9}, {PatchBytes: 10, TemporaryBytes: 9}} {
		_, e := NewChangesService(s, ChangesConfig{Limits: l})
		require.Error(t, e)
	}
}

func TestChangesU05U08ProtocolStates(t *testing.T) {
	h := strings.Repeat("a", 40)
	id := ChangesIdentity{Version: 1, Tenant: "tenant", JobID: "job", Repository: "example.test/repo", State: ChangesProducer{JobID: "job", Task: "task", Attempt: 1}, Boundary: "after", BaseOrigin: "active", EmptyBase: true, Unborn: true, Sources: []ChangesSource{}, SourceID: "source", Selection: "selection"}
	unborn := ChangesHistory{ChangesIdentity: id, History: "complete", Diff: "unavailable", Complete: true, Commits: []ChangesCommit{}}
	require.NoError(t, ValidateChangesHistory(&unborn))
	for name, mutate := range map[string]func(*ChangesHistory){
		"unknown": func(v *ChangesHistory) { v.Unborn = false; v.EmptyBase = false }, "contradictory": func(v *ChangesHistory) { v.Base = h }, "unborn_head": func(v *ChangesHistory) { v.Head = h },
		"version": func(v *ChangesHistory) { v.Version = 999 }, "unborn_format": func(v *ChangesHistory) { v.ObjectFormat = "sha1" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := unborn
			mutate(&copy)
			require.Equal(t, "protocol", ChangesDiagnostic(ValidateChangesHistory(&copy)).Code)
		})
	}
	committed := id
	committed.Unborn = false
	committed.Head = h
	committed.BaseTree = h
	committed.HeadTree = h
	committed.ObjectFormat = "sha1"
	require.NoError(t, validateChangesIdentity(committed))
	committed.EmptyBase = false
	committed.Base = h
	require.NoError(t, validateChangesIdentity(committed))
	committed.Head = "abc"
	require.Error(t, validateChangesIdentity(committed))
	for _, e := range []*ChangesError{{Version: 999, Code: "limit"}, {Version: 1, Code: "unknown"}} {
		require.Error(t, ValidateChangesDiagnostic(e))
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, jobdb.ErrExecutionLeaseLost} {
		raw, e := json.Marshal(ChangesDiagnostic(cause))
		require.NoError(t, e)
		var copy ChangesError
		require.NoError(t, json.Unmarshal(raw, &copy))
		require.True(t, errors.Is(&copy, cause))
	}
}
