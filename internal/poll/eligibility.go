package poll

import "time"

const (
	// Two weeks, so the answers come from people who have lived with Radar.
	// Days of actual use are counted by the browser, which sees them.
	MinInstallAge  = 14 * 24 * time.Hour
	Cooldown       = 90 * 24 * time.Hour
	StarQuietAfter = 48 * time.Hour
)

// Inputs is everything eligibility depends on, gathered by the caller so the
// rules stay a pure function.
type Inputs struct {
	Now            time.Time
	Round          Round
	Disabled       bool // RADAR_POLL=off
	DevBuild       bool
	CloudMode      bool
	TunnelSet      bool // --cloud-url
	Mode           string
	InstalledAt    time.Time
	StarPromptedAt time.Time
	State          State
}

// Eligible reports whether this Radar may offer the poll, and why not when it
// may not. For in-cluster Radar it only answers the install-wide questions;
// the browser applies the per-person ones.
func Eligible(in Inputs) (bool, string) {
	switch {
	case in.Disabled:
		return false, "disabled"
	case in.DevBuild:
		return false, "development_build"
	case in.CloudMode || in.TunnelSet:
		return false, "cloud"
	case !in.Round.Active(in.Now):
		return false, "no_active_round"
	case in.InstalledAt.IsZero():
		return false, "install_age_unknown"
	case in.Now.Sub(in.InstalledAt) < MinInstallAge:
		return false, "too_new"
	case !in.StarPromptedAt.IsZero() && in.Now.Sub(in.StarPromptedAt) < StarQuietAfter:
		return false, "star_prompt_recent"
	}
	if normalizeMode(in.Mode) == "in-cluster" {
		return true, ""
	}
	switch {
	case !in.State.NeverAt.IsZero():
		return false, "dismissed"
	case in.State.SubmittedRound == in.Round.ID:
		return false, "submitted"
	case !in.State.ShownAt.IsZero() && in.Now.Sub(in.State.ShownAt) < Cooldown:
		return false, "shown_recently"
	}
	return true, ""
}

// AgeBucket turns an install time into the coarse bucket sent with answers.
func AgeBucket(now, installedAt time.Time) string {
	if installedAt.IsZero() {
		return "unknown"
	}
	switch age := now.Sub(installedAt); {
	case age < 30*24*time.Hour:
		return "under_1_month"
	case age < 180*24*time.Hour:
		return "1_6_months"
	default:
		return "over_6_months"
	}
}
