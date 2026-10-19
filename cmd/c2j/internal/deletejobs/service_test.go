package deletejobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func cellConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".c2j"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".c2j", "config.yaml"), []byte("pattern: 'github.com/acme/${{ cell }}'\nself:\n  repo: backend\n"), 0644))
	return dir
}

func TestBuildRequestCombinesSelectors(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	opts := Options{WorkingDir: cellConfig(t), Cells: []string{"backend", "frontend"}, Recipe: "build", JobIDs: []string{"one", "two"}, OlderThan: "720h", DryRun: true}
	req, selection, err := buildRequest(context.Background(), opts, "tenant", now)
	require.NoError(t, err)
	require.Equal(t, "tenant", req.TenantID)
	require.Equal(t, opts.JobIDs, req.JobIDs)
	require.Equal(t, now.Add(-720*time.Hour), *req.CreatedBefore)
	require.True(t, req.DryRun)
	require.False(t, req.All)
	predicates, err := jobdb.MetadataPredicates(req.MetadataFilter)
	require.NoError(t, err)
	require.Equal(t, []jobdb.MetadataPredicate{
		{Path: []string{"repo"}, Values: []any{"https://github.com/acme/backend.git", "https://github.com/acme/frontend.git"}},
		{Path: []string{"recipe"}, Values: []any{"build"}},
	}, predicates)
	require.Equal(t, []string{"https://github.com/acme/backend.git", "https://github.com/acme/frontend.git"}, selection.Repositories)
	require.Equal(t, req.CreatedBefore, selection.CreatedBefore)

	req, selection, err = buildRequest(context.Background(), Options{WorkingDir: opts.WorkingDir, Self: true}, "tenant", now)
	require.NoError(t, err)
	require.Equal(t, []string{"https://github.com/acme/backend.git"}, selection.Repositories)
	predicates, err = jobdb.MetadataPredicates(req.MetadataFilter)
	require.NoError(t, err)
	require.Len(t, predicates, 1)
}

func TestBuildRequestExplicitTenantWideSelection(t *testing.T) {
	for _, opts := range []Options{
		{All: true}, {JobIDs: []string{" exact:id,with-comma "}},
		{CreatedBefore: "2026-10-19T12:00:00+02:00"}, {Recipe: "build"},
	} {
		req, selection, err := buildRequest(context.Background(), opts, "tenant", time.Now())
		require.NoError(t, err)
		require.Empty(t, selection.Repositories, "must not infer current cell")
		require.Equal(t, opts.JobIDs, req.JobIDs, "IDs are opaque")
		if req.CreatedBefore != nil {
			require.Equal(t, "2026-10-19T10:00:00Z", req.CreatedBefore.Format(time.RFC3339))
		}
	}
}

func TestBuildRequestRejectsInvalidSelection(t *testing.T) {
	for name, opts := range map[string]Options{
		"empty": {}, "dry run only": {DryRun: true}, "empty id": {JobIDs: []string{""}},
		"empty cell": {Cells: []string{" "}}, "empty recipe": {Recipe: " "},
		"self and cell": {Self: true, Cells: []string{"backend"}},
		"all and self":  {All: true, Self: true}, "all and cells": {All: true, Cells: []string{"backend"}},
		"all and ids": {All: true, JobIDs: []string{"one"}}, "all and recipe": {All: true, Recipe: "build"},
		"all and age": {All: true, OlderThan: "1h"}, "all and cutoff": {All: true, CreatedBefore: "2026-10-19T00:00:00Z"},
		"both ages": {OlderThan: "1h", CreatedBefore: "2026-10-19T00:00:00Z"},
		"bad age":   {OlderThan: "30d"}, "zero age": {OlderThan: "0s"}, "negative age": {OlderThan: "-1h"},
		"bad cutoff": {CreatedBefore: "yesterday"}, "overflow cutoff": {CreatedBefore: "9999-01-01T00:00:00Z"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildRequest(context.Background(), opts, "tenant", time.Now())
			require.ErrorIs(t, err, jobdb.ErrInvalidJobDeletion)
		})
	}
}
