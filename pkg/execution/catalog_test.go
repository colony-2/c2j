package execution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func catalogFixture() (BaseCatalog, time.Time) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	return BaseCatalog{Version: 1, Scope: CatalogScope{Runtime: "https://jobdb.example", Tenant: "tenant"}, Complete: true, NamedBases: []string{"macos-26-arm64", "macos-15-arm64", "linux"}, Dynamic: []DynamicBaseRule{{Kind: "oci"}}, Generation: "v1", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}, now
}
func TestExecutionBaseCatalogSchema(t *testing.T) {
	c, now := catalogFixture()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	decoded, err := DecodeBaseCatalog(raw, c.Scope, now)
	require.NoError(t, err)
	require.Equal(t, []string{"linux", "macos-15-arm64", "macos-26-arm64"}, decoded.NamedBases)
	for _, mutate := range []func(*BaseCatalog){
		func(v *BaseCatalog) { v.Version = 2 }, func(v *BaseCatalog) { v.Complete = false }, func(v *BaseCatalog) { v.NamedBases = nil }, func(v *BaseCatalog) { v.Dynamic = nil }, func(v *BaseCatalog) { v.Generation = " " },
		func(v *BaseCatalog) { v.NamedBases = []string{"mac*"} }, func(v *BaseCatalog) { v.NamedBases = []string{"x", "x"} }, func(v *BaseCatalog) { v.NamedBases = []string{"bad name"} },
		func(v *BaseCatalog) { v.Dynamic = []DynamicBaseRule{{Kind: "unknown"}} }, func(v *BaseCatalog) { v.Dynamic = []DynamicBaseRule{{Kind: "oci"}, {Kind: "oci"}} },
		func(v *BaseCatalog) { v.Scope.Tenant = "other" }, func(v *BaseCatalog) { v.Scope.Runtime = "https://elsewhere" }, func(v *BaseCatalog) { v.PublishedAt = now.Add(time.Second) }, func(v *BaseCatalog) { v.ExpiresAt = now }, func(v *BaseCatalog) { v.PublishedAt = time.Time{} },
	} {
		v, _ := catalogFixture()
		mutate(&v)
		bad, e := json.Marshal(v)
		require.NoError(t, e)
		_, e = DecodeBaseCatalog(bad, c.Scope, now)
		require.Error(t, e, string(bad))
	}
	for _, bad := range []string{`null`, `{}`, string(raw) + ` {}`, strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(raw), `"runtime":`, `"extra":1,"runtime":`, 1), strings.Replace(string(raw), `"kind":"oci"`, `"kind":"oci","kind":"oci"`, 1), strings.Repeat(" ", BaseCatalogMaxBytes+1)} {
		_, e := DecodeBaseCatalog([]byte(bad), c.Scope, now)
		require.Error(t, e)
	}
	for _, member := range []string{"version", "scope", "runtime", "kind", "published_at"} {
		for _, replacement := range []string{
			`"` + strings.ToUpper(member) + `":`,
			`"` + strings.ToUpper(member) + `":null,"` + member + `":`,
		} {
			bad := strings.Replace(string(raw), `"`+member+`":`, replacement, 1)
			_, err := DecodeBaseCatalog([]byte(bad), c.Scope, now)
			require.Error(t, err, bad)
		}
	}
	zeroOffset := strings.ReplaceAll(string(raw), "Z\"", "+00:00\"")
	_, err = DecodeBaseCatalog([]byte(zeroOffset), c.Scope, now)
	require.NoError(t, err)
	badTime := c
	badTime.ExpiresAt = badTime.ExpiresAt.In(time.FixedZone("offset", 3600))
	encoded, _ := json.Marshal(badTime)
	_, err = DecodeBaseCatalog(encoded, c.Scope, now)
	require.Error(t, err)
	c.NamedBases = []string{}
	c.Dynamic = []DynamicBaseRule{}
	raw, _ = json.Marshal(c)
	_, err = DecodeBaseCatalog(raw, c.Scope, now)
	require.NoError(t, err)
}
func TestExecutionBaseCatalogMatching(t *testing.T) {
	c, _ := catalogFixture()
	cases := map[string]BaseDecision{"": BaseAllowed, "*": BaseAllowed, "macos-15-arm64": BaseAllowed, "macos*": BaseAllowed, "*arm64": BaseAllowed, "MACOS*": BaseUnsupported, "macos-sequoia-15.7.7-arm64": BaseUnsupported, "oci:alpine:3": BaseAllowed, "oci:": BaseUnsupported, "oci:*": BaseCannotVerify, "o*i:*": BaseCannotVerify, "*alpine*": BaseCannotVerify, "*oci:*": BaseCannotVerify, "o*": BaseCannotVerify, "linux*": BaseAllowed, "macos-99*": BaseUnsupported, "ociX*": BaseUnsupported}
	for selector, want := range cases {
		t.Run(selector, func(t *testing.T) { require.Equal(t, want, c.AdmitBase(selector)) })
	}
	c.Dynamic = []DynamicBaseRule{}
	require.Equal(t, BaseUnsupported, c.AdmitBase("oci:alpine:3"))
	require.Equal(t, BaseUnsupported, c.AdmitBase("o*"))
	c.NamedBases = []string{}
	require.Equal(t, BaseUnsupported, c.AdmitBase("macos*"))
}
