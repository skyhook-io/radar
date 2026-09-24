package version

import (
	"context"
	"time"
)

// IsDevelopmentBuild reports whether this binary is a local or CI build rather
// than a published release. Surfaces that address real users stay quiet on it.
func IsDevelopmentBuild() bool {
	return buildChannel(Current) == buildChannelDevelopment
}

// InstallMethodName reports how this binary was installed: homebrew, krew,
// scoop, direct or desktop.
func InstallMethodName() string {
	return string(detectInstallMethod())
}

// InstalledAt reports when this install was set up, or the zero time when
// that can't be determined. mode is "in-cluster" or "local".
func InstalledAt(ctx context.Context, mode string) time.Time {
	ts := installTimestamp(ctx, mode)
	if ts <= 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}
