package main

import (
	"testing"

	"github.com/skyhook-io/radar/internal/reachability"
	versionpkg "github.com/skyhook-io/radar/internal/version"
)

func TestReachabilityImagesWithVersionChecksDisabled(t *testing.T) {
	t.Setenv("RADAR_DISABLE_VERSION_CHECK", "")
	t.Setenv("RADAR_IMAGE", "")
	previousVersion := versionpkg.Current
	previousDisabled := versionpkg.IsDisabled()
	previousImage := reachability.DefaultImageRef
	previousLatest := reachability.LatestReleaseImage
	t.Cleanup(func() {
		versionpkg.SetCurrent(previousVersion)
		versionpkg.SetDisabled(previousDisabled)
		reachability.DefaultImageRef = previousImage
		reachability.LatestReleaseImage = previousLatest
	})
	versionpkg.SetDisabled(true)

	for _, tc := range []struct {
		version string
		wantTag string
	}{
		{"1.6.0", "1.6.0"},
		{"1.6.0-rc.1", "1.6.0-rc.1"},
		{"1.6.0-beta.2", "1.6.0-beta.2"},
		{"1.6.0-vendor.1", "1.6.0-vendor.1"},
		{"dev", "latest"},
		{"1.6.0-37-g8b325b372", "latest"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			versionpkg.SetCurrent(tc.version)
			reachability.LatestReleaseImage = nil
			configureReachabilityImages()

			if got, want := reachability.DefaultImage(), "ghcr.io/skyhook-io/radar:"+tc.wantTag; got != want {
				t.Errorf("probe image = %q, want %q", got, want)
			}
		})
	}
}
