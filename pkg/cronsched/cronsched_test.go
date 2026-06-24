package cronsched

import (
	"testing"
	"time"
)

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
		{"0 0 ? * *", true, day},          // daily via Quartz '?' for day-of-month
		{"0 0 * * ?", true, day},          // daily via Quartz '?' for day-of-week
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
		{"*/5 * * * *", day},               // intra-day → floored at 24h
		{"0 * * * *", day},                 // hourly → floored at 24h
		{"0 0 * * *", 36 * time.Hour},      // daily → 24h + 50% grace
		{"0 0 * * 1", 7*day + 84*time.Hour}, // weekly → 7d + 50% grace
		{"unparseable", day},               // fallback → flat 24h
	}
	for _, c := range cases {
		if got := StaleThreshold(c.schedule); got != c.want {
			t.Errorf("%q: threshold=%s, want %s", c.schedule, got, c.want)
		}
	}
}
