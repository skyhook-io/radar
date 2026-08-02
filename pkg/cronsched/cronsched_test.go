package cronsched

import (
	"testing"
	"time"
)

func TestSampledMaxInterval(t *testing.T) {
	anchor := time.Date(2026, time.July, 21, 12, 0, 30, 0, time.UTC)
	cases := []struct {
		name     string
		schedule string
		timeZone string
		want     time.Duration
		wantOK   bool
	}{
		{name: "every minute", schedule: "* * * * *", want: time.Minute, wantOK: true},
		{name: "every five minutes", schedule: "*/5 * * * *", want: 5 * time.Minute, wantOK: true},
		{name: "hourly macro", schedule: "@hourly", want: time.Hour, wantOK: true},
		{name: "constant delay", schedule: "@every 90m", want: 90 * time.Minute, wantOK: true},
		{name: "multiple daily times uses longest gap", schedule: "0 9,17 * * *", want: 16 * time.Hour, wantOK: true},
		{name: "weekday schedule includes weekend gap", schedule: "0 0 * * 1-5", want: 72 * time.Hour, wantOK: true},
		{name: "dense business hours include weekend gap", schedule: "*/5 9-17 * * 1-5", want: 63*time.Hour + 5*time.Minute, wantOK: true},
		{name: "dense daily window includes overnight gap", schedule: "*/5 9-17 * * *", want: 15*time.Hour + 5*time.Minute, wantOK: true},
		{name: "day-of-month range includes long month tail", schedule: "0 0 1-10 * *", want: 22 * 24 * time.Hour, wantOK: true},
		{name: "monthly schedule includes 31-day month", schedule: "0 0 1 * *", want: 31 * 24 * time.Hour, wantOK: true},
		{name: "yearly schedule includes leap year", schedule: "0 0 1 1 *", want: 366 * 24 * time.Hour, wantOK: true},
		{name: "leap-day schedule", schedule: "0 0 29 2 *", want: 1461 * 24 * time.Hour, wantOK: true},
		{name: "daily in fixed explicit time zone", schedule: "0 2 * * *", timeZone: "Etc/GMT-2", want: 24 * time.Hour, wantOK: true},
		{name: "daily time skipped by daylight saving", schedule: "0 2 * * *", timeZone: "America/New_York", want: 47 * time.Hour, wantOK: true},
		{name: "intraday daylight-saving gap", schedule: "0 */2 * * *", timeZone: "America/New_York", want: 3 * time.Hour, wantOK: true},
		{name: "midnight daylight-saving transition", schedule: "0 0 * * *", timeZone: "America/Santiago", want: 47 * time.Hour, wantOK: true},
		{name: "invalid schedule", schedule: "not a schedule"},
		{name: "invalid time zone", schedule: "0 2 * * *", timeZone: "Mars/Olympus_Mons"},
		{name: "empty schedule"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SampledMaxInterval(tc.schedule, tc.timeZone, anchor)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("SampledMaxInterval(%q, %q) = (%s, %t), want (%s, %t)", tc.schedule, tc.timeZone, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestMinInterval(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		schedule string
		wantOK   bool
		atLeast  time.Duration // returned interval must be >= this
	}{
		{"*/5 * * * *", true, time.Hour}, // every 5 min → intra-day floor
		{"0 * * * *", true, time.Hour},   // hourly (minute 0, every hour) → intra-day floor
		{"0 0 * * *", true, day},         // daily
		{"0 0 * * 1", true, 7 * day},     // weekly
		{"0 0 1 * *", true, 28 * day},    // monthly (specific dom)
		{"0 0 1 */4 *", true, 100 * day}, // quarterly (every 4th month) — gap ~4 months
		{"0 0 1 1 *", true, 365 * day},   // yearly via numeric month (Jan 1)
		{"0 0 1 1,7 *", true, 180 * day}, // semi-annual (Jan + Jul) — 6-month gap
		{"0 0 ? * *", true, day},         // daily via Quartz '?' for day-of-month
		{"0 0 * * ?", true, day},         // daily via Quartz '?' for day-of-week
		{"@daily", true, day},            //
		{"@weekly", true, 7 * day},       //
		{"@yearly", true, 365 * day},     //
		{"not a schedule", false, 0},     //
	}
	for _, c := range cases {
		got, ok := MinInterval(c.schedule)
		if ok != c.wantOK {
			t.Errorf("%q: ok=%v want %v", c.schedule, ok, c.wantOK)
			continue
		}
		if ok && got < c.atLeast {
			t.Errorf("%q: interval=%s, want >= %s", c.schedule, got, c.atLeast)
		}
	}
}

func TestStaleThreshold(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		schedule string
		want     time.Duration
	}{
		{"*/5 * * * *", day},                // intra-day → floored at 24h
		{"0 * * * *", day},                  // hourly → floored at 24h
		{"0 0 * * *", 36 * time.Hour},       // daily → 24h + 50% grace
		{"0 0 * * 1", 7*day + 84*time.Hour}, // weekly → 7d + 50% grace
		{"unparseable", day},                // fallback → flat 24h
	}
	for _, c := range cases {
		if got := StaleThreshold(c.schedule); got != c.want {
			t.Errorf("%q: threshold=%s, want %s", c.schedule, got, c.want)
		}
	}
}
