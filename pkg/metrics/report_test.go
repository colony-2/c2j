package metrics

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReportLimitBoundaries(t *testing.T) {
	for _, field := range []string{"name", "unit", "label-key", "label-value", "labels", "bounds", "mutations"} {
		t.Run(field, func(t *testing.T) {
			makeReport := func(extra int) *Report {
				m := counter(1)
				switch field {
				case "name":
					m.Name = strings.Repeat("é", MaxNameBytes/2) + strings.Repeat("x", extra)
				case "unit":
					m.Unit = strings.Repeat("u", MaxNameBytes+extra)
				case "label-key":
					m.Labels = map[string]string{strings.Repeat("k", MaxNameBytes+extra): ""}
				case "label-value":
					m.Labels = map[string]string{"key": strings.Repeat("é", MaxLabelBytes/2) + strings.Repeat("x", extra)}
				case "labels":
					m.Labels = map[string]string{}
					for i := 0; i < MaxLabels+extra; i++ {
						m.Labels[string(rune('a'+i))] = ""
					}
				case "bounds":
					m = histogram(0)
					for i := 0; i < MaxBounds+extra; i++ {
						m.Bounds = append(m.Bounds, float64(i))
					}
				}
				r := &Report{Version: Version, Mutations: []Mutation{m}}
				if field == "mutations" {
					r.Mutations = make([]Mutation, MaxMutations+extra)
					for i := range r.Mutations {
						r.Mutations[i] = m
					}
				}
				return r
			}
			_, err := Validate(makeReport(0), false)
			require.NoError(t, err, "the declared boundary is admissible")
			_, err = Validate(makeReport(1), false)
			require.Error(t, err, "one byte/entry above the boundary rejects the whole report")
		})
	}
	raw := `{"version":1,"mutations":[]}`
	raw += strings.Repeat(" ", MaxBytes-len(raw))
	_, diagnostic := DecodeReport([]byte(raw))
	require.Empty(t, diagnostic)
	_, diagnostic = DecodeReport([]byte(raw + " "))
	require.Equal(t, "metric report exceeds byte limit", diagnostic)
}

func TestCollectorChecksNewSecretsBeforePersistence(t *testing.T) {
	known := false
	c := NewCollector(func(v any) error {
		if known {
			return errors.New("sensitive material")
		}
		return nil
	})
	c.AddInt("tokens", "", map[string]string{"model": "later-discovered-secret"}, 1)
	require.Equal(t, "valid", c.Snapshot().Status)
	known = true
	channel := c.Snapshot()
	require.Equal(t, "invalid", channel.Status)
	require.Nil(t, channel.Report)
	require.Equal(t, "metric report contains secret data", channel.Diagnostic)
	// An arbitrary producer diagnostic cannot become persisted error text.
	c.Invalidate("invalid", "later-discovered-secret")
	require.Equal(t, "invalid metric report", c.Snapshot().Diagnostic)
}
