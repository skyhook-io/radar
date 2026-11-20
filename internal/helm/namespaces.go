package helm

import (
	"context"
	"slices"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// ResolveNoAuthListNamespaces returns a namespace fanout set for Helm list-style
// reads in no-auth mode. nil means keep the single cluster-wide Helm list.
func ResolveNoAuthListNamespaces(ctx context.Context) []string {
	namespaces, _ := ResolveNoAuthStorageScope(ctx)
	return namespaces
}

// ResolveNoAuthStorageScope is ResolveNoAuthListNamespaces that also returns
// the namespaces it leaves out because Radar's identity can't list Secrets
// there, so a read can say what it didn't check.
//
// Whether Radar can list Secrets cluster-wide is asked on every call: it's one
// cheap check, and asking keeps a grant that was narrowed or added since
// startup from sending reads to the wrong place. When it can't, the
// permission probe already tried each candidate namespace, so a namespace it
// saw refused is skipped instead of costing a denied list call per namespace
// on every request (on a default install, where Radar can't read Secrets
// anywhere, that was one call per namespace each time the Helm view, Home or
// Issues loaded). A namespace granted Secrets after the probe stays skipped
// until the probe runs again.
func ResolveNoAuthStorageScope(ctx context.Context) (namespaces, skipped []string) {
	accessible, _ := k8s.GetAccessibleNamespaces(ctx)
	if len(accessible) == 0 {
		return nil, nil
	}
	allowed, apiErr := k8score.CanI(ctx, k8s.GetClient(), "", "", "secrets", "list")
	if !apiErr && allowed {
		return nil, nil
	}
	if read, skipped, ok := probedSecretNamespaces(k8s.GetCachedPermissionResult(), accessible); ok {
		return read, skipped
	}
	return accessible, nil
}

// probedSecretNamespaces splits accessible by the namespaces where the
// permission probe saw Secrets refused (a 403). A namespace the probe didn't
// answer (out of time, a transient error, rejected credentials, created since)
// is still read. ok is false when the probe recorded no refusal or saw Radar's
// credentials rejected, which the read itself should report. read nil means
// every namespace was refused: one cluster-wide list then answers for all.
func probedSecretNamespaces(probe *k8s.PermissionCheckResult, accessible []string) (read, skipped []string, ok bool) {
	if probe == nil {
		return nil, nil, false
	}
	if _, rejected := probe.ProbeErrors[k8score.Secrets]; rejected {
		return nil, nil, false
	}
	denied := probe.DeniedNamespaces[k8score.Secrets]
	if len(denied) == 0 {
		return nil, nil, false
	}
	for _, ns := range accessible {
		if slices.Contains(denied, ns) {
			skipped = append(skipped, ns)
		} else {
			read = append(read, ns)
		}
	}
	if len(read) == 0 {
		return nil, nil, true
	}
	return read, skipped, true
}
