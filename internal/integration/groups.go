package integration

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// keepGroups drops anything whose apiVersion is not one of groups, so an
// object of another group can never ride along on a kind-name match (a Velero
// Backup on a CloudNativePG Backup, a CAPI Cluster on a CNPG Cluster).
func KeepGroups(items []*unstructured.Unstructured, groups []string) []*unstructured.Unstructured {
	out := items[:0:0]
	for _, u := range items {
		if u == nil || !slices.Contains(groups, u.GroupVersionKind().Group) {
			continue
		}
		out = append(out, u)
	}
	return out
}
