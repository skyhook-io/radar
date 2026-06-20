// Package timeutil provides shared duration/time formatting helpers.
// Centralized here so audit findings, GitOps Issue messages, and the
// frontend's relative-age text agree on tier breakpoints — a "23h" in
// the audit page must read the same as "23h" in the GitOps banner.
package timeutil

import (
	"fmt"
	"time"
)

// FormatAgeShort renders a duration as a compact relative string:
// "3s", "12m", "4h", "21d". Negative inputs clamp to zero — duration
// formatting is for display, not validation, so don't surface "-1s".
//
// keep in sync: packages/k8s-ui/src/utils/format.ts::formatCompactAge
// (TypeScript). Adding a new tier (e.g. "weeks") here without mirroring
// it in TS would let the audit finding and the fleet banner disagree on
// the same age.
func FormatAgeShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
