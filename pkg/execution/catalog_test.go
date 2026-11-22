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
	return BaseCatalog{Version: 1, Scope: CatalogScope{Runtime: "https://jobdb.example", Tenant: "tenant"}, Complete: true, Offerings: []BaseOffering{
		{Base: "linux", Name: "default", Capabilities: []string{"virtualization.kvm", "docker"}, Resources: &OfferingResources{CPU: &ResourceRule{State: "range", Min: catalogPtr("500m"), Max: catalogPtr("8")}, Memory: &ResourceRule{State: "range", Min: catalogPtr("1Gi"), Max: catalogPtr("32Gi")}}},
		{Base: "macos", Name: "default", Capabilities: []string{}, Resources: &OfferingResources{CPU: &ResourceRule{State: "not_allowed"}, Memory: &ResourceRule{State: "unbounded"}}},
	}, Generation: "v1", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}, now
}
func catalogPtr(v string) *string { return &v }
func TestExecutionBaseCatalogSchema(t *testing.T) {
	c, now := catalogFixture()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	t.Run("M01_round_trip", func(t *testing.T) {
		d, err := DecodeBaseCatalog(raw, c.Scope, now)
		require.NoError(t, err)
		require.Equal(t, []string{"docker", "virtualization.kvm"}, d.Offerings[0].Capabilities)
		require.Empty(t, d.Offerings[1].Capabilities)
		canonical, _ := json.Marshal(d)
		again, err := DecodeBaseCatalog(canonical, c.Scope, now)
		require.NoError(t, err)
		require.Equal(t, d, again)
	})
	t.Run("M02_M05_incomplete_and_invalid", func(t *testing.T) {
		mutations := map[string]func(*BaseCatalog){
			"version": func(c *BaseCatalog) { c.Version = 2 }, "complete": func(c *BaseCatalog) { c.Complete = false }, "offerings": func(c *BaseCatalog) { c.Offerings = nil }, "generation": func(c *BaseCatalog) { c.Generation = " " },
			"capabilities": func(c *BaseCatalog) { c.Offerings[0].Capabilities = nil }, "resources": func(c *BaseCatalog) { c.Offerings[0].Resources = nil }, "cpu": func(c *BaseCatalog) { c.Offerings[0].Resources.CPU = nil }, "memory": func(c *BaseCatalog) { c.Offerings[0].Resources.Memory = nil }, "state": func(c *BaseCatalog) { c.Offerings[0].Resources.CPU.State = "" },
			"wildcard": func(c *BaseCatalog) { c.Offerings[0].Base = "lin*" }, "oci": func(c *BaseCatalog) { c.Offerings[0].Base = "oci:alpine" }, "name": func(c *BaseCatalog) { c.Offerings[0].Name = "Bad name" }, "base": func(c *BaseCatalog) { c.Offerings[0].Base = "bad name" }, "duplicate_offering": func(c *BaseCatalog) { c.Offerings = append(c.Offerings, c.Offerings[0]) }, "duplicate_capability": func(c *BaseCatalog) { c.Offerings[0].Capabilities = []string{"docker", "docker"} }, "capability": func(c *BaseCatalog) { c.Offerings[0].Capabilities = []string{"Docker"} },
			"scope": func(c *BaseCatalog) { c.Scope.Tenant = "other" }, "future": func(c *BaseCatalog) { c.PublishedAt = now.Add(time.Second) }, "expiry": func(c *BaseCatalog) { c.ExpiresAt = now }, "time": func(c *BaseCatalog) { c.PublishedAt = time.Time{} }, "offset": func(c *BaseCatalog) { c.ExpiresAt = c.ExpiresAt.In(time.FixedZone("offset", 3600)) },
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				v, _ := catalogFixture()
				mutate(&v)
				b, err := json.Marshal(v)
				require.NoError(t, err)
				_, err = DecodeBaseCatalog(b, c.Scope, now)
				require.Error(t, err)
			})
		}
	})
	t.Run("M03_M04_strict_json", func(t *testing.T) {
		bad := []string{`null`, `{}`, string(raw) + ` {}`, strings.Repeat(" ", BaseCatalogMaxBytes+1), string(raw) + string([]byte{255}), strings.Replace(string(raw), `"generation":"v1"`, `"generation":"`+strings.Repeat("<", 14000)+`"`, 1), strings.Replace(string(raw), `"min":"500m"`, `"min":null`, 1), strings.Replace(string(raw), `"offerings":`, `"dynamic":[],"offerings":`, 1)}
		for _, member := range []string{"version", "scope", "runtime", "offerings", "base", "name", "capabilities", "resources", "cpu", "memory", "state", "min", "max", "published_at"} {
			bad = append(bad, strings.Replace(string(raw), `"`+member+`":`, `"`+strings.ToUpper(member)+`":`, 1), strings.Replace(string(raw), `"`+member+`":`, `"`+member+`":null,"`+member+`":`, 1))
		}
		for i, b := range bad {
			_, err := DecodeBaseCatalog([]byte(b), c.Scope, now)
			require.Error(t, err, "invalid case %d", i)
		}
		_, err := DecodeBaseCatalog([]byte(strings.ReplaceAll(string(raw), `Z"`, `+00:00"`)), c.Scope, now)
		require.NoError(t, err)
	})
	t.Run("M06_bounds", func(t *testing.T) {
		for _, cpu := range []bool{true, false} {
			values := []string{"0", "-1", "", "99999999999999999999999999", "0.0001"}
			if !cpu {
				values = []string{"0Gi", "-1Gi", "", "999999999999999999999999Gi", "0.0001k"}
			}
			for _, value := range values {
				_, err := normalizeRule(&ResourceRule{State: "range", Min: &value}, cpu)
				require.Error(t, err, value)
			}
			_, err := normalizeRule(&ResourceRule{State: "range"}, cpu)
			require.Error(t, err)
		}
		_, err := normalizeRule(&ResourceRule{State: "range", Min: catalogPtr("2"), Max: catalogPtr("1")}, true)
		require.Error(t, err)
		for _, state := range []string{"not_allowed", "unbounded", "unknown"} {
			_, err := normalizeRule(&ResourceRule{State: state, Min: catalogPtr("1")}, true)
			require.Error(t, err)
		}
	})
}
func TestExecutionBaseCatalogMatching(t *testing.T) {
	c, _ := catalogFixture()
	cases := []struct {
		id, selector, cpu, memory string
		caps                      []string
		decision                  string
	}{
		{"M07_inclusive_min", "linux", "500m", "1Gi", nil, "allowed"}, {"M07_inclusive_max", "linux", "8", "32Gi", nil, "allowed"},
		{"M08_equivalent", "linux", "0.5", "1073.741824M", nil, "allowed"},
		{"M14_below_floor", "linux", "499m", "1Gi", nil, "not_allowed"}, {"M07_above_ceiling", "linux", "8001m", "1Gi", nil, "not_allowed"},
		{"M09_not_allowed", "macos", "1", "", nil, "not_allowed"}, {"M09_omission", "macos", "", "", nil, "allowed"}, {"M09_unbounded", "macos", "", "100Ti", nil, "allowed"},
		{"M11_explicit_capability", "linux", "", "", []string{"virtualization.kvm"}, "allowed"}, {"M11_no_hierarchy", "linux", "", "", []string{"docker.workspace-mounts"}, "not_allowed"},
		{"M12_case", "LINUX*", "", "", nil, "not_allowed"}, {"M12_wildcard", "lin*", "", "", nil, "allowed"}, {"M12_unconstrained", "", "", "", nil, "allowed"}, {"M12_star", "*", "", "", nil, "allowed"}, {"M12_no_namespace", "o*", "", "", nil, "not_allowed"}, {"M12_oci", "oci:alpine", "", "", nil, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			r := Requirements{Capabilities: tc.caps}
			if tc.selector != "" {
				r.Base = &tc.selector
			}
			if tc.cpu != "" {
				r.Resources.CPU = &tc.cpu
			}
			if tc.memory != "" {
				r.Resources.Memory = &tc.memory
			}
			require.Equal(t, tc.decision, c.Evaluate(r).Decision)
		})
	}
	t.Run("M10_simultaneous_and_all_reasons", func(t *testing.T) {
		c, _ := catalogFixture()
		o := c.Offerings[0]
		o.Name = "other"
		o.Capabilities = []string{"special"}
		o.Resources = &OfferingResources{CPU: &ResourceRule{State: "unbounded"}, Memory: &ResourceRule{State: "not_allowed"}}
		c.Offerings = append(c.Offerings, o)
		r := Requirements{Base: catalogPtr("linux"), Capabilities: []string{"docker", "special"}, Resources: Resources{CPU: catalogPtr("16"), Memory: catalogPtr("64Gi")}}
		e := c.Evaluate(r)
		require.Equal(t, "not_allowed", e.Decision)
		require.Len(t, e.Reasons, 5)
		require.Empty(t, e.Matches)
	})
	t.Run("M13_empty", func(t *testing.T) {
		c.Offerings = []BaseOffering{}
		require.Equal(t, "not_allowed", c.Evaluate(Requirements{}).Decision)
	})
	t.Run("M14_unrelated_fields", func(t *testing.T) {
		c, _ := catalogFixture()
		r := Requirements{Base: catalogPtr("linux"), Packages: []string{"uv:ruff==0.12.0"}, Resources: Resources{EphemeralStorage: catalogPtr("99Ti")}}
		require.Equal(t, "allowed", c.Evaluate(r).Decision)
	})
	t.Run("M07_equal_endpoints", func(t *testing.T) {
		c, _ := catalogFixture()
		c.Offerings[0].Resources.CPU.Min = catalogPtr("8")
		require.NoError(t, c.Validate(c.Scope, c.PublishedAt))
		require.Equal(t, "allowed", c.Evaluate(Requirements{Base: catalogPtr("linux"), Resources: Resources{CPU: catalogPtr("8000m")}}).Decision)
	})
}
