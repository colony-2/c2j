package compiler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

// The fixture is a real completed job recorded by unmodified f1a153b898c5,
// before schema_version or the new validator existed. Both root and included
// recipe accept a checkpoint as type: object; a task reads its contract name.
func TestReplayPreSchemaVersionHistory(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	in, err := os.Open("testdata/schema_v1_history.db.gz")
	require.NoError(t, err)
	defer in.Close()
	zipped, err := gzip.NewReader(in)
	require.NoError(t, err)
	defer zipped.Close()
	path := filepath.Join(t.TempDir(), "jobs.db")
	out, err := os.Create(path)
	require.NoError(t, err)
	_, err = io.Copy(out, zipped)
	require.NoError(t, err)
	require.NoError(t, out.Close())
	raw, err := os.ReadFile("testdata/schema_v1_history.key.json")
	require.NoError(t, err)
	var key jobdb.JobKey
	require.NoError(t, json.Unmarshal(raw, &key))
	ctx := context.Background()
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
	require.NoError(t, err)
	defer rt.Close(ctx)
	before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(t, err)
	resolver := &conventionResolver{
		resolve: func(string) (RecipeSourceResolution, error) {
			return RecipeSourceResolution{}, fmt.Errorf("original source is unavailable")
		},
		load: func(RecipeSourceResolution) ([]byte, error) { return nil, fmt.Errorf("original source is unavailable") },
	}
	// No task workers are supplied. Completed tasks must replay from history.
	require.NoError(t, probeReplay(t, rt, key, NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true, RootSourceResolver: resolver})))
	after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
	require.NoError(t, err)
	require.Equal(t, before, after, "legacy history must remain untouched")
}
