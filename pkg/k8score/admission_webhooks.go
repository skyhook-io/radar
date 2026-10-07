package k8score

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/configrefs"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const admissionWebhookServiceIndex = "radar.admissionWebhookService"

func isAdmissionWebhookGVR(gvr schema.GroupVersionResource) bool {
	return gvr.Group == "admissionregistration.k8s.io" && (gvr.Resource == "mutatingwebhookconfigurations" || gvr.Resource == "validatingwebhookconfigurations")
}
func admissionWebhookServiceKeys(obj any) ([]string, error) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, nil
	}
	var keys []string
	seen := map[string]bool{}
	for _, ref := range configrefs.AdmissionWebhookServices(u) {
		key := ref.Service.Key()
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// AdmissionWebhookConsumers looks up configurations by their declared backend
// Service using the informer index. complete is false until that exact
// cluster-wide informer has synced; partial readable matches may still return.
// Hot lookups perform no API calls or full configuration scan.
func (d *DynamicResourceCache) AdmissionWebhookConsumers(gvr schema.GroupVersionResource, namespace, name string) ([]*unstructured.Unstructured, bool, error) {
	if !isAdmissionWebhookGVR(gvr) {
		return nil, false, fmt.Errorf("not an admission webhook configuration GVR: %v", gvr)
	}
	if err := d.ensureWatching(gvr, ""); err != nil {
		return nil, false, err
	}
	d.mu.RLock()
	entry := d.informers[informerKey{gvr: gvr}]
	d.mu.RUnlock()
	if entry == nil {
		return nil, false, fmt.Errorf("cluster-wide admission webhook informer unavailable")
	}
	key := (configrefs.Ref{Kind: "Service", Namespace: namespace, Name: name}).Key()
	objects, err := entry.informer.GetIndexer().ByIndex(admissionWebhookServiceIndex, key)
	if err != nil {
		return nil, false, err
	}
	refs := make([]*unstructured.Unstructured, 0, len(objects))
	for _, obj := range objects {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			refs = append(refs, StripUnstructuredFields(u))
		}
	}
	d.mu.RLock()
	complete := d.informers[informerKey{gvr: gvr}] == entry && entry.informer.HasSynced()
	d.mu.RUnlock()
	return refs, complete, nil
}
