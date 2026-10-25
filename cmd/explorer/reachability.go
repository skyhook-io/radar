package main

import (
	"context"

	"github.com/skyhook-io/radar/internal/reachability"
	versionpkg "github.com/skyhook-io/radar/internal/version"
)

func configureReachabilityImages() {
	reachability.DefaultImageRef = "ghcr.io/skyhook-io/radar:" + versionpkg.Current
	if !versionpkg.IsDevelopmentBuild() {
		return
	}

	// Git-describe build versions have no matching published image.
	reachability.LatestReleaseImage = func() string {
		if !versionpkg.IsDisabled() {
			if update := versionpkg.CheckForUpdateRelease(context.Background()); update.LatestVersion != "" {
				return "ghcr.io/skyhook-io/radar:" + update.LatestVersion
			}
		}
		return "ghcr.io/skyhook-io/radar:latest"
	}
}
