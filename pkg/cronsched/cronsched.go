// Package cronsched provides coarse cadence estimation for cron schedules so
// staleness checks can grade "this CronJob hasn't run recently" against the
// schedule's own interval instead of a flat daily threshold. A quarterly job
// that ran on schedule 29 days ago is healthy, not stale.
package cronsched

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

const day = 24 * time.Hour

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
