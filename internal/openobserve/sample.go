package openobserve

import (
	"context"
	"errors"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// BuildPodSample picks up to oo.MaxSamplePods running Pods from the
// namespace with the most running Pods, for the name suggestion.
func BuildPodSample(ctx context.Context, client kubernetes.Interface) (oo.PodSample, error) {
	list, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "status.phase=" + string(corev1.PodRunning)})
	if err != nil {
		return oo.PodSample{}, fmt.Errorf("list running Pods: %w", err)
	}
	byNS := map[string][]oo.PodRef{}
	for _, p := range list.Items {
		if p.Status.Phase != corev1.PodRunning || p.UID == "" {
			continue
		}
		byNS[p.Namespace] = append(byNS[p.Namespace], oo.PodRef{Name: p.Name, UID: string(p.UID)})
	}
	if len(byNS) == 0 {
		return oo.PodSample{}, errors.New("this cluster has no running Pods to look for in OpenObserve")
	}
	namespaces := make([]string, 0, len(byNS))
	for ns := range byNS {
		namespaces = append(namespaces, ns)
	}
	sort.Slice(namespaces, func(i, j int) bool {
		a, b := namespaces[i], namespaces[j]
		if len(byNS[a]) != len(byNS[b]) {
			return len(byNS[a]) > len(byNS[b])
		}
		return a < b
	})
	ns := namespaces[0]
	pods := byNS[ns]
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	if len(pods) > oo.MaxSamplePods {
		pods = pods[:oo.MaxSamplePods]
	}
	return oo.PodSample{Namespace: ns, Pods: pods}, nil
}
