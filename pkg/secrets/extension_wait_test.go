package secrets

import (
	"errors"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExtensionWaitRedaction(t *testing.T) {
	r := NewRedactor()
	defer r.Close()
	require.NoError(t, r.Register("private-cleanup-error"))
	e := errors.Join(extensionwait.New(extensionwait.Request{Version: 1}), errors.New("private-cleanup-error"))
	e = r.Error(e)
	require.True(t, extensionwait.IsDiscard(e))
	require.NotContains(t, e.Error(), "private-cleanup-error")
	_, ok := extensionwait.Eligible(e)
	require.False(t, ok)
}
