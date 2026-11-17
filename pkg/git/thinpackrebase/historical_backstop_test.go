package thinpackrebase

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These are reduced regression cases, not reconstructed application repositories.
// The source failures and Backstop interventions are identified in
// testdata/repros/rebase-shared-history/historical-backstop.md.
func TestRunThinpackRebase_HistoricalBackstop(t *testing.T) {
	for _, tc := range []struct {
		name          string
		job           string
		upstreamFiles []string
		versions      []string
		localFiles    []string
	}{
		{
			name: "cortex-review-history",
			job:  "3M7OO6GyLMkFPvhI8JNaZ4STGPe",
			upstreamFiles: []string{
				"web/app/src/components/InputDetailPage.tsx",
				"web/app/src/components/ReviewDocuments.tsx",
			},
			versions:   []string{"original review", "review v2", "revised review v2"},
			localFiles: []string{"web/app/src/components/DagActions.tsx"},
		},
		{
			name:          "c2j-dependency-upgrade",
			job:           "Wk8CYgJjhGya9Q8hRfMGC9znqgc",
			upstreamFiles: []string{"go.mod", "go.sum"},
			versions:      []string{"jobdb v0.0.85", "jobdb v0.0.86", "jobdb v0.0.88"},
			localFiles:    []string{"pkg/story/download_limit.go"},
		},
		{
			name: "recipes-manager-history",
			job:  "3M797193dahkOPVxg2yDd52q0b9",
			upstreamFiles: []string{
				"recipe-tests/adaptive3/packages.test.yaml",
				"recipe-tests/adaptive3/statements.md",
				"recipe-tests/adaptive3/tools.test.yaml",
			},
			versions:   []string{"original validator", "package restore fix", "updated validator"},
			localFiles: []string{"recipes/adaptive3/manager.yaml", "recipe-tests/adaptive3/manager.test.yaml"},
		},
	} {
		for _, advance := range []bool{false, true} {
			name := tc.name + "/incorporated-target"
			if advance {
				name = tc.name + "/later-target"
			}
			t.Run(name, func(t *testing.T) {
				repo := t.TempDir()
				initRepo(t, repo)
				commitFiles := func(paths []string, content, message string) {
					for _, path := range paths {
						path = filepath.Join(repo, path)
						require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
						require.NoError(t, os.WriteFile(path, []byte(content+"\n"), 0644))
					}
					runGit(t, repo, "git", "add", ".")
					runGit(t, repo, "git", "commit", "-m", message)
				}
				commitFiles(tc.upstreamFiles, tc.versions[0], "recorded managed base")
				base := gitRevParse(t, repo, "HEAD")
				if tc.name == "cortex-review-history" {
					// The historical replay dropped an already-published cleanup
					// before conflicting on the shared review-interface commit.
					commitFiles([]string{"cleanup"}, "published cleanup", "already published cleanup")
				}
				commitFiles(tc.upstreamFiles, tc.versions[1], "shared upstream change")
				shared := gitRevParse(t, repo, "HEAD")
				commitFiles(tc.upstreamFiles, tc.versions[2], "newer upstream content")
				incorporated := gitRevParse(t, repo, "HEAD")
				for i, path := range tc.localFiles {
					commitFiles([]string{path}, "approved local work "+strconv.Itoa(i), "approved local work "+strconv.Itoa(i))
				}
				candidate := gitRevParse(t, repo, "HEAD")
				candidateTree := gitRevParse(t, repo, "HEAD^{tree}")
				target, expectedTree := incorporated, candidateTree
				if advance {
					runGit(t, repo, "git", "checkout", "--detach", incorporated)
					commitFiles(tc.upstreamFiles, "later upstream content", "pinned upstream advances again")
					target = gitRevParse(t, repo, "HEAD")
					// Independently construct the expected tree from the pinned
					// target and the approved local files, without native replay.
					for i, path := range tc.localFiles {
						commitFiles([]string{path}, "approved local work "+strconv.Itoa(i), "expected local work")
					}
					expectedTree = gitRevParse(t, repo, "HEAD^{tree}")
					runGit(t, repo, "git", "checkout", "--detach", candidate)
				}
				// Establish the defining historical property before invoking the
				// operation: the authoritative base is older than shared ancestry.
				require.NotEqual(t, base, incorporated)
				runGit(t, repo, "git", "merge-base", "--is-ancestor", base, shared)
				require.Equal(t, incorporated, strings.TrimSpace(runGitOutput(t, repo, "git", "merge-base", candidate, target)))
				require.Equal(t, strconv.Itoa(len(tc.localFiles)), strings.TrimSpace(runGitOutput(t, repo, "git", "rev-list", "--count", target+".."+candidate)))
				input := ThinpackRebaseInput{
					RepoPath: repo, BaseHash: base, PersistHash: candidate,
					TargetBaseHash: target, GitAuthor: "Bot <bot@example.com>",
				}
				confirmed, err := json.Marshal(input)
				require.NoError(t, err)
				t.Logf("historical source job=%s shared=%s incorporated=%s candidate_tree=%s expected_tree=%s input=%s", tc.job, shared, incorporated, candidateTree, expectedTree, confirmed)
				out, err := Run(context.Background(), input)
				if err != nil {
					// A failing old operation must still restore the original clean
					// candidate; this does not make its acceptance result a pass.
					require.Equal(t, candidate, gitRevParse(t, repo, "HEAD"))
					require.Equal(t, candidateTree, gitRevParse(t, repo, "HEAD^{tree}"))
					require.NoError(t, requireCleanIntegration(context.Background(), repo))
					t.Logf("rejected replay restored the clean original candidate: %v", err)
				}
				require.NoError(t, err, "shared upstream commits must not be replayed as local work")
				require.Equal(t, target, out.NewBaseHash)
				require.Equal(t, target, out.TargetBaseHash)
				require.Equal(t, base, out.RebasedFrom.BaseHash)
				require.Equal(t, candidate, out.RebasedFrom.PersistHash)
				require.Equal(t, out.NewPersistHash, gitRevParse(t, repo, "HEAD"))
				require.Equal(t, expectedTree, gitRevParse(t, repo, "HEAD^{tree}"))
				require.Equal(t, strconv.Itoa(len(tc.localFiles)), strings.TrimSpace(runGitOutput(t, repo, "git", "rev-list", "--count", target+"..HEAD")))
				require.Equal(t, target, gitRevParse(t, repo, "HEAD"+strings.Repeat("^", len(tc.localFiles))))
				if !advance {
					require.Equal(t, candidateTree, gitRevParse(t, repo, "HEAD^{tree}"), "an already incorporated target must preserve the complete candidate tree")
				}
				require.NoError(t, requireCleanIntegration(context.Background(), repo))
				observed, err := json.Marshal(out)
				require.NoError(t, err)
				t.Logf("accepted native output=%s tree=%s clean=true", observed, gitRevParse(t, repo, "HEAD^{tree}"))
			})
		}
	}
}
