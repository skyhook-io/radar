package server

import (
	"sort"
	"time"

	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// sealingKeyLabel marks the Secrets the sealed-secrets controller keeps its
// sealing keys in. Only the value "active" is a key the controller uses.
const sealingKeyLabel = "sealedsecrets.bitnami.com/sealed-secrets-key"

type sealedSecretsControllerRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`
}

// sealingKey is everything the drawer shows about a sealing key: names and
// public-certificate dates. No key material, PEM, labels or annotations.
type sealingKey struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	NotBefore string    `json:"notBefore,omitempty"`
	NotAfter  string    `json:"notAfter,omitempty"`
	Active    bool      `json:"active"`
	NotUsed   bool      `json:"notUsed,omitempty"`
}

// findSealedSecretsController picks the controller Deployment: one labelled
// app.kubernetes.io/name=sealed-secrets, else one named
// sealed-secrets-controller; several → the first by namespace/name.
func findSealedSecretsController(deployments []*appsv1.Deployment) *appsv1.Deployment {
	var labelled, named []*appsv1.Deployment
	for _, d := range deployments {
		if d == nil {
			continue
		}
		if d.Labels["app.kubernetes.io/name"] == "sealed-secrets" {
			labelled = append(labelled, d)
		} else if d.Name == "sealed-secrets-controller" {
			named = append(named, d)
		}
	}
	for _, set := range [][]*appsv1.Deployment{labelled, named} {
		if len(set) == 0 {
			continue
		}
		sort.Slice(set, func(i, j int) bool {
			if set[i].Namespace != set[j].Namespace {
				return set[i].Namespace < set[j].Namespace
			}
			return set[i].Name < set[j].Name
		})
		return set[0]
	}
	return nil
}

func sealedSecretsControllerRefFor(d *appsv1.Deployment) sealedSecretsControllerRef {
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	return sealedSecretsControllerRef{
		Kind: "Deployment", Namespace: d.Namespace, Name: d.Name,
		Ready: desired > 0 && d.Status.ReadyReplicas >= desired,
	}
}

// sealingKeysFromSecrets projects the labelled Secrets to names and dates,
// newest first. It reads Data["tls.crt"] (the public certificate) and never
// any other key of Data. The active key is the newest one labelled "active",
// which is the one the controller seals new secrets with.
func sealingKeysFromSecrets(objs []runtime.Object) []sealingKey {
	type entry struct {
		key  sealingKey
		when time.Time
	}
	var entries []entry
	for _, o := range objs {
		sec, ok := o.(*corev1.Secret)
		if !ok || sec == nil {
			continue
		}
		value, labelled := sec.Labels[sealingKeyLabel]
		if !labelled {
			continue
		}
		k := sealingKey{Name: sec.Name, CreatedAt: sec.CreationTimestamp.Time, NotUsed: value != "active"}
		when := k.CreatedAt
		if certs := topology.ParsePEMCertificates(sec.Data["tls.crt"]); len(certs) > 0 {
			k.NotBefore, k.NotAfter = certs[0].NotBefore, certs[0].NotAfter
			if t, err := time.Parse(time.RFC3339, k.NotBefore); err == nil {
				when = t
			}
		}
		entries = append(entries, entry{k, when})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].when.Equal(entries[j].when) {
			return entries[i].when.After(entries[j].when)
		}
		return entries[i].key.Name < entries[j].key.Name
	})
	out := make([]sealingKey, 0, len(entries))
	activeSet := false
	for _, e := range entries {
		if !activeSet && !e.key.NotUsed {
			e.key.Active, activeSet = true, true
		}
		out = append(out, e.key)
	}
	return out
}
