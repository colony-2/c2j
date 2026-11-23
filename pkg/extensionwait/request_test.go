package extensionwait

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExtensionWaitDecode(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"W01_time", `{"wait":{"version":1,"until":"2026-12-01T10:00:00+01:00"}}`, true},
		{"W01_past", `{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}`, true},
		{"W02_goals_retired", `{"wait":{"version":1,"until":"2026-12-01T09:00:00Z","dependencies":[{"job":"j","condition":"complete"},{"goal":"g","condition":"success"}]}}`, false},
		{"W02_success", `{"wait":{"version":1,"dependencies":[{"job":"j","condition":"success"}]}}`, true},
		{"W03_case", `{"WAIT":{"version":1,"until":"2000-01-01T00:00:00Z"}}`, false},
		{"W04_empty_second_target", `{"wait":{"version":1,"dependencies":[{"job":"j","goal":"","condition":"success"}]}}`, false},
		{"W03_empty", `{"wait":{"version":1}}`, false},
		{"W03_unknown", `{"wait":{"version":1,"output":{}}}`, false},
		{"W03_state", `{"wait":{"version":1,"state":{}}}`, false},
		{"W03_metrics", `{"wait":{"version":1,"until":"2026-12-01T09:00:00Z"},"metrics":{}}`, false},
		{"W03_execution_required", `{"wait":{"version":1,"until":"2026-12-01T09:00:00Z"},"execution_required":{}}`, false},
		{"W03_fractional_version", `{"wait":{"version":1.0,"until":"2026-12-01T09:00:00Z"}}`, false},
		{"W03_wrong_version", `{"wait":{"version":2,"until":"2026-12-01T09:00:00Z"}}`, false},
		{"W03_null", `{"wait":{"version":1,"until":null}}`, false},
		{"W03_null_dependencies", `{"wait":{"version":1,"until":"2026-12-01T09:00:00Z","dependencies":null}}`, false},
		{"W03_duplicate", `{"wait":{"version":1,"version":1,"until":"2026-12-01T09:00:00Z"}}`, false},
		{"W03_nested_duplicate", `{"wait":{"version":1,"dependencies":[{"job":"j","condition":"success","condition":"success"}]}}`, false},
		{"W03_trailing", `{"wait":{"version":1,"until":"2026-12-01T09:00:00Z"}} {}`, false},
		{"W03_root", `[]`, false},
		{"W03_root_null", `null`, false},
		{"W04_goal_complete", `{"wait":{"version":1,"dependencies":[{"goal":"g","condition":"complete"}]}}`, false},
		{"W04_blank", `{"wait":{"version":1,"dependencies":[{"job":" ","condition":"success"}]}}`, false},
		{"W04_missing_condition", `{"wait":{"version":1,"dependencies":[{"job":"j"}]}}`, false},
		{"W04_two_targets", `{"wait":{"version":1,"dependencies":[{"job":"j","goal":"g","condition":"success"}]}}`, false},
		{"W04_conflicting", `{"wait":{"version":1,"dependencies":[{"job":"j","condition":"success"},{"job":"j","condition":"complete"}]}}`, false},
		{"W05_minimum", `{"wait":{"version":1,"until":"1677-09-21T00:12:43.145224192Z"}}`, true},
		{"W05_maximum", `{"wait":{"version":1,"until":"2262-04-11T23:47:16.854775807Z"}}`, true},
		{"W05_below", `{"wait":{"version":1,"until":"1677-09-21T00:12:43.145224191Z"}}`, false},
		{"W05_above", `{"wait":{"version":1,"until":"2262-04-11T23:47:16.854775808Z"}}`, false},
		{"W06_duration", `{"wait":{"version":1,"until":"1h"}}`, false},
		{"W06_local", `{"wait":{"version":1,"until":"2026-12-01T09:00:00"}}`, false},
		{"W06_offset", `{"wait":{"version":1,"until":"2026-12-01T09:00:00+24:00"}}`, false},
		{"W06_excess_precision", `{"wait":{"version":1,"until":"2026-12-01T09:00:00.1234567890Z"}}`, false},
		{"W06_comma_fraction", `{"wait":{"version":1,"until":"2026-12-01T09:00:00,1Z"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Decode([]byte(tc.raw), 4<<20)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, 1, r.Version)
				if r.Until != nil {
					require.Equal(t, time.UTC, r.Until.Location())
				}
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), tc.raw)
			}
		})
	}
	t.Run("W07_normalization", func(t *testing.T) {
		r, e := Decode([]byte(`{"wait":{"version":1,"dependencies":[{"job":"same","condition":"success"},{"job":"same","condition":"success"},{"job":"other","condition":"success"}]}}`), 4<<20)
		require.NoError(t, e)
		require.Len(t, r.Dependencies, 2)
	})
	t.Run("W08_size", func(t *testing.T) {
		_, e := Decode([]byte(strings.Repeat(" ", 4<<20)+`{}`), 4<<20)
		require.Error(t, e)
	})
}
func TestExtensionWaitDiscardEligibility(t *testing.T) {
	r := Request{Version: 1}
	e := fmt.Errorf("wrapper: %w", New(r))
	require.True(t, IsDiscard(e))
	_, ok := Eligible(e)
	require.False(t, ok)
	e = Cleaned(e, nil)
	_, ok = Eligible(e)
	require.True(t, ok)
	e = errors.Join(e, errors.New("cleanup failed"))
	require.True(t, IsDiscard(e))
	_, ok = Eligible(e)
	require.False(t, ok)
	e = Cleaned(New(r), errors.New("private-data"))
	require.True(t, IsDiscard(e))
	_, ok = Eligible(e)
	require.False(t, ok)
	require.NotContains(t, e.Error(), "private-data")
	e = Redacted(e, "safe")
	require.True(t, IsDiscard(e))
	_, ok = Eligible(e)
	require.False(t, ok)
}
