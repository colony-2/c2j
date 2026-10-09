package jobdbtarget

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRejectsCredentialsWithoutEchoingInput(t *testing.T) {
	for _, raw := range []string{
		"https://secret-user:secret-password@localhost/tenant",
		"https://localhost/tenant?token=secret-query",
		"https://localhost/tenant#secret-fragment",
		"https://secret-user:secret-password@localhost:bad/tenant?token=secret-query",
		"https://localhost/%zz?token=secret-query",
		"embed:///secret-path?token=secret-query",
		"secret-scheme://localhost/tenant",
		"https:///secret-path?token=secret-query",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := Parse(raw)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret-")
			require.Contains(t, err.Error(), "JobDB URI")
		})
	}
}
