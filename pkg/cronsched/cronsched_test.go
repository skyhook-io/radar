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

// TestMinInterval_MultiDayOfMonth pins day-of-month cadence to the real gap
// between firings. Any constrained day-of-month used to collapse to a flat 28
// days, so a twice-monthly job inherited a 42-day staleness grace and could miss
// two consecutive runs while still reading healthy for six weeks.
//
// This needs an UPPER bound — the main table asserts only a lower one, which is
// why the bug survived there.
func TestMinInterval_MultiDayOfMonth(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		schedule string
		atMost   time.Duration
		why      string
	}{
		// 1st and 15th: the widest gap is the 15th to the 1st of next month.
		{"0 0 1,15 * *", 18 * day, "twice monthly"},
		// Every fifth day: gaps of 5, plus the wrap off the 26th.
		{"0 0 */5 * *", 11 * day, "every fifth day"},
		// A contiguous run fires daily inside the window; the wrap off the 7th
		// is the widest gap.
		{"0 0 1-7 * *", 26 * day, "first week of each month"},
	}
	for _, c := range cases {
		got, ok := MinInterval(c.schedule)
		if !ok {
			t.Errorf("%q (%s): could not parse", c.schedule, c.why)
			continue
		}
		if got > c.atMost {
			t.Errorf("%q (%s): interval=%s, want <= %s — too wide a cadence grants "+
				"an unearned staleness grace", c.schedule, c.why, got, c.atMost)
		}
	}

	// A day that doesn't exist in every month must stay conservative rather
	// than alarm every February.
	if got, _ := MinInterval("0 0 31 * *"); got < 60*day {
		t.Errorf("dom=31: interval=%s, want >= 60d — the 31st is skipped in short "+
			"months, so the real gap can span two months", got)
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
