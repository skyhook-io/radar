package k8s

import (
	"os"
	"testing"

	clientfeatures "k8s.io/client-go/features"
)

type stubGates struct{ enabled bool }

func (s stubGates) Enabled(clientfeatures.Feature) bool { return s.enabled }

func TestWatchListDisabledGatesForcesWatchListOff(t *testing.T) {
	g := watchListDisabledGates{stubGates{enabled: true}}
	if g.Enabled(clientfeatures.WatchListClient) {
		t.Fatal("WatchListClient must be reported disabled even when the wrapped gates enable it")
	}
	if !g.Enabled(clientfeatures.InformerResourceVersion) {
		t.Fatal("other features must pass through to the wrapped gates")
	}
}

func TestProcessDefaultDisablesWatchListStreaming(t *testing.T) {
	if _, set := os.LookupEnv("KUBE_FEATURE_WatchListClient"); set {
		t.Skip("explicit KUBE_FEATURE_WatchListClient override present in this environment")
	}
	if clientfeatures.FeatureGates().Enabled(clientfeatures.WatchListClient) {
		t.Fatal("package init must leave WatchListClient disabled for the process")
	}
}
