package k8s

import (
	"os"

	clientfeatures "k8s.io/client-go/features"
)

// Streaming initial lists (client-go's WatchListClient gate, on by default
// against apiservers >= 1.34) sync 13-16x slower than plain LISTs at large
// scale: informer stores stay empty until the stream's terminal bookmark, so
// on a 25k+ pod cluster the critical Pod/Service/Deployment informers hold
// first paint for minutes. Radar values fast startup over the
// apiserver-memory savings streaming buys, so the gate is forced off for the
// whole process before any informer starts. Setting KUBE_FEATURE_WatchListClient
// explicitly keeps client-go's env-var handling in charge instead.
func init() {
	if v, set := os.LookupEnv("KUBE_FEATURE_WatchListClient"); set {
		streamingListsMode = "env:" + v
		return
	}
	clientfeatures.ReplaceFeatureGates(watchListDisabledGates{clientfeatures.FeatureGates()})
}

// streamingListsMode records who decided the WatchListClient state, for the
// startup log and the diagnostics snapshot. Written once in init.
var streamingListsMode = "disabled (default)"

// StreamingListsMode reports the effective WatchListClient policy:
// "disabled (default)" when radar forced the gate off, or "env:<value>" when
// an explicit KUBE_FEATURE_WatchListClient left client-go in charge. Surfaced
// in the diagnostics snapshot so support can tell the two apart without
// asking the user to inspect their environment.
func StreamingListsMode() string {
	return streamingListsMode
}

type watchListDisabledGates struct {
	clientfeatures.Gates
}

func (g watchListDisabledGates) Enabled(f clientfeatures.Feature) bool {
	if f == clientfeatures.WatchListClient {
		return false
	}
	return g.Gates.Enabled(f)
}
