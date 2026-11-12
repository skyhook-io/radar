package server

import (
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
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

type sealedSecretCounts struct {
	Total     int  `json:"total"`
	Synced    int  `json:"synced"`
	NotSynced int  `json:"notSynced"`
	Partial   bool `json:"partial"`
}

type sealedSecretsControllerResponse struct {
	Installed       bool                        `json:"installed"`
	Controller      *sealedSecretsControllerRef `json:"controller,omitempty"`
	ControllerState string                      `json:"controllerState"`
	Keys            []sealingKey                `json:"keys,omitempty"`
	KeysState       string                      `json:"keysState"`
	Counts          *sealedSecretCounts         `json:"counts,omitempty"`
}

var sealedSecretsKind = integration.WorkspaceKind{Key: "sealedSecrets", Group: "bitnami.com", Kind: "SealedSecret", Resource: "sealedsecrets"}

// handleSealedSecretsController answers what the SealedSecret drawer shows
// beside one SealedSecret: the controller, the sealing keys and the totals.
//
// Each part has its own gate, and a denied part is a state in a 200, never a
// 403 for the whole answer:
//   - controller: the caller's `get deployments` in the controller namespace;
//   - keys: the caller's `list secrets` there. Keys come from Radar's Secret
//     informer and are projected to names and the public certificate's dates:
//     only Data["tls.crt"] is read, so tls.key, the PEM, labels and
//     annotations never reach the response;
//   - counts: the SealedSecrets the caller can list (partial when that is not
//     cluster-wide), omitted when they can list none.
//
// In shared no-auth mode the names and dates are Radar's own read and carry
// no secret material.
func (s *Server) handleSealedSecretsController(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	resp := sealedSecretsControllerResponse{ControllerState: "notFound", KeysState: "unknown"}
	if d := k8s.GetResourceDiscovery(); d != nil {
		_, resp.Installed = d.GetGVRWithGroup("SealedSecret", "bitnami.com")
	}
	if !resp.Installed {
		s.writeJSON(w, resp)
		return
	}

	lister := cache.Deployments()
	if lister == nil {
		resp.ControllerState = "notCollected"
	} else if deployments, err := lister.List(labels.Everything()); err != nil {
		log.Printf("[sealedsecrets] Failed to list Deployments: %v", err)
		resp.ControllerState = "notCollected"
	} else if d := findSealedSecretsController(deployments); d != nil {
		ns := d.Namespace
		if s.canRead(r, "apps", "deployments", ns, "get") {
			ref := sealedSecretsControllerRefFor(d)
			resp.Controller, resp.ControllerState = &ref, "ok"
		} else {
			resp.ControllerState = "denied"
		}
		if !s.canRead(r, "", "secrets", ns, "list") {
			resp.KeysState = "denied"
		} else if objs, ok := cache.ReadableObjects("secrets", []string{ns}); !ok {
			resp.KeysState = "notCollected"
		} else {
			resp.Keys, resp.KeysState = sealingKeysFromSecrets(objs), "ok"
		}
	}

	access, items := s.readWorkspaceKind(r, cache, sealedSecretsKind, s.parseNamespacesForUser(r), []string{"bitnami.com"}, newSyncBudget(r.Context()))
	if access.State == integration.KindCoverageFull || access.State == integration.KindCoveragePartial {
		counts := &sealedSecretCounts{Partial: !access.All || access.State == integration.KindCoveragePartial}
		for _, it := range items {
			counts.Total++
			conds, _, _ := unstructured.NestedSlice(it.Object, "status", "conditions")
			for _, c := range conds {
				cm, _ := c.(map[string]any)
				if cm["type"] != "Synced" {
					continue
				}
				switch cm["status"] {
				case "True":
					counts.Synced++
				case "False":
					counts.NotSynced++
				}
			}
		}
		resp.Counts = counts
	}
	s.writeJSON(w, resp)
}
