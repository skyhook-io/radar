package k8s

import (
	"context"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
	"sync"
	"time"
)

// Targeted discovery shares the probe budget without waiting for aggregated discovery.
func discoverProbeAbsence(ctx context.Context) map[string]bool {
	client := GetClient()
	if client == nil {
		return nil
	}
	probes := resourceProbeTargets(&ResourcePermissions{})
	lists := map[schema.GroupVersion]*metav1.APIResourceList{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	seen := map[schema.GroupVersion]bool{}
	for _, p := range probes {
		for _, gvr := range resolveProbeGVRs(p) {
			gv := gvr.GroupVersion()
			if seen[gv] {
				continue
			}
			seen[gv] = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				path := "/apis/" + gv.String()
				if gv.Group == "" {
					path = "/api/" + gv.Version
				}
				list := &metav1.APIResourceList{}
				err := client.Discovery().RESTClient().Get().AbsPath(path).Do(probeCtx).Into(list)
				if apierrors.IsNotFound(err) {
					list = &metav1.APIResourceList{}
				} else if err != nil {
					return
				}
				mu.Lock()
				lists[gv] = list
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	absent := map[string]bool{}
	for _, p := range probes {
		conclusivelyAbsent := true
		for _, gvr := range resolveProbeGVRs(p) {
			list, ok := lists[gvr.GroupVersion()]
			if !ok {
				conclusivelyAbsent = false
				break
			}
			for _, resource := range list.APIResources {
				if resource.Name == gvr.Resource {
					conclusivelyAbsent = false
					break
				}
			}
		}
		if conclusivelyAbsent {
			absent[p.key] = true
		}
	}
	return absent
}

func resourceAbsent(group, resource string) bool {
	resourcePermsMu.RLock()
	for _, p := range resourceProbeTargets(&ResourcePermissions{}) {
		if p.gvr.Group == group && p.gvr.Resource == resource {
			absent := cachedPermResult != nil && cachedPermResult.NotServed[p.key]
			resourcePermsMu.RUnlock()
			return absent
		}
	}
	resourcePermsMu.RUnlock()
	if disc := GetResourceDiscovery(); disc != nil {
		return disc.ResourceAbsent(group, resource)
	}
	return false
}

func KindNotServed(kind, group string) bool {
	gvr, ok := lookupTypedBuiltinGVR(kind)
	if ok && (group == "" || group == gvr.Group) {
		return resourceAbsent(gvr.Group, gvr.Resource)
	}
	if disc := GetResourceDiscovery(); disc != nil {
		return disc.ResourceAbsent(group, strings.ToLower(kind))
	}
	return false
}
