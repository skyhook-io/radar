package cloud

// Version is the Radar binary version. Set at build time with:
//
//	-ldflags "-X github.com/skyhook-io/radar/internal/cloud.Version=v1.5.2"
//
// Falls back to "dev" for local builds.
var Version = "dev"
