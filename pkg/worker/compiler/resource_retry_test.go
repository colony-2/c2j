package compiler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestResourceRetryWorkspaceResolution(t *testing.T) {
	for _, tc := range []struct {
		diagnostic string
		retry      bool
		wantError  string
	}{
		{"fork/exec /usr/bin/git: resource temporarily unavailable", true, ""},
		{"fatal: authentication failed", false, "authentication failed"},
		{"fatal: not a git repository", false, "not a git repository"},
		{"bash: fork: retry: Resource temporarily unavailable\nfatal: Remote branch missing not found in upstream origin", false, "Remote branch missing not found"},
		{"bash: fork: retry: Resource temporarily unavailable\nfatal: cannot fork git-upload-pack: Resource temporarily unavailable", true, ""},
	} {
		t.Run(tc.diagnostic, func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf '%s' '"+tc.diagnostic+"' >&2\nexit 1\n"), 0700))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx := celltest.Context(context.Background())
			ctx = permissions.WithAccess(ctx, permissions.NewAccess(ctx, permissions.JobDB{}, nil, nil))
			input := jobdb.NewTaskDataOrPanic(WorkspaceResolutionInput{Cell: "test", Repo: "example.test/work", Ref: "main", ScopeID: "test", Initial: "empty"})
			out, err := resolveWorkspaceTask(ctx, input)
			require.Error(t, err)
			require.Nil(t, out)
			require.Equal(t, tc.retry, resourceretry.Is(err), "%v", err)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}
