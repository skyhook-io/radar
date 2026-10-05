package k8s

import (
	"context"
	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestAbsentResourcesSkipEveryListProbe(t *testing.T) {
	dyn := fakeDyn(t, func(gvr schema.GroupVersionResource, _ string) bool {
		if gvr.Resource == "pods" || gvr.Resource == "serviceaccounts" {
			t.Errorf("probed absent %s", gvr.Resource)
		}
		return true
	})
	result, _ := probeResourceAccess(context.Background(), dyn, nil, false, map[string]bool{"pods": true, "serviceaccounts": true, "deployments": true, "services": true})
	if result.Scopes[k8score.Pods].Enabled || result.Perms.Pods {
		t.Fatal("absent pod informer enabled")
	}
	if got := BuildVisibilitySummary(result, ""); got != nil {
		t.Fatalf("absence reported as restricted: %+v", got)
	}
}

func TestGroupLessNonTypedKindsAreResolvedBeforeAbsenceCheck(t *testing.T) {
	withCleanDiscoveryGlobals(t)
	core := buildFakeDiscoveryCore(t)
	discoveryMu.Lock()
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	resourceDiscoveryClient = GetDiscoveryClient()
	discoveryMu.Unlock()
	for _, kind := range []string{"certificates", "EndpointSlice", "Lease", "PriorityClass"} {
		if KindNotServed(kind, "") {
			t.Errorf("group-less %s falsely classified core absent", kind)
		}
	}
	if !KindNotServed("certificates", "cert-manager.io") {
		t.Fatal("explicit absent endpoint was not classified")
	}
}
