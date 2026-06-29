package timeutil

import (
	"testing"
	"time"
)

func TestFormatAgeShort(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "0s"},
		{"sub-second rounds down to 0s", 999 * time.Millisecond, "0s"},
		{"seconds", 3 * time.Second, "3s"},
		{"just under a minute", 59 * time.Second, "59s"},
		{"exactly one minute", time.Minute, "1m"},
		{"minutes round down", 90 * time.Second, "1m"},
		{"just under an hour", 59 * time.Minute, "59m"},
		{"exactly one hour", time.Hour, "1h"},
		{"hours round down", 90 * time.Minute, "1h"},
		{"just under a day", 23 * time.Hour, "23h"},
		{"exactly one day", 24 * time.Hour, "1d"},
		{"multiple days round down", 49 * time.Hour, "2d"},
		{"three weeks", 21 * 24 * time.Hour, "21d"},
		{"negative clamps to zero", -5 * time.Second, "0s"},
		{"large negative clamps to zero", -100 * time.Hour, "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatAgeShort(tt.d); got != tt.want {
				t.Errorf("FormatAgeShort(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// Each tier boundary must flip the unit exactly at its threshold — a
// regression here silently desyncs the Go-rendered age from the TypeScript
// mirror in packages/k8s-ui/src/utils/format.ts::formatCompactAge.
func TestFormatAgeShort_TierBoundaries(t *testing.T) {
	boundaries := []struct {
		below     time.Duration
		belowWant string
		at        time.Duration
		atWant    string
	}{
		{time.Minute - time.Nanosecond, "59s", time.Minute, "1m"},
		{time.Hour - time.Nanosecond, "59m", time.Hour, "1h"},
		{24*time.Hour - time.Nanosecond, "23h", 24 * time.Hour, "1d"},
	}

	for _, b := range boundaries {
		if got := FormatAgeShort(b.below); got != b.belowWant {
			t.Errorf("FormatAgeShort(%v) = %q, want %q", b.below, got, b.belowWant)
		}
		if got := FormatAgeShort(b.at); got != b.atWant {
			t.Errorf("FormatAgeShort(%v) = %q, want %q", b.at, got, b.atWant)
		}
	}
}
