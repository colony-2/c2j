package submitjob

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	portableTimeMin  = time.Date(1677, 9, 21, 0, 12, 43, 145224192, time.UTC)
	portableTimeMax  = time.Date(2262, 4, 11, 23, 47, 16, 854775807, time.UTC)
	rfcTimePattern   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`)
	localTimePattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}) (\d{2}):(\d{2})(?::(\d{2}))?$`)
	clockTimePattern = regexp.MustCompile(`^(\d{2}):(\d{2})(?::(\d{2}))?$`)
	ampmTimePattern  = regexp.MustCompile(`(?i)^(\d{1,2}):(\d{2})(am|pm)$`)
)

// resolveWaitTime reads no clock and returns the original instant, never a
// backend-rounded scheduling value. Freshness is checked only here.
func resolveWaitTime(raw string, anchor time.Time, loc *time.Location) (time.Time, string, error) {
	value := strings.TrimSpace(raw)
	if loc == nil {
		loc = time.Local
	}
	var instant time.Time
	zone := loc.String()
	var err error
	switch {
	case rfcTimePattern.MatchString(value):
		instant, err = time.Parse(time.RFC3339Nano, value)
		if strings.HasSuffix(value, "Z") {
			zone = "UTC"
		} else {
			zone = value[len(value)-6:]
			hour, _ := strconv.Atoi(zone[1:3])
			minute, _ := strconv.Atoi(zone[4:6])
			if hour > 23 || minute > 59 {
				err = fmt.Errorf("invalid RFC3339 offset")
			}
		}
	case localTimePattern.MatchString(value):
		layout := "2006-01-02 15:04"
		if len(value) == len("2006-01-02 15:04:05") {
			layout += ":05"
		}
		var civil time.Time
		civil, err = time.Parse(layout, value)
		if err == nil {
			instant, err = resolveCivil(civil, loc)
		}
	case clockTimePattern.MatchString(value) || ampmTimePattern.MatchString(value):
		var hour, minute, second int
		if m := ampmTimePattern.FindStringSubmatch(value); m != nil {
			hour, _ = strconv.Atoi(m[1])
			minute, _ = strconv.Atoi(m[2])
			if hour < 1 || hour > 12 || minute > 59 {
				err = fmt.Errorf("invalid clock time")
			}
			hour %= 12
			if strings.EqualFold(m[3], "pm") {
				hour += 12
			}
		} else {
			m := clockTimePattern.FindStringSubmatch(value)
			hour, _ = strconv.Atoi(m[1])
			minute, _ = strconv.Atoi(m[2])
			second, _ = strconv.Atoi(m[3])
			if hour > 23 || minute > 59 || second > 59 {
				err = fmt.Errorf("invalid clock time")
			}
		}
		if err == nil {
			local := anchor.In(loc)
			civil := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, second, 0, time.UTC)
			// Compare civil clocks first, then validate the selected local date.
			if hour*3600+minute*60+second <= local.Hour()*3600+local.Minute()*60+local.Second() {
				civil = civil.AddDate(0, 0, 1)
			}
			instant, err = resolveCivil(civil, loc)
		}
	default:
		var duration time.Duration
		duration, err = time.ParseDuration(value)
		if err == nil && duration <= 0 {
			err = fmt.Errorf("duration must be positive")
		}
		if err == nil {
			instant = anchor.Add(duration)
			zone = "elapsed duration"
		}
	}
	if err != nil {
		return time.Time{}, "", fmt.Errorf("--wait-until %q: %w; use RFC3339 with an explicit offset, a local date/time or clock, or a positive Go duration", raw, err)
	}
	instant = instant.UTC()
	if instant.Before(portableTimeMin) || instant.After(portableTimeMax) {
		return time.Time{}, "", fmt.Errorf("--wait-until must be within %s through %s", portableTimeMin.Format(time.RFC3339Nano), portableTimeMax.Format(time.RFC3339Nano))
	}
	if !instant.After(anchor) {
		return time.Time{}, "", fmt.Errorf("--wait-until must be strictly after the submission time")
	}
	return instant, zone, nil
}

// resolveCivil enumerates adjacent zone offsets and accepts only a unique UTC
// candidate that round-trips to the requested civil components. ZoneBounds
// handles non-hour transitions and skipped dates without assuming DST size.
func resolveCivil(civil time.Time, loc *time.Location) (time.Time, error) {
	offsets := map[int]bool{}
	end := civil.Add(72 * time.Hour)
	for cursor := civil.Add(-72 * time.Hour); cursor.Before(end); {
		_, offset := cursor.In(loc).Zone()
		offsets[offset] = true
		_, next := cursor.In(loc).ZoneBounds()
		if next.IsZero() || !next.After(cursor) {
			break
		}
		cursor = next
	}
	var matches []time.Time
	for offset := range offsets {
		candidate := civil.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(loc)
		if local.Year() == civil.Year() && local.Month() == civil.Month() && local.Day() == civil.Day() && local.Hour() == civil.Hour() && local.Minute() == civil.Minute() && local.Second() == civil.Second() {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return time.Time{}, fmt.Errorf("nonexistent local time in %s (for example, use 2027-01-01T12:20:00-05:00 to select an explicit instant)", loc)
	}
	if len(matches) > 1 {
		return time.Time{}, fmt.Errorf("ambiguous local time in %s (for example, use 2027-01-01T12:20:00-05:00 to select an explicit instant)", loc)
	}
	return matches[0], nil
}
