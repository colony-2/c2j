package submitjob

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func parseInstant(t *testing.T, value string) time.Time {
	t.Helper()
	instant, err := time.Parse(time.RFC3339Nano, value)
	require.NoError(t, err)
	return instant
}

func TestResolveWaitTime(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	for _, tc := range []struct {
		raw, anchor, want string
		loc               *time.Location
	}{
		{"2027-01-01T12:20:00.123456789Z", "2026-01-01T00:00:00Z", "2027-01-01T12:20:00.123456789Z", ny},
		{"2027-01-01T12:20:00.123456789+05:30", "2026-01-01T00:00:00Z", "2027-01-01T06:50:00.123456789Z", ny},
		{"2027-01-01 12:20", "2026-01-01T00:00:00Z", "2027-01-01T17:20:00Z", ny},
		{"2027-01-01 12:20:01", "2026-01-01T00:00:00Z", "2027-01-01T17:20:01Z", ny},
		{"1:30PM", "2026-11-15T12:00:00Z", "2026-11-15T18:30:00Z", ny},
		{"12:00am", "2026-11-15T12:00:00Z", "2026-11-16T05:00:00Z", ny},
		{"12:00pm", "2026-11-15T12:00:00Z", "2026-11-15T17:00:00Z", ny},
		{"13:30:01", "2026-11-15T12:00:00Z", "2026-11-15T18:30:01Z", ny},
		{"13:30", "2026-11-15T18:30:00Z", "2026-11-16T18:30:00Z", ny},
		{"13:30", "2026-11-15T18:30:00.1Z", "2026-11-16T18:30:00Z", ny},
		{"00:00", "2026-12-31T23:59:59Z", "2027-01-01T00:00:00Z", time.UTC},
		{"00:00", "2026-01-31T23:59:59Z", "2026-02-01T00:00:00Z", time.UTC},
		{"03:30", "2027-03-13T09:00:00Z", "2027-03-14T07:30:00Z", ny},
		{"2027-11-07T01:30:00-04:00", "2027-11-07T04:00:00Z", "2027-11-07T05:30:00Z", ny},
		{"1h30m", "2026-11-15T12:00:00.123456789Z", "2026-11-15T13:30:00.123456789Z", ny},
		{"90m", "2026-11-15T12:00:00.123456789Z", "2026-11-15T13:30:00.123456789Z", time.UTC},
	} {
		t.Run(tc.raw+tc.anchor, func(t *testing.T) {
			got, _, err := resolveWaitTime(tc.raw, parseInstant(t, tc.anchor), tc.loc)
			require.NoError(t, err)
			require.Equal(t, parseInstant(t, tc.want), got)
			require.Same(t, time.UTC, got.Location())
		})
	}
}

func TestResolveWaitTimeRejectsInvalidInput(t *testing.T) {
	anchor := parseInstant(t, "2026-11-15T12:00:00Z")
	for _, raw := range []string{"", " ", "123", "2027-02-29 12:00", "2027-01-01", "2027-01-01 12:00 EST", "1d", "1mo", "0", "0s", "-1h", "999999999999999h", "24:00", "12:60", "12:00:60", "0:30pm", "13:30pm", "2026-11-15T12:00:00Z", "2026-11-14 12:00", "2300-01-01T00:00:00Z", "2027-01-01T00:00:00+24:00", "2027-01-01T00:00:00+01:60"} {
		t.Run(raw, func(t *testing.T) { _, _, err := resolveWaitTime(raw, anchor, time.UTC); require.Error(t, err) })
	}
}

func TestResolveWaitTimeDST(t *testing.T) {
	for _, tc := range []struct{ zone, raw, anchor, want string }{
		{"America/New_York", "2027-03-14 02:30", "2027-01-01T00:00:00Z", "nonexistent"},
		{"America/New_York", "02:30", "2027-03-13T09:00:00Z", "nonexistent"},
		{"America/New_York", "2027-11-07 01:30", "2027-01-01T00:00:00Z", "ambiguous"},
		{"America/New_York", "01:30", "2027-11-07T04:00:00Z", "ambiguous"},
		{"Australia/Lord_Howe", "2027-04-04 01:45", "2027-01-01T00:00:00Z", "ambiguous"},
		{"Australia/Lord_Howe", "2027-10-03 02:15", "2027-01-01T00:00:00Z", "nonexistent"},
	} {
		t.Run(tc.zone+tc.raw, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			require.NoError(t, err)
			_, _, err = resolveWaitTime(tc.raw, parseInstant(t, tc.anchor), loc)
			require.ErrorContains(t, err, tc.want)
			require.ErrorContains(t, err, "explicit")
		})
	}
}

func TestResolveWaitTimePortableBounds(t *testing.T) {
	anchor := portableTimeMin.Add(-time.Hour)
	for _, instant := range []time.Time{portableTimeMin, portableTimeMax} {
		got, _, err := resolveWaitTime(instant.Format(time.RFC3339Nano), anchor, time.UTC)
		require.NoError(t, err)
		require.Equal(t, instant, got)
	}
	for _, instant := range []time.Time{portableTimeMin.Add(-time.Nanosecond), portableTimeMax.Add(time.Nanosecond)} {
		_, _, err := resolveWaitTime(instant.Format(time.RFC3339Nano), anchor, time.UTC)
		require.ErrorContains(t, err, "within")
	}
	_, _, err := resolveWaitTime("2ns", portableTimeMax.Add(-time.Nanosecond), time.UTC)
	require.ErrorContains(t, err, "within")
}
