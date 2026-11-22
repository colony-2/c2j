//go:build postpublication

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Run only from a fresh checkout of the actual adaptive3 publication. Missing
// publication inputs fail acceptance; these checks are never pre-merge skips.
func TestInputHistoryPublication(t *testing.T) {
	repository := os.Getenv("C2J_INPUT_HISTORY_PUBLISHED_REPOSITORY")
	ref := os.Getenv("C2J_INPUT_HISTORY_PUBLISHED_REF")
	commit := os.Getenv("C2J_INPUT_HISTORY_PUBLISHED_COMMIT")
	receiptPath := os.Getenv("C2J_INPUT_HISTORY_PUBLICATION_RECEIPT")
	require.Equal(t, "https://github.com/colony-2/c2j.git", repository, "use the actual adaptive3 c2j publication repository")
	require.True(t, strings.HasPrefix(ref, "refs/heads/"), "published ref must be explicit")
	require.Len(t, commit, 40, "published commit is required")
	require.NotEmpty(t, receiptPath, "download the actual merge output, not a synthetic receipt")
	raw, e := os.ReadFile(receiptPath)
	require.NoError(t, e)
	var receipt struct {
		Candidate   string `json:"candidate_sha"`
		Merged      string `json:"merged_hash"`
		Branch      string `json:"publication_branch"`
		Disposition string `json:"publication_disposition"`
	}
	require.NoError(t, json.Unmarshal(raw, &receipt))
	require.Contains(t, []string{"published", "already_present"}, receipt.Disposition, "publication is unconfirmed")
	require.Equal(t, commit, receipt.Candidate)
	if receipt.Merged != "" {
		require.Equal(t, commit, receipt.Merged)
	}
	require.Equal(t, ref, "refs/heads/"+strings.TrimPrefix(receipt.Branch, "refs/heads/"))
	t.Run("R01PublishedIdentity", func(t *testing.T) {
		b, e := exec.Command("git", "ls-remote", "--exit-code", repository, ref).CombinedOutput()
		require.NoError(t, e, string(b))
		rows := strings.Fields(string(b))
		require.Len(t, rows, 2)
		require.Equal(t, commit, rows[0])
		require.Equal(t, ref, rows[1])
		b, e = exec.Command("git", "rev-parse", "HEAD").CombinedOutput()
		require.NoError(t, e)
		require.Equal(t, commit, strings.TrimSpace(string(b)))
		b, e = exec.Command("git", "status", "--porcelain").CombinedOutput()
		require.NoError(t, e)
		require.Empty(t, strings.TrimSpace(string(b)), "published-source check requires a fresh clean checkout")
	})
	if t.Failed() {
		return
	}
	binary := filepath.Join(t.TempDir(), "published-c2j")
	b, e := exec.Command("go", "build", "-mod=readonly", "-o", binary, ".").CombinedOutput()
	require.NoError(t, e, string(b))
	t.Setenv("C2J_TEST_INPUT_HISTORY_BINARY", binary)
	t.Run("R02RetainedMetadataAndBytes", TestInputHistoryCLIProcessRetainedRecords)
	t.Run("R03RevocationAndFailedStreams", func(t *testing.T) {
		t.Run("completion", TestInputHistoryCLIProcessTransport)
		t.Run("authority", TestInputHistoryCLIProcessAuthorityBeforeCompletion)
		t.Run("remote-origin", TestInputHistoryCLIProcessRemoteOrigin)
	})
	// Every required assertion above fails closed; missing receipts, mismatching
	// refs and failing published binaries are failed publication acceptance (R04).
}
