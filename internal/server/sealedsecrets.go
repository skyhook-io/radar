package server

import (
	"encoding/pem"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
)

// sealingKeyLabel marks the Secrets the sealed-secrets controller keeps its
// sealing keys in. Only the value "active" is a key the controller uses.
const sealingKeyLabel = "sealedsecrets.bitnami.com/sealed-secrets-key"

// legacySealingKeyName is the unlabelled key Secret the controller also loads:
// the one named by its --key-prefix, whose default this is.
const legacySealingKeyName = "sealed-secrets-key"

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

// findSealedSecretsController picks the controller Deployment among those
// labelled app.kubernetes.io/name=sealed-secrets or named
// sealed-secrets-controller: kube-system/sealed-secrets-controller (kubeseal's
// defaults), else the only match in kube-system, else the only match anywhere.
// Anyone who can create a Deployment can match, so several candidates are
// reported as ambiguous rather than resolved by sort order, which would let an
// early-sorting namespace stand in for the controller and have its Secrets
// shown as sealing keys.
func findSealedSecretsController(deployments []*appsv1.Deployment) (controller *appsv1.Deployment, ambiguous bool) {
	var system, elsewhere []*appsv1.Deployment
	for _, d := range deployments {
		if d == nil || (d.Labels["app.kubernetes.io/name"] != "sealed-secrets" && d.Name != sealedSecretsControllerName) {
			continue
		}
		if d.Namespace != metav1.NamespaceSystem {
			elsewhere = append(elsewhere, d)
			continue
		}
		if d.Name == sealedSecretsControllerName {
			return d, false
		}
		system = append(system, d)
	}
	for _, set := range [][]*appsv1.Deployment{system, elsewhere} {
		switch len(set) {
		case 0:
			continue
		case 1:
			return set[0], false
		}
		return nil, true
	}
	return nil, false
}

const sealedSecretsControllerName = "sealed-secrets-controller"

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

// sealingKeysFromSecrets projects the controller's key Secrets to names and
// dates, newest first. It reads Data["tls.crt"] (the public certificate) and
// never any other key of Data.
//
// It loads keys the way the controller does: Secrets labelled "active", plus
// the Secret named by --key-prefix whatever its label. Other label values are
// listed as not used. The key marked active is the newest loaded key by the
// controller's --key-order-priority (certificate NotBefore by default, or
// Secret creation time), ties going to the earliest created, as the
// controller only replaces its latest key with a strictly newer one. A key
// whose certificate doesn't parse is never loaded by the controller. With an
// unreadable key order no key is marked.
func sealingKeysFromSecrets(objs []runtime.Object, s sealedSecretsSettings) []sealingKey {
	order := s.keyOrder()
	legacy := s.legacyKeyName()
	if legacy == "" {
		legacy = legacySealingKeyName
	}
	type entry struct {
		key    sealingKey
		when   time.Time
		parsed bool
	}
	var entries []entry
	for _, o := range objs {
		sec, ok := o.(*corev1.Secret)
		if !ok || sec == nil {
			continue
		}
		value, labelled := sec.Labels[sealingKeyLabel]
		if sec.Name == legacy {
			value, labelled = "active", true
		}
		if !labelled {
			continue
		}
		k := sealingKey{Name: sec.Name, CreatedAt: sec.CreationTimestamp.Time, NotUsed: value != "active"}
		when, parsed := k.CreatedAt, false
		// Only the first block is the key's certificate: a malformed one must
		// not hand the key a later block's dates.
		if block, _ := pem.Decode(sec.Data["tls.crt"]); block == nil || block.Type != "CERTIFICATE" {
			// no usable certificate
		} else if certs := topology.ParsePEMCertificates(pem.EncodeToMemory(block)); len(certs) > 0 {
			k.NotBefore, k.NotAfter, parsed = certs[0].NotBefore, certs[0].NotAfter, true
			if t, err := time.Parse(time.RFC3339, k.NotBefore); err == nil && order != keyOrderCreationTime {
				when = t
			}
		}
		entries = append(entries, entry{k, when, parsed})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].when.Equal(entries[j].when) {
			return entries[i].when.After(entries[j].when)
		}
		if !entries[i].key.CreatedAt.Equal(entries[j].key.CreatedAt) {
			return entries[i].key.CreatedAt.Before(entries[j].key.CreatedAt)
		}
		return entries[i].key.Name < entries[j].key.Name
	})
	out := make([]sealingKey, 0, len(entries))
	activeSet := order == keyOrderUnknown
	for _, e := range entries {
		if !activeSet && !e.key.NotUsed && e.parsed {
			e.key.Active, activeSet = true, true
		}
		out = append(out, e.key)
	}
	return out
}

// unreadKeysState says why the controller namespace's Secrets can't be read
// from the cache: still syncing (Radar just started or switched context),
// failed to load, or not held by Radar at all.
func unreadKeysState(a k8score.KindReadAssessment) string {
	switch {
	case len(a.Loading) > 0:
		return "loading"
	case len(a.Failed) > 0:
		return "failed"
	}
	return "notCollected"
}

// absentControllerState says why no controller Deployment was found: the
// Deployment cache is still syncing or failed to load, or Radar watches
// Deployments in some namespaces only (the controller may be in another).
// Only a complete cluster-wide read means there is none.
func absentControllerState(a k8score.KindReadAssessment, namespaceScoped bool) string {
	switch {
	case a.Readiness == k8score.KindPending && len(a.Failed) == 0 || len(a.Loading) > 0:
		return "loading"
	// A cluster-wide informer has no per-namespace partition: its failure
	// shows only in Readiness.
	case len(a.Failed) > 0 || a.Readiness == k8score.KindFailed:
		return "failed"
	case namespaceScoped:
		return "notCollected"
	}
	return "notFound"
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
	// AmbiguousReason, with controllerState "ambiguous": "overlap" (several
	// controllers serve the namespace) or "unknownScope" (Radar can't read
	// which namespaces one of them serves). Never names them.
	AmbiguousReason string `json:"ambiguousReason,omitempty"`
	// AtKubesealDefault: the controller serving this namespace is
	// kube-system/sealed-secrets-controller, where a bare `kubeseal
	// --fetch-cert` reaches it. Set even when the controller is denied:
	// upstream installs let every authenticated user get that Service by
	// name, so kubeseal already reveals it.
	AtKubesealDefault bool         `json:"atKubesealDefault,omitempty"`
	Keys              []sealingKey `json:"keys"`
	KeysState         string       `json:"keysState"`
	// KeyOrder, with keysState "ok": how the controller picks its sealing
	// key, "certNotBefore", "creationTime", or "unknown" (none is marked).
	KeyOrder string              `json:"keyOrder,omitempty"`
	Counts   *sealedSecretCounts `json:"counts,omitempty"`
}

// answerSealedSecretsController fills the controller and its keys for the
// chosen controller d, each behind the caller's own access.
func (s *Server) answerSealedSecretsController(r *http.Request, cache *k8s.ResourceCache, d *appsv1.Deployment, settings sealedSecretsSettings, resp *sealedSecretsControllerResponse) {
	ns := d.Namespace
	resp.AtKubesealDefault = ns == metav1.NamespaceSystem && d.Name == sealedSecretsControllerName
	if s.canRead(r, "apps", "deployments", ns, "get") {
		ref := sealedSecretsControllerRefFor(d)
		resp.Controller, resp.ControllerState = &ref, "ok"
	} else {
		resp.ControllerState = "denied"
	}
	if !s.canRead(r, "", "secrets", ns, "list") {
		resp.KeysState = "denied"
	} else if objs, ok := cache.ReadableObjects("secrets", []string{ns}); !ok {
		resp.KeysState = unreadKeysState(cache.AssessKindRead("secrets", []string{ns}))
	} else {
		resp.Keys, resp.KeysState, resp.KeyOrder = sealingKeysFromSecrets(objs, settings), "ok", settings.keyOrder()
	}
}

// sealedSecretsControllerChoice is the controller serving a namespace, or,
// with controller nil, why none can be named: state is "ambiguous" (with
// ambiguousReason), "notWatched", or what absentControllerState says.
type sealedSecretsControllerChoice struct {
	controller      *appsv1.Deployment
	settings        sealedSecretsSettings
	state           string
	ambiguousReason string
}

// selectSealedSecretsController picks the controller from Radar's Deployment
// cache: the one serving target (chooseControllerFor), or with target empty
// the one kubeseal's defaults reach (findSealedSecretsController).
func selectSealedSecretsController(cache *k8s.ResourceCache, target string, requireUnique bool) sealedSecretsControllerChoice {
	lister := cache.Deployments()
	if lister == nil {
		return sealedSecretsControllerChoice{state: "notCollected"}
	}
	deployments, err := lister.List(labels.Everything())
	if err != nil {
		log.Printf("[sealedsecrets] Failed to list Deployments: %v", err)
		return sealedSecretsControllerChoice{state: "notCollected"}
	}
	var candidates []*appsv1.Deployment
	for _, d := range deployments {
		if isSealedSecretsControllerCandidate(d) {
			candidates = append(candidates, d)
		}
	}
	var choice sealedSecretsControllerChoice
	watched := true
	if target == "" {
		var ambiguous bool
		if choice.controller, ambiguous = findSealedSecretsController(candidates); ambiguous {
			choice.ambiguousReason = ambiguousOverlap
		} else if choice.controller != nil {
			choice.settings = readSealedSecretsSettings(choice.controller)
		}
	} else if requireUnique {
		for _, d := range candidates {
			settings := readSealedSecretsSettings(d)
			switch coverageOf(d, settings, target) {
			case coverageUnknown:
				return sealedSecretsControllerChoice{state: "ambiguous", ambiguousReason: ambiguousUnknownScope}
			case coverageExplicit, coverageAll:
				if choice.controller != nil {
					return sealedSecretsControllerChoice{state: "ambiguous", ambiguousReason: ambiguousOverlap}
				}
				choice.controller, choice.settings = d, settings
			}
		}
		watched = choice.controller != nil
	} else {
		choice.controller, choice.settings, choice.ambiguousReason, watched = chooseControllerFor(candidates, target)
	}
	absent := func() string {
		return absentControllerState(cache.AssessKindRead("deployments", nil), cache.KindNamespaces("deployments") != nil)
	}
	switch {
	case choice.ambiguousReason != "":
		choice.state = "ambiguous"
	case choice.controller == nil && len(candidates) > 0 && !watched:
		// Controllers exist but none serves this namespace; only a complete
		// cluster-wide read can say that.
		if choice.state = absent(); choice.state == "notFound" {
			choice.state = "notWatched"
		}
	case choice.controller == nil:
		choice.state = absent()
	}
	return choice
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
// With ?sealedSecretNamespace= (the drawer's SealedSecret namespace; not
// ?namespace=, which is the namespace filter the totals honour) the controller is
// the one serving that namespace (chooseControllerFor); "notWatched" when no
// controller serves it. Without it, kubeseal's defaults decide
// (findSealedSecretsController). The controller's flags, read from its
// Deployment, decide its sealing key and legacy key.
//
// In shared no-auth mode the names and dates are Radar's own read and carry
// no secret material. A caller denied the controller learns only that one
// exists (controllerState "denied", not "notFound"), and, through
// atKubesealDefault, whether it sits at kubeseal's default location, which a
// bare `kubeseal --fetch-cert` would reveal to them anyway. Several
// candidate controllers read "ambiguous" for every caller, with the reason
// but no names, no controller and no keys: like "denied", it says only that
// look-alikes exist.
func (s *Server) handleSealedSecretsController(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	target := r.URL.Query().Get("sealedSecretNamespace")
	if target != "" && len(validation.IsDNS1123Label(target)) > 0 {
		s.writeError(w, http.StatusBadRequest, "invalid namespace")
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

	choice := selectSealedSecretsController(cache, target, false)
	resp.AmbiguousReason = choice.ambiguousReason
	if choice.controller == nil {
		resp.ControllerState = choice.state
	} else {
		s.answerSealedSecretsController(r, cache, choice.controller, choice.settings, &resp)
	}

	// Counting reads only status; the copy would include every encryptedData.
	budget := newSyncBudget(r.Context())
	budget.readOnly = true
	access, items := s.readWorkspaceKind(r, cache, sealedSecretsKind, s.parseNamespacesForUser(r), []string{"bitnami.com"}, budget)
	if access.State == integration.KindCoverageFull || access.State == integration.KindCoveragePartial {
		counts := &sealedSecretCounts{Partial: !access.All || access.State == integration.KindCoveragePartial}
		for _, it := range items {
			counts.Total++
			conds, _, _ := unstructured.NestedFieldNoCopy(it.Object, "status", "conditions")
			list, _ := conds.([]any)
			for _, c := range list {
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
