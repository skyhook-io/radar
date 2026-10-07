package configrefs

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
)

// ServiceAccountSecretRef describes a literal association in a ServiceAccount.
// It does not assert that a Pod reads the Secret or that a token Secret exists.
type ServiceAccountSecretRef struct {
	Ref
	Path      string
	ImagePull bool
}

// ServiceAccountSecretReferences reads named, same-namespace Secret references
// without resolving targets or reading Secret contents. Preserve both roles
// when one Secret is declared in both fields.
func ServiceAccountSecretReferences(sa *corev1.ServiceAccount) []ServiceAccountSecretRef {
	if sa == nil {
		return nil
	}
	var out []ServiceAccountSecretRef
	seen := map[ServiceAccountSecretRef]bool{}
	add := func(name, path string, imagePull bool) {
		if name == "" {
			return
		}
		ref := ServiceAccountSecretRef{Ref: Ref{Kind: "Secret", Namespace: sa.Namespace, Name: name}, Path: path, ImagePull: imagePull}
		if !seen[ref] {
			out = append(out, ref)
			seen[ref] = true
		}
	}
	for _, ref := range sa.Secrets {
		if ref.Namespace != "" && ref.Namespace != sa.Namespace || ref.Kind != "" && ref.Kind != "Secret" || ref.APIVersion != "" && resourceid.GroupFromAPIVersion(ref.APIVersion) != "" {
			continue
		}
		add(ref.Name, "secrets[]", false)
	}
	for _, ref := range sa.ImagePullSecrets {
		add(ref.Name, "imagePullSecrets[]", true)
	}
	return out
}
