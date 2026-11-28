//go:build integration

package joblabel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/colony-2/c2j/pkg/joblabel"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

func readLabelStateHTTP(t *testing.T, ctx context.Context, client jobdb.ConsumerRuntime, job jobdb.JobKey, shape, key string) joblabel.State {
	t.Helper()
	if shape == "exact" {
		state, err := joblabel.GetJobLabel(ctx, client, job, key)
		require.NoError(t, err)
		return state
	}
	page, err := client.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{job.TenantId}})
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	compact := joblist.JobFromSummary(page.Jobs[0])
	jobView := func(value any) joblabel.View {
		wire, err := json.Marshal(value)
		require.NoError(t, err)
		var view joblabel.View
		require.NoError(t, json.Unmarshal(wire, &view))
		return view
	}
	view := jobView(compact)
	recipe, ok, err := recipejob.RecipeJobFromSummary(page.Jobs[0])
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, view, jobView(recipe))
	state, err := view.StateFor(key)
	require.NoError(t, err)
	return state
}

func TestJobLabelHTTPConfirmedEmptyCAS(t *testing.T) {
	for _, source := range []string{"compact", "exact"} {
		t.Run(source, func(t *testing.T) {
			ctx, db, job := fixture(t)
			handler := remote.NewConsumerServer(db)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				handler.ServeHTTP(writer, request.WithContext(celltest.Context(request.Context())))
			}))
			t.Cleanup(server.Close)
			client, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			read := func(shape, key string) joblabel.State {
				return readLabelStateHTTP(t, ctx, client, job, shape, key)
			}
			var first *joblabel.Version
			for _, shape := range []string{"compact", "exact"} {
				initial := read(shape, "friend")
				require.Equal(t, joblabel.State{Status: joblabel.Absent, Version: new(joblabel.Version)}, initial, "never-written/%s", shape)
				if shape == source {
					first = initial.Version
				}
			}
			added, err := joblabel.SetJobLabel(ctx, client, job, "friend", "foe", joblabel.Blue, *first)
			assertReceipt(t, added, err, joblabel.Present, 1)
			require.Equal(t, &joblabel.Label{Key: "friend", Value: "foe", Color: joblabel.Blue, Version: 1}, added.Label)
			for _, shape := range []string{"compact", "exact"} {
				require.Equal(t, added, read(shape, "friend"), "labeled/%s", shape)
				require.Equal(t, joblabel.State{Status: joblabel.Absent, Version: new(joblabel.Version)}, read(shape, "other"), "labeled/%s", shape)
			}
			_, err = joblabel.SetJobLabel(ctx, client, job, "friend", "stale", joblabel.Red, *first)
			require.ErrorIs(t, err, jobdb.ErrConflict)
			for _, shape := range []string{"compact", "exact"} {
				require.Equal(t, added, read(shape, "friend"), "after stale write/%s", shape)
			}
			removed, err := joblabel.RemoveJobLabel(ctx, client, job, "friend", *added.Version)
			assertReceipt(t, removed, err, joblabel.Tombstone, 2)
			for _, shape := range []string{"compact", "exact"} {
				require.Equal(t, removed, read(shape, "friend"), "tombstoned/%s", shape)
				require.Equal(t, joblabel.State{Status: joblabel.Absent, Version: new(joblabel.Version)}, read(shape, "other"), "tombstoned/%s", shape)
			}
		})
	}
}

func TestJobLabelHTTPUnconfirmedProjections(t *testing.T) {
	const visibleEntries = `,"extendedMetadata":{"c2j.label.visible":{"present":true,"version":"7","value":{"value":"kept","color":"blue"}},"c2j.label.removed":{"present":false,"version":"9"}}`
	job := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	for _, scenario := range []struct {
		name, metadata string
		visible        bool
	}{
		{"unsupported", `,"extendedMetadataComplete":false`, false},
		{"older-peer-omitted", ``, false},
		{"older-peer-empty-object", `,"extendedMetadata":{}`, false},
		{"scope-redacted", visibleEntries + `,"extendedMetadataComplete":false`, true},
		{"true-without-object", `,"extendedMetadataComplete":true`, false},
		{"true-with-null-object", `,"extendedMetadataComplete":true,"extendedMetadata":null`, false},
		{"nonempty-without-attestation", visibleEntries, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			const jobs = "/v1/tenants/tenant/jobs"
			item := `{"tenantId":"tenant","jobId":"job","jobType":"recipe","status":"pending","goalId":"job","otherGoalIds":[],"clientPayloadRevision":"0"` + scenario.metadata + `}`
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				switch {
				case request.Method == http.MethodGet && request.URL.Path == jobs+"/job":
					fmt.Fprint(writer, item)
				case request.Method == http.MethodPost && request.URL.Path == jobs+"/query":
					fmt.Fprintf(writer, `{"jobs":[%s]}`, item)
				default:
					t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
					http.NotFound(writer, request)
				}
			}))
			t.Cleanup(server.Close)
			client, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			for _, shape := range []string{"compact", "exact"} {
				t.Run(shape, func(t *testing.T) {
					read := func(key string) joblabel.State {
						return readLabelStateHTTP(t, t.Context(), client, job, shape, key)
					}
					require.Equal(t, joblabel.State{Status: joblabel.Unknown}, read("missing"))
					if scenario.visible {
						version, tombstone := joblabel.Version(7), joblabel.Version(9)
						require.Equal(t, joblabel.State{Status: joblabel.Present, Version: &version, Label: &joblabel.Label{Key: "visible", Value: "kept", Color: joblabel.Blue, Version: version}}, read("visible"))
						require.Equal(t, joblabel.State{Status: joblabel.Tombstone, Version: &tombstone}, read("removed"))
					}
				})
			}
		})
	}
}
