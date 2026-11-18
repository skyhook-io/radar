package main

import (
	"slices"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// Ownership and the relation families resolve kinds through discovery, and
// count a kind as an owner's dependent only when it is served with the
// garbage collector's verbs. A census fixture must serve them, or every
// relationship answer reads as unavailable.
func TestCensusDiscoveryServesBuiltinsWithGCVerbs(t *testing.T) {
	if err := installCensusDiscovery(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceDiscovery)
	for _, r := range k8s.GetResourceDiscovery().Catalog().Resources {
		if r.Group == "apps" && r.Kind == "Deployment" {
			for _, verb := range []string{"delete", "list", "watch"} {
				if !slices.Contains(r.Verbs, verb) {
					t.Fatalf("Deployment verbs = %v, missing %s", r.Verbs, verb)
				}
			}
			return
		}
	}
	t.Fatal("census discovery doesn't serve apps/Deployment")
}
