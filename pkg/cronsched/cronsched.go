// Package cronsched provides sampled and coarse cadence calculations for cron
// schedules. Sampled intervals support schedule-relative diagnostics; coarse
// estimates keep rare-cadence staleness checks from using a flat daily limit.
package cronsched

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	day                               = 24 * time.Hour
	cadenceCalendarSampleYears        = 8
	cronSpecStarBit            uint64 = 1 << 63
)

// SampledMaxInterval returns the longest interval found across eight calendar
// years of a Kubernetes CronJob schedule. Walking civil dates rather than every
// firing keeps dense schedules bounded while still seeing overnight, weekend,
// month-length, leap-year, and daylight-saving boundaries.
// The separate timeZone argument mirrors CronJob.spec.timeZone. An omitted time
// zone uses UTC so the machine running Radar cannot change the result. Callers
// that need a stable threshold must pass a stable anchor.
func SampledMaxInterval(schedule, timeZone string, anchor time.Time) (time.Duration, bool) {
	spec := strings.TrimSpace(schedule)
	if spec == "" {
		return 0, false
	}
	if timeZone == "" {
		timeZone = "UTC"
	}
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		return 0, false
	}
	spec = "CRON_TZ=" + timeZone + " " + spec

	parsed, err := cron.ParseStandard(spec)
	if err != nil {
		return 0, false
	}
	if constant, ok := parsed.(cron.ConstantDelaySchedule); ok {
		return constant.Delay, constant.Delay > 0
	}
	anchorLocal := anchor.In(loc)
	date := time.Date(anchorLocal.Year(), time.January, 1, 0, 0, 0, 0, loc)
	first := parsed.Next(date.Add(-time.Second))
	if first.IsZero() {
		return 0, false
	}
	parsedSpec, ok := parsed.(*cron.SpecSchedule)
	if !ok {
		return 0, false
	}
	offsets := scheduledMinuteOffsets(parsedSpec)
	if len(offsets) == 0 {
		return 0, false
	}

	longest := maxScheduledMinuteGap(offsets)
	end := date.AddDate(cadenceCalendarSampleYears, 0, 0)
	var previousLast time.Time
	for !date.After(end) {
		if specMatchesDate(parsedSpec, date) {
			firstOfDay, lastOfDay, found := scheduledDayBounds(date, offsets)
			if utcOffsetChangesAcrossDate(date) {
				var intraDay time.Duration
				var ok bool
				firstOfDay, lastOfDay, intraDay, found, ok = actualScheduledDayBounds(parsed, date)
				if !ok {
					return 0, false
				}
				longest = max(longest, intraDay)
			}
			if found && !lastOfDay.Before(first) {
				if !previousLast.IsZero() {
					longest = max(longest, firstOfDay.Sub(previousLast))
				}
				previousLast = lastOfDay
			}
		}
		date = date.AddDate(0, 0, 1)
	}
	return longest, longest > 0
}

func scheduledMinuteOffsets(schedule *cron.SpecSchedule) []int {
	var offsets []int
	for hour := range 24 {
		if schedule.Hour&(1<<hour) == 0 {
			continue
		}
		for minute := range 60 {
			if schedule.Minute&(1<<minute) != 0 {
				offsets = append(offsets, hour*60+minute)
			}
		}
	}
	return offsets
}

func maxScheduledMinuteGap(offsets []int) time.Duration {
	var longest time.Duration
	for i := 1; i < len(offsets); i++ {
		longest = max(longest, time.Duration(offsets[i]-offsets[i-1])*time.Minute)
	}
	return longest
}

func specMatchesDate(schedule *cron.SpecSchedule, date time.Time) bool {
	if schedule.Month&(1<<uint(date.Month())) == 0 {
		return false
	}
	domMatch := schedule.Dom&(1<<uint(date.Day())) != 0
	dowMatch := schedule.Dow&(1<<uint(date.Weekday())) != 0
	if schedule.Dom&cronSpecStarBit != 0 || schedule.Dow&cronSpecStarBit != 0 {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

func scheduledDayBounds(date time.Time, offsets []int) (time.Time, time.Time, bool) {
	for _, offset := range offsets {
		first, ok := scheduledTimeOnDate(date, offset)
		if !ok {
			continue
		}
		for i := len(offsets) - 1; i >= 0; i-- {
			last, ok := scheduledTimeOnDate(date, offsets[i])
			if ok {
				return first, last, true
			}
		}
	}
	return time.Time{}, time.Time{}, false
}

func scheduledTimeOnDate(date time.Time, offset int) (time.Time, bool) {
	hour, minute := offset/60, offset%60
	candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, date.Location())
	ok := candidate.Year() == date.Year() && candidate.Month() == date.Month() && candidate.Day() == date.Day() &&
		candidate.Hour() == hour && candidate.Minute() == minute
	return candidate, ok
}

func utcOffsetChangesAcrossDate(date time.Time) bool {
	_, startOffset := date.Zone()
	_, endOffset := date.AddDate(0, 0, 1).Zone()
	return startOffset != endOffset
}

func actualScheduledDayBounds(schedule cron.Schedule, date time.Time) (time.Time, time.Time, time.Duration, bool, bool) {
	var first, previous time.Time
	var longest time.Duration
	for next := schedule.Next(date.Add(-time.Second)); ; next = schedule.Next(previous) {
		if next.IsZero() {
			return time.Time{}, time.Time{}, 0, false, false
		}
		local := next.In(date.Location())
		if local.Year() != date.Year() || local.Month() != date.Month() || local.Day() != date.Day() {
			return first, previous, longest, !first.IsZero(), true
		}
		if first.IsZero() {
			first = next
		} else {
			longest = max(longest, next.Sub(previous))
		}
		previous = next
	}
}

// MinInterval estimates a representative interval between runs of a standard
// 5-field cron schedule (minute hour dom month dow), plus the common @-macros.
// It is deliberately approximate — its only job is to keep staleness checks from
// flagging a rare-cadence job (weekly / monthly / quarterly / yearly) as stale
// against a flat daily threshold. For month-constrained schedules it returns the
// largest gap between firing months (so a once-a-year job reads as ~yearly, not
// monthly). ok=false for schedules it can't parse; callers then fall back to the
// flat threshold.
func MinInterval(schedule string) (time.Duration, bool) {
	s := strings.TrimSpace(schedule)
	// '?' is the Quartz "no specific value" token some schedules use in the
	// day-of-month / day-of-week field; treat it as '*' so a daily "0 0 ? * *"
	// isn't misread as a monthly cadence (and given a 42-day stale window).
	s = strings.ReplaceAll(s, "?", "*")
	switch s {
	case "@yearly", "@annually":
		return 365 * day, true
	case "@monthly":
		return 28 * day, true
	case "@weekly":
		return 7 * day, true
	case "@daily", "@midnight":
		return day, true
	case "@hourly":
		return time.Hour, true
	}
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return 0, false
	}
	hour, dom, month, dow := fields[1], fields[2], fields[3], fields[4]
	switch {
	case month != "*":
		// Constrained months → cadence is the largest gap between firing months
		// (yearly if a single month, quarterly for */4, etc.), not a flat month.
		if gap, ok := maxMonthGapDays(month); ok {
			return gap, true
		}
		return 28 * day, true
	case dom != "*":
		// Specific day(s)-of-month → monthly cadence.
		return 28 * day, true
	case dow != "*":
		// Specific day(s)-of-week → weekly is the conservative lower bound.
		return 7 * day, true
	case hour != "*" && !strings.HasPrefix(hour, "*/"):
		// Specific hour(s) each day → daily.
		return day, true
	default:
		// Intra-day cadence (every minute / */n minutes or hours).
		return time.Hour, true
	}
}

// maxMonthGapDays returns the longest stretch (in days) the schedule can go
// between firing months, given a constrained month field. A single month fires
// once a year; */4 fires every four months; a list takes the widest gap between
// consecutive entries (wrapping past December). Months are valued at 31 days so
// the estimate stays an upper bound — staleness should never trip on a healthy
// rare-cadence job. ok=false when the field can't be parsed.
func maxMonthGapDays(field string) (time.Duration, bool) {
	months, ok := cronFieldValues(field, 1, 12)
	if !ok || len(months) == 0 {
		return 0, false
	}
	if len(months) == 1 {
		return 365 * day, true
	}
	maxGap := months[0] + 12 - months[len(months)-1] // wrap from last to first next year
	for i := 1; i < len(months); i++ {
		if g := months[i] - months[i-1]; g > maxGap {
			maxGap = g
		}
	}
	return time.Duration(maxGap) * 31 * day, true
}

// cronFieldValues expands a single cron field (lists, ranges, steps, and *) into
// its sorted set of concrete values within [lo, hi]. ok=false on any token it
// can't parse so the caller can fall back rather than trust a partial set.
func cronFieldValues(field string, lo, hi int) ([]int, bool) {
	set := make(map[int]bool)
	for _, part := range strings.Split(field, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			st, err := strconv.Atoi(part[i+1:])
			if err != nil || st <= 0 {
				return nil, false
			}
			step = st
			part = part[:i]
		}
		start, end := lo, hi
		switch {
		case part == "*":
			// start/end already span [lo, hi]
		case strings.Contains(part, "-"):
			bounds := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(bounds[0])
			b, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil {
				return nil, false
			}
			start, end = a, b
		default:
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, false
			}
			start, end = v, v
		}
		if start < lo || end > hi || start > end {
			return nil, false
		}
		for v := start; v <= end; v += step {
			set[v] = true
		}
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Ints(out)
	return out, len(out) > 0
}

// StaleThreshold returns how long a CronJob may go without running before it is
// considered stale. Cadence-relative (interval + 50% grace) but floored at 24h
// so frequent jobs keep the original sensitivity.
func StaleThreshold(schedule string) time.Duration {
	threshold := day
	if interval, ok := MinInterval(schedule); ok {
		if grace := interval + interval/2; grace > threshold {
			threshold = grace
		}
	}
	return threshold
}
