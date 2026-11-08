package resourcecontext

import (
	"context"
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/topology"
)

func controlledPod() *corev1.Pod {
	yes := true
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-0", UID: "uid-pod", OwnerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: "uid-rs", Controller: &yes},
		}},
	}
}

// An ownership answer that names no owner says why in the context, as the
// graph's answer did: the reader may not view it, or Radar couldn't tell.
func TestBuildSaysWhyOwnershipNamedNoOwner(t *testing.T) {
	for _, tc := range []struct {
		name     string
		coverage topology.OwnershipCoverage
		want     OmittedReason
	}{
		{"withheld", topology.OwnershipCoverage{PrimaryOwnerState: "withheld"}, OmittedRBACDenied},
		{"unknown", topology.OwnershipCoverage{PrimaryOwnerState: "unknown"}, OmittedUnavailable},
		{"unavailable", topology.OwnershipCoverage{UnavailableReason: "connection_changed"}, OmittedUnavailable},
		{"gone", topology.OwnershipCoverage{PrimaryOwnerState: "gone"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := Build(context.Background(), controlledPod(), Options{
				AccessChecker: allowAllChecker{},
				Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
					c := tc.coverage
					rel.Ownership = &c
				},
			})
			if rc.Owner != nil {
				t.Fatalf("owner = %+v; the ownership answer names none", rc.Owner)
			}
			var got OmittedReason
			for _, o := range rc.Omitted {
				if o.Field == "owner" {
					got = o.Reason
				}
			}
			if got != tc.want {
				t.Fatalf("owner omitted for %q, want %q (omitted %+v)", got, tc.want, rc.Omitted)
			}
		})
	}
}

// Ownership doesn't follow an Event's owner references, so an Event's context
// names no owner, rather than the unchecked one its own reference names.
func TestBuildNamesNoOwnerForAnEvent(t *testing.T) {
	event := &corev1.Event{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Event"}, ObjectMeta: metav1.ObjectMeta{
		Namespace: "shop", Name: "e", UID: "uid-event", OwnerReferences: controlledPod().OwnerReferences,
	}}
	s, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	v := ownership.NewView(s, ownership.Unrestricted())
	rc := Build(context.Background(), event, Options{
		AccessChecker: allowAllChecker{},
		Ownership:     func(rel *topology.Relationships, fetched ownership.Observation) { topology.ApplyOwner(rel, v, fetched) },
	})
	if rc.Owner != nil {
		t.Fatalf("owner = %+v; ownership names none for an Event", rc.Owner)
	}
}

// Without a graph, the managers an object's own labels and annotations name
// Metadata alone must not manufacture a second management answer.
func TestBuildIgnoresMetadataManagedByWithoutAnOwnershipAnswer(t *testing.T) {
	deploy := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", Annotations: map[string]string{"argocd.argoproj.io/tracking-id": "storefront:apps/Deployment:shop/web"}}}
	rc := Build(context.Background(), deploy, Options{AccessChecker: allowAllChecker{}})
	if len(rc.ManagedBy) != 0 {
		t.Fatalf("metadata-only managedBy: %+v", rc.ManagedBy)
	}
}

// The manager answer reaches the resource context compacted: the evidence
// that answers, the controller's when it does, and no copied claim.
func TestBuildCompactsTheManagerAnswer(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-1-a", UID: "uid-pod"},
	}
	app := topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "shop"}
	other := topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Name: "old"}
	deploy := topology.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	rc := Build(context.Background(), pod, Options{AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
		rel.Ownership = &topology.OwnershipCoverage{Manager: &topology.ManagerAnswer{
			State: "agree", Lead: &app, Narrowed: true,
			Direct: topology.ManagerEvidence{Object: topology.ResourceRef{Kind: "Pod", Namespace: "shop", Name: "web-1-a"}, Claims: []topology.ManagerClaim{
				{Manager: topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Name: "tmpl"}, Source: "argo-instance-label", Check: "unresolved", Weak: true},
			}},
			Via: &topology.ManagerEvidence{Object: deploy, Recorders: []topology.ResourceRef{app}, Claims: []topology.ManagerClaim{
				{Manager: app, Source: "argo-tracking-id", Check: "recorded"},
				{Manager: other, Source: "argo-tracking-id", Check: "missing", Copied: true},
			}},
		}}
	}})
	m := rc.Manager
	if m == nil || m.State != "agree" || m.Lead == nil || m.Lead.Name != "shop" || m.Via == nil || m.Via.Name != "web" || !m.Narrowed {
		t.Fatalf("manager %+v; want agree, led by shop, via the Deployment, narrowed", m)
	}
	if len(m.ListedBy) != 1 || len(m.Claims) != 1 || m.Claims[0].Check != "recorded" {
		t.Fatalf("listedBy %+v claims %+v; want the Deployment's evidence without the copied claim", m.ListedBy, m.Claims)
	}
	if len(m.AlsoClaimed) != 1 || m.AlsoClaimed[0].Manager.Name != "tmpl" || m.AlsoClaimed[0].On == nil || m.AlsoClaimed[0].On.Kind != "Pod" {
		t.Fatalf("alsoClaimed %+v; want the Pod's own weak claim, on the Pod", m.AlsoClaimed)
	}
}

// A claim the walk passed is kept even when no ancestor answers.
func TestBuildKeepsClaimsOnTheWayWithoutVia(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-1-a", UID: "uid-pod"},
	}
	rs := topology.ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "shop", Name: "web-1"}
	rc := Build(context.Background(), pod, Options{AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
		rel.Ownership = &topology.OwnershipCoverage{Manager: &topology.ManagerAnswer{
			State:   "claimed",
			Direct:  topology.ManagerEvidence{Object: topology.ResourceRef{Kind: "Pod", Namespace: "shop", Name: "web-1-a"}, Claims: []topology.ManagerClaim{{Manager: topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Name: "pod-app"}, Source: "argo-instance-label", Check: "unresolved", Weak: true}}},
			Between: []topology.ManagerEvidence{{Object: rs, Claims: []topology.ManagerClaim{{Manager: topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Name: "rs-app"}, Source: "argo-instance-label", Check: "unresolved", Weak: true}}}},
		}}
	}})
	m := rc.Manager
	if m == nil || m.Via != nil || len(m.AlsoClaimed) != 1 || m.AlsoClaimed[0].Manager.Name != "rs-app" || m.AlsoClaimed[0].On == nil || m.AlsoClaimed[0].On.Kind != "ReplicaSet" {
		t.Fatalf("manager %+v; want the ReplicaSet's claim in alsoClaimed", m)
	}
}

// With a manager answer, managedBy keeps only managers the answer doesn't
// weigh: the GitOps one would be a second answer to the same question.
func TestBuildManagedByLeavesGitOpsManagersToTheAnswer(t *testing.T) {
	secret := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls", UID: "uid-tls"},
	}
	app := topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "shop"}
	cert := topology.ResourceRef{Group: "cert-manager.io", Kind: "Certificate", Namespace: "shop", Name: "tls"}
	build := func(answer *topology.ManagerAnswer) *ResourceContext {
		return Build(context.Background(), secret, Options{AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
			rel.ManagedBy = []topology.ResourceRef{app}
			rel.Managers = []topology.ResourceRef{cert}
			rel.Ownership = &topology.OwnershipCoverage{Manager: answer}
		}})
	}
	with := build(&topology.ManagerAnswer{State: "listed", Lead: &app, Direct: topology.ManagerEvidence{Object: topology.ResourceRef{Kind: "Secret", Namespace: "shop", Name: "tls"}, Recorders: []topology.ResourceRef{app}}})
	if len(with.ManagedBy) != 1 || with.ManagedBy[0].Kind != "Certificate" {
		t.Fatalf("managedBy with an answer = %+v; want only the Certificate", with.ManagedBy)
	}
	if without := build(nil); len(without.ManagedBy) != 1 {
		t.Fatalf("managedBy without an answer = %+v; want only the classified Certificate", without.ManagedBy)
	}
}

// A managedBy the caller can't read leaves no trace: a note would say that a
// manager the caller can't see links to this object.
func TestBuildManagedByHidesUnreadableManagersWithoutANote(t *testing.T) {
	secret := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls", UID: "uid-tls"},
	}
	cert := topology.ResourceRef{Group: "cert-manager.io", Kind: "Certificate", Namespace: "team-b", Name: "tls"}
	build := func(managedBy []topology.ResourceRef) *ResourceContext {
		return Build(context.Background(), secret, Options{AccessChecker: denyChecker{group: "cert-manager.io", kind: "Certificate", namespace: "team-b"}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
			rel.Managers = managedBy
			rel.Ownership = &topology.OwnershipCoverage{}
		}})
	}
	hidden, none := build([]topology.ResourceRef{cert}), build(nil)
	a, _ := json.Marshal(hidden)
	b, _ := json.Marshal(none)
	if string(a) != string(b) {
		t.Fatalf("a hidden manager changes the context:\n%s\nvs\n%s", a, b)
	}
}

func TestBuildReportsUnavailableSpecControllers(t *testing.T) {
	secret := &corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "tls", UID: "uid-tls"}}
	for _, tc := range []struct{ graph, prefix bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		graph := tc.graph
		opts := Options{AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
			rel.Ownership = &topology.OwnershipCoverage{}
			rel.ControllerChain = &topology.ControllerChain{Refs: []topology.ResourceRef{{Kind: "Secret", Namespace: "shop", Name: "tls"}}}
			rel.OtherManagersIncomplete = graph
			if tc.prefix {
				rel.ControllerChain.Refs = append(rel.ControllerChain.Refs, topology.ResourceRef{Group: "example.io", Kind: "Widget", Namespace: "shop", Name: "generator"})
			}
		}}
		if graph {
			opts.Relationships = &topology.Relationships{}
		}
		rc := Build(context.Background(), secret, opts)
		found := false
		for _, o := range rc.Omitted {
			if o.Field == "managedBy" && o.Reason == OmittedUnavailable {
				found = true
			}
		}
		if !found {
			t.Fatalf("graph=%v: missing incompleteness: %+v", graph, rc)
		}
	}
}

// managedBy reaches the top of the caller's controller chain when the graph's
// pointer stops on it (the graph has no node for a custom resource nothing
// owns), and when the manager answer takes the GitOps pointer it would
// otherwise name; a pointer off the chain stands.
func TestBuildManagedByReachesTheTopController(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "basic-tidb-0", UID: "uid-pod"},
	}
	sts := topology.ResourceRef{Group: "apps", Kind: "StatefulSet", Namespace: "db", Name: "basic-tidb"}
	cr := topology.ResourceRef{Group: "pingcap.com", Kind: "TidbCluster", Namespace: "db", Name: "basic"}
	app := topology.ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "db"}
	cert := topology.ResourceRef{Group: "cert-manager.io", Kind: "Certificate", Namespace: "db", Name: "tls"}
	chain := &topology.ControllerChain{Refs: []topology.ResourceRef{{Kind: "Pod", Namespace: "db", Name: "basic-tidb-0"}, sts, cr}}
	answer := &topology.ManagerAnswer{State: "listed", Lead: &app}
	for _, tc := range []struct {
		name     string
		pointer  []topology.ResourceRef
		manager  *topology.ManagerAnswer
		chain    *topology.ControllerChain
		want     string
		wantNote OmittedReason
	}{
		{"the graph stops at the workload", []topology.ResourceRef{sts}, nil, chain, "TidbCluster", ""},
		{"no pointer", nil, nil, chain, "TidbCluster", ""},
		{"a GitOps manager without an answer", []topology.ResourceRef{app}, nil, chain, "TidbCluster", ""},
		{"a GitOps manager the answer takes", []topology.ResourceRef{app}, answer, chain, "TidbCluster", ""},
		{"a GitOps manager at the chain's top stays the answer's", nil, answer, &topology.ControllerChain{Refs: []topology.ResourceRef{{Kind: "Pod", Namespace: "db", Name: "basic-tidb-0"}, sts, app}}, "Application", ""},
		{"another managing controller", []topology.ResourceRef{cert}, nil, chain, "TidbCluster", ""},
		{"an owner Radar is still collecting", nil, nil, &topology.ControllerChain{Refs: chain.Refs[:2], Stop: "unknown", Reason: readreason.KindSyncPending}, "StatefulSet", OmittedCacheCold},
		{"an owner Radar couldn't check", []topology.ResourceRef{sts}, nil, &topology.ControllerChain{Refs: chain.Refs[:2], Stop: "unknown"}, "StatefulSet", OmittedUnavailable},
		{"an owner the caller may not read", nil, nil, &topology.ControllerChain{Refs: chain.Refs[:2], Stop: "withheld"}, "StatefulSet", OmittedRBACDenied},
		{"a GitOps pointer above a stop", []topology.ResourceRef{app}, nil, &topology.ControllerChain{Refs: chain.Refs[:2], Stop: "unknown"}, "StatefulSet", OmittedUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := Build(context.Background(), pod, Options{Topology: &topology.Topology{}, AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
				rel.ManagedBy = tc.pointer
				rel.Ownership = &topology.OwnershipCoverage{Manager: tc.manager}
				rel.ControllerChain = tc.chain
			}})
			if tc.want == "" && len(rc.ManagedBy) != 0 || tc.want != "" && (len(rc.ManagedBy) != 1 || rc.ManagedBy[0].Kind != tc.want) {
				t.Fatalf("managedBy %+v, want %s", rc.ManagedBy, tc.want)
			}
			var note OmittedReason
			for _, o := range rc.Omitted {
				if o.Field == "managedBy" {
					note = o.Reason
				}
			}
			if note != tc.wantNote {
				t.Fatalf("managedBy note %q, want %q", note, tc.wantNote)
			}
		})
	}
}

// The chain read through ApplyOwner reaches the custom resource, end to end.
func TestBuildManagedByFromTheOwnershipView(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cr := b.Add("example.io", "Widget", "db", "basic")
	workload := b.Add("apps", "Deployment", "db", "basic", cr.OwnerRef(true))
	rs := b.Add("apps", "ReplicaSet", "db", "basic-1", workload.OwnerRef(true))
	podObj := b.Add("", "Pod", "db", "basic-1-a", rs.OwnerRef(true))
	s, err := ownershiptest.Scan(b.World()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	v := ownership.NewView(s, ownership.Unrestricted())
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "basic-1-a", UID: podObj.UID, OwnerReferences: podObj.OwnerReferences},
	}
	rc := Build(context.Background(), pod, Options{AccessChecker: allowAllChecker{}, Ownership: func(rel *topology.Relationships, fetched ownership.Observation) {
		topology.ApplyOwner(rel, v, fetched)
	}})
	if len(rc.ManagedBy) != 1 || rc.ManagedBy[0] != (ContextRef{Kind: "Widget", Group: "example.io", Namespace: "db", Name: "basic"}) {
		t.Fatalf("managedBy %+v, want the Widget", rc.ManagedBy)
	}
	if rc.Owner == nil || rc.Owner.Kind != "ReplicaSet" {
		t.Fatalf("owner %+v, want the ReplicaSet", rc.Owner)
	}
}

// A host that may not show the top the walk read (REST reads ownership by
// namespace, references by kind) names the highest object it may show, and
// says an owner is withheld above it.
func TestBuildManagedByStopsBelowAnOwnerTheHostMayNotShow(t *testing.T) {
	pod := &corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "basic-tidb-0", UID: "uid-pod"},
	}
	sts := topology.ResourceRef{Group: "apps", Kind: "StatefulSet", Namespace: "db", Name: "basic-tidb"}
	cr := topology.ResourceRef{Group: "pingcap.com", Kind: "TidbCluster", Namespace: "db", Name: "basic"}
	rc := Build(context.Background(), pod, Options{AccessChecker: denyChecker{group: "pingcap.com", kind: "TidbCluster", namespace: "db"}, Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
		rel.ManagedBy = []topology.ResourceRef{cr}
		rel.Ownership = &topology.OwnershipCoverage{}
		rel.ControllerChain = &topology.ControllerChain{Refs: []topology.ResourceRef{{Kind: "Pod", Namespace: "db", Name: "basic-tidb-0"}, sts, cr}}
	}})
	if len(rc.ManagedBy) != 1 || rc.ManagedBy[0].Kind != "StatefulSet" {
		t.Fatalf("managedBy %+v, want the StatefulSet", rc.ManagedBy)
	}
	var note OmittedReason
	for _, o := range rc.Omitted {
		if o.Field == "managedBy" {
			note = o.Reason
		}
	}
	if note != OmittedRBACDenied {
		t.Fatalf("managedBy note %q, want rbac_denied", note)
	}
}
