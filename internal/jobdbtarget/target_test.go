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

func TestProjectTargetURICompatibility(t *testing.T) {
	for _, example := range []struct{ raw, uri, runtime, id string }{
		{"embed:///", "embed:///", "embed:///", "0"},
		{"embed:///new-project", "embed:///new-project", "embed:///", "new-project"},
		{"embed:///new%2Dproject", "embed:///new-project", "embed:///", "new-project"},
		{"https://jobdb.example/new-project", "https://jobdb.example/new-project", "https://jobdb.example", "new-project"},
	} {
		target, err := Parse(example.raw)
		require.NoError(t, err)
		require.Equal(t, example.uri, target.URI)
		require.Equal(t, example.runtime, target.RuntimeURL)
		require.Equal(t, example.id, target.TenantID)
		catalog, err := ParseCatalog(example.raw)
		require.NoError(t, err)
		require.Equal(t, target.RuntimeURL, catalog.RuntimeURL)
		require.Empty(t, catalog.TenantID)
	}
	for _, raw := range []string{"https://jobdb.example", "https://jobdb.example/"} {
		_, err := Parse(raw)
		require.ErrorContains(t, err, "requires tenant path")
		catalog, err := ParseCatalog(raw)
		require.NoError(t, err)
		require.Equal(t, "https://jobdb.example", catalog.RuntimeURL)
		require.Empty(t, catalog.TenantID)
	}
	for _, raw := range []string{
		"embed:///a/b", "embed:///a%2Fb", "embed:///%2Fa", "embed:opaque", "embed:///%20", "embed:///id/",
		"https://jobdb.example/a%2fb", "https://jobdb.example/a/b", "https://secret-user:secret-password@jobdb.example",
		"https://jobdb.example?token=secret-value", "https://jobdb.example/#secret-fragment",
	} {
		for _, parse := range []func(string) (Target, error){Parse, ParseCatalog} {
			_, err := parse(raw)
			require.Error(t, err, raw)
			require.NotContains(t, err.Error(), "secret-")
		}
	}
}
