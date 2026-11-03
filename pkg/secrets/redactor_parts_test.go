package secrets

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRedactPartsCombinesOverlappingScopesBeforeReplacement(t *testing.T) {
	primary, git := NewRedactor(), NewRedactor()
	require.NoError(t, primary.Register("private"))
	require.NoError(t, git.Register("private-token"))
	require.Equal(t, []string{"prefix [REDACTED]", " suffix"}, primary.RedactParts([]string{"prefix private-", "token suffix"}, false, git))
	require.Equal(t, []string{"[REDACTED]"}, primary.RedactParts([]string{"private-to"}, true, git))
}

func TestRedactPartsEscapedSecretAndUnicode(t *testing.T) {
	r := NewRedactor()
	require.NoError(t, r.Register("secret\n€"))
	require.Equal(t, []string{"[REDACTED]", "!"}, r.RedactParts([]string{`secret\n`, "€!"}, false))
}
