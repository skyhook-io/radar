package subject

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- test doubles (layering-clean: implement the injected interfaces) ---

// fakeOwners is a multi-hop OwnerResolver: refKey(child) -> parent controller.
type fakeOwners map[string]Ref

func (f fakeOwners) ParentOf(child Ref) (Ref, bool) { p, ok := f[refKey(child)]; return p, ok }

// fakeLookup is a single-hop OwnerLookup for the operator-root hook.
type fakeLookup map[string]Ref

func (f fakeLookup) ImmediateOwner(child Ref) (Ref, bool) { p, ok := f[refKey(child)]; return p, ok }

func obj(ns, name string, labels, annos map[string]string, owners ...metav1.OwnerReference) metav1.Object {
	return &metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels, Annotations: annos, OwnerReferences: owners}
}

func ctrlRef(kind, name, apiVersion string) metav1.OwnerReference {
	c := true
	return metav1.OwnerReference{Kind: kind, Name: name, APIVersion: apiVersion, Controller: &c}
}

// ============================ TIER 1: SUBJECT ============================

func TestResolveSubject_PodToDeploymentOwnerCollapse(t *testing.T) {
	pod := obj("ns", "web-abc12345", nil, nil, ctrlRef("ReplicaSet", "web-5d8f9c", "apps/v1"))
	start := Ref{Kind: "Pod", Namespace: "ns", Name: "web-abc12345"}
	got := ResolveSubject(start, HeuristicPodOwnerResolver{Pod: pod}, nil)
	want := Subject{Ref: Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}, Scope: ScopeWorkload, Anchor: AnchorOwnerCollapsed}
	if got != want {
		t.Errorf("Pod->RS->Deployment: got %+v, want %+v", got, want)
	}
}

func TestResolveSubject_MultiHopJobToCronJob(t *testing.T) {
	// A topology-backed OwnerResolver supplies the deeper Pod->Job->CronJob chain.
	podRef := Ref{Kind: "Pod", Namespace: "ns", Name: "backup-29384-xyz"}
	jobRef := Ref{Group: "batch", Kind: "Job", Namespace: "ns", Name: "backup-29384"}
	cronRef := Ref{Group: "batch", Kind: "CronJob", Namespace: "ns", Name: "backup"}
	owners := fakeOwners{refKey(podRef): jobRef, refKey(jobRef): cronRef}
	got := ResolveSubject(podRef, owners, nil)
	want := Subject{Ref: cronRef, Scope: ScopeWorkload, Anchor: AnchorOwnerCollapsed}
	if got != want {
		t.Errorf("Pod->Job->CronJob: got %+v, want %+v", got, want)
	}
}

func TestResolveSubject_BarePod(t *testing.T) {
	pod := obj("ns", "loner", nil, nil) // no owner refs
	start := Ref{Kind: "Pod", Namespace: "ns", Name: "loner"}
	got := ResolveSubject(start, HeuristicPodOwnerResolver{Pod: pod}, nil)
	if got.Anchor != AnchorBare || got.Ref != start {
		t.Errorf("bare pod: got %+v, want anchor=bare ref=%+v", got, start)
	}
}

func TestResolveSubject_StaticPodOwnedByNode(t *testing.T) {
	podRef := Ref{Kind: "Pod", Namespace: "kube-system", Name: "kube-apiserver-node1"}
	owners := fakeOwners{refKey(podRef): {Kind: "Node", Name: "node1"}}
	got := ResolveSubject(podRef, owners, nil)
	if got.Anchor != AnchorNode {
		t.Errorf("static pod (owner=Node): anchor %q, want node", got.Anchor)
	}
	if got.Ref != podRef { // must NOT collapse "up into" the Node
		t.Errorf("static pod must stay the pod, not become the Node: %+v", got.Ref)
	}
}

func TestResolveSubject_NonPodSelf(t *testing.T) {
	svc := Ref{Kind: "Service", Namespace: "ns", Name: "api"}
	got := ResolveSubject(svc, nil, nil) // owners=nil: resource is its own subject
	want := Subject{Ref: svc, Scope: ScopeService, Anchor: AnchorSelf}
	if got != want {
		t.Errorf("non-pod self: got %+v, want %+v", got, want)
	}
}

func TestResolveSubject_OperatorCRRootHop(t *testing.T) {
	podRef := Ref{Kind: "Pod", Namespace: "ns", Name: "pg-1"}
	stsRef := Ref{Group: "apps", Kind: "StatefulSet", Namespace: "ns", Name: "pg"}
	cnpg := Ref{Group: "postgresql.cnpg.io", Kind: "Cluster", Namespace: "ns", Name: "pg"}
	owners := fakeOwners{refKey(podRef): stsRef} // Pod->STS; STS has no further controller
	ops := DefaultOperatorRoots{Owners: fakeLookup{refKey(stsRef): cnpg}}
	got := ResolveSubject(podRef, owners, ops)
	if got.Anchor != AnchorOperatorCR || got.Ref != cnpg {
		t.Errorf("operator-CR hop: got %+v, want ref=%+v anchor=operator_cr", got, cnpg)
	}
}

func TestResolveSubject_StrimziKafkaRootHop(t *testing.T) {
	podRef := Ref{Kind: "Pod", Namespace: "ns", Name: "kafka-0"}
	stsRef := Ref{Group: "apps", Kind: "StatefulSet", Namespace: "ns", Name: "kafka-kafka"}
	kafka := Ref{Group: "kafka.strimzi.io", Kind: "Kafka", Namespace: "ns", Name: "kafka"}
	owners := fakeOwners{refKey(podRef): stsRef}
	ops := DefaultOperatorRoots{Owners: fakeLookup{refKey(stsRef): kafka}}
	got := ResolveSubject(podRef, owners, ops)
	if got.Anchor != AnchorOperatorCR || got.Ref != kafka {
		t.Errorf("Strimzi Kafka hop: got %+v, want ref=%+v anchor=operator_cr", got, kafka)
	}
}

func TestResolveSubject_CrossplaneXRRootHop(t *testing.T) {
	// Crossplane XRs are matched structurally by the apiextensions.crossplane.io
	// group rather than by an enumerated kind.
	podRef := Ref{Kind: "Pod", Namespace: "ns", Name: "db-1"}
	stsRef := Ref{Group: "apps", Kind: "StatefulSet", Namespace: "ns", Name: "db"}
	xr := Ref{Group: "apiextensions.crossplane.io", Kind: "XPostgreSQLInstance", Namespace: "ns", Name: "db"}
	owners := fakeOwners{refKey(podRef): stsRef}
	ops := DefaultOperatorRoots{Owners: fakeLookup{refKey(stsRef): xr}}
	got := ResolveSubject(podRef, owners, ops)
	if got.Anchor != AnchorOperatorCR || got.Ref != xr {
		t.Errorf("Crossplane XR hop: got %+v, want ref=%+v anchor=operator_cr", got, xr)
	}
}

func TestResolveSubject_UnknownOperatorDegradesToWorkload(t *testing.T) {
	// A CR not on the allowlist must NOT hop — degrade to the workload (raw-always).
	podRef := Ref{Kind: "Pod", Namespace: "ns", Name: "x-1"}
	stsRef := Ref{Group: "apps", Kind: "StatefulSet", Namespace: "ns", Name: "x"}
	unknownCR := Ref{Group: "unknown.example.io", Kind: "Widget", Namespace: "ns", Name: "x"}
	owners := fakeOwners{refKey(podRef): stsRef}
	ops := DefaultOperatorRoots{Owners: fakeLookup{refKey(stsRef): unknownCR}}
	got := ResolveSubject(podRef, owners, ops)
	if got.Ref != stsRef || got.Anchor != AnchorOwnerCollapsed {
		t.Errorf("unknown operator CR must degrade to the workload: got %+v", got)
	}
}

func TestScopeForKind(t *testing.T) {
	cases := map[string]Scope{
		"Pod": ScopeWorkload, "Deployment": ScopeWorkload, "Rollout": ScopeWorkload, "CronJob": ScopeWorkload,
		"Service": ScopeService, "Ingress": ScopeIngress, "PersistentVolumeClaim": ScopePVC,
		"Node": ScopeNode, "Frobnicator": ScopeUnknown,
	}
	for k, want := range cases {
		if got := ScopeForKind(k); got != want {
			t.Errorf("ScopeForKind(%q)=%q want %q", k, got, want)
		}
	}
}

func TestStripReplicaSetHash(t *testing.T) {
	cases := map[string]string{"web-5d8f9c": "web", "my-app-7b9": "my-app", "noHyphen": "noHyphen", "-leading": "-leading"}
	for in, want := range cases {
		if got := StripReplicaSetHash(in); got != want {
			t.Errorf("StripReplicaSetHash(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIssueID_DeterministicAndCategoryKeyed(t *testing.T) {
	a := StableID(ScopeWorkload, "apps|Deployment|ns|web", "crashloop")
	if a != StableID(ScopeWorkload, "apps|Deployment|ns|web", "crashloop") {
		t.Error("IssueID must be deterministic")
	}
	if len(a) != 16 {
		t.Errorf("IssueID len=%d, want 16 hex chars (8 bytes)", len(a))
	}
	// Each component must change the id. category being part of the key is what
	// the detector-monotonicity contract depends on (a flapping category churns
	// the id), so it must hold; subject-key and scope must also be load-bearing.
	if StableID(ScopeWorkload, "apps|Deployment|ns|web", "image_pull_failed") == a {
		t.Error("IssueID must change with category")
	}
	if StableID(ScopeWorkload, "apps|Deployment|ns|other", "crashloop") == a {
		t.Error("IssueID must change with the subject key")
	}
	if StableID(ScopeService, "apps|Deployment|ns|web", "crashloop") == a {
		t.Error("IssueID must change with scope")
	}
}

// ============================ TIER 2: APP OVERLAY ============================

func TestResolveOverlay_FluxHelmReleaseTier1(t *testing.T) {
	ov := ResolveOverlay(obj("ns", "x", map[string]string{fluxHelmNameLabel: "podinfo", fluxHelmNSLabel: "flux-system"}, nil), false)
	if ov == nil || ov.Winner.Tier != TierFluxHelmRelease {
		t.Fatalf("flux HelmRelease: got %+v", ov)
	}
	if ov.Winner.Ref.Kind != "HelmRelease" || ov.Winner.Ref.Group != fluxHelmGroup {
		t.Errorf("flux HR ref: %+v", ov.Winner.Ref)
	}
	if ov.Winner.Confidence != ConfidenceHigh {
		t.Errorf("tier1 confidence=%q want high", ov.Winner.Confidence)
	}
}

func TestResolveOverlay_NativeHelmSentinelGroupEmpty(t *testing.T) {
	ov := ResolveOverlay(obj("ns", "x", nil, map[string]string{helmReleaseNameAnno: "redis", helmReleaseNSAnno: "ns"}), false)
	if ov == nil || ov.Winner.Tier != TierHelmRelease {
		t.Fatalf("native helm: got %+v", ov)
	}
	// The {Kind:HelmRelease, Group:""} sentinel must survive — the Source
	// classifier keys "helm" off the empty group.
	if ov.Winner.Ref.Kind != "HelmRelease" || ov.Winner.Ref.Group != "" {
		t.Errorf("native-helm sentinel broken: %+v", ov.Winner.Ref)
	}
	if ov.Winner.Confidence != ConfidenceMedium {
		t.Errorf("native helm (tier5) confidence=%q want medium", ov.Winner.Confidence)
	}
}

func TestResolveOverlay_ArgoInstanceBeatsHelm(t *testing.T) {
	// Fixed precedence: argo instance (tier 4) wins over native Helm (tier 5),
	// and Helm is retained as a conflict runner-up.
	ov := ResolveOverlay(obj("ns", "x",
		map[string]string{argoInstanceLabel: "checkout"},
		map[string]string{helmReleaseNameAnno: "checkout"}), false)
	if ov == nil || ov.Winner.Tier != TierArgoInstance {
		t.Fatalf("argo-instance must beat helm: got %+v", ov)
	}
	if len(ov.Conflicts) != 1 || ov.Conflicts[0].Tier != TierHelmRelease {
		t.Errorf("helm must be retained as a conflict: %+v", ov.Conflicts)
	}
}

func TestResolveOverlay_HelmBeatsLabelTiers(t *testing.T) {
	// Helm release (tier 5) ranks above the app.kubernetes.io label tiers (7-8).
	ov := ResolveOverlay(obj("ns", "x",
		map[string]string{appNameLabel: "web", partOfLabel: "store"},
		map[string]string{helmReleaseNameAnno: "web"}), false)
	if ov == nil || ov.Winner.Tier != TierHelmRelease {
		t.Fatalf("helm must beat labels: got %+v", ov)
	}
	if len(ov.Conflicts) != 2 { // part-of (7) + name (8) retained
		t.Errorf("label tiers must be retained as conflicts: %+v", ov.Conflicts)
	}
}

func TestResolveOverlay_InstanceTierDisambiguatesInstallations(t *testing.T) {
	// app.kubernetes.io/instance is Argo's default tracking label and Helm's
	// install-identity label. It must rank above the semantic name/part-of tiers
	// (so two releases of one chart — identical app.kubernetes.io/name — split on
	// instance) but below the explicit Helm-release annotation.
	ov := ResolveOverlay(obj("ns", "x",
		map[string]string{appInstanceLabel: "grafana-prod", appNameLabel: "grafana", partOfLabel: "platform"}, nil), false)
	if ov == nil || ov.Winner.Tier != TierInstance {
		t.Fatalf("instance must win over name/part-of: got %+v", ov)
	}
	if ov.Winner.Key != "ns/app/grafana-prod" || ov.Winner.Confidence != ConfidenceMedium {
		t.Errorf("instance winner key/confidence = %q/%q, want ns/app/grafana-prod/medium", ov.Winner.Key, ov.Winner.Confidence)
	}
	if len(ov.Conflicts) != 2 { // name + part-of retained
		t.Errorf("name + part-of must be retained as conflicts: %+v", ov.Conflicts)
	}

	// The explicit Helm-release annotation still outranks the instance label.
	ov2 := ResolveOverlay(obj("ns", "x",
		map[string]string{appInstanceLabel: "grafana-prod"},
		map[string]string{helmReleaseNameAnno: "grafana-prod", helmReleaseNSAnno: "ns"}), false)
	if ov2 == nil || ov2.Winner.Tier != TierHelmRelease {
		t.Fatalf("Helm-release annotation must beat the instance label: got %+v", ov2)
	}
}

func TestResolveOverlay_RawWinsWhenNoSignal(t *testing.T) {
	if ov := ResolveOverlay(obj("ns", "x", nil, nil), true); ov != nil {
		t.Errorf("no signal must yield nil overlay (raw-always), got %+v", ov)
	}
}

func TestResolveOverlay_BareAppIsOptIn(t *testing.T) {
	o := obj("ns", "x", map[string]string{bareAppLabel: "legacy"}, nil)
	if ov := ResolveOverlay(o, false); ov != nil {
		t.Errorf("bare-app-only must be nil without allowBareApp (never silent): %+v", ov)
	}
	ov := ResolveOverlay(o, true)
	if ov == nil || ov.Winner.Tier != TierBareApp || ov.Winner.Confidence != ConfidenceLow {
		t.Errorf("bare-app with allowBareApp: got %+v", ov)
	}
}

func TestParseArgoTrackingID(t *testing.T) {
	cases := []struct {
		in       string
		ns, name string
		ok       bool
	}{
		{"guestbook:apps/Deployment:default/guestbook", "", "guestbook", true},              // default
		{"argocd_guestbook:apps/Deployment:default/guestbook", "argocd", "guestbook", true}, // namespaced
		{"legacyname", "", "", false}, // no colon
		{"", "", "", false},
	}
	for _, c := range cases {
		ns, name, ok := parseArgoTrackingID(c.in)
		if ns != c.ns || name != c.name || ok != c.ok {
			t.Errorf("parseArgoTrackingID(%q)=(%q,%q,%v) want (%q,%q,%v)", c.in, ns, name, ok, c.ns, c.name, c.ok)
		}
	}
}

// TestConfidenceForTier pins every tier→confidence band, including the boundary
// tiers (Argo-instance #4 / Helm #5 = high/medium edge, name #8 / bare-app #9 =
// medium/low edge) that the overlay spot-checks don't cover — an off-by-one in
// the range checks would silently mislabel trust.
func TestConfidenceForTier(t *testing.T) {
	cases := []struct {
		tier Tier
		want Confidence
	}{
		{TierFluxHelmRelease, ConfidenceHigh},
		{TierFluxKustomize, ConfidenceHigh},
		{TierArgoTrackingID, ConfidenceHigh},
		{TierArgoInstance, ConfidenceHigh},
		{TierHelmRelease, ConfidenceMedium},
		{TierInstance, ConfidenceMedium},
		{TierPartOf, ConfidenceMedium},
		{TierAppName, ConfidenceMedium},
		{TierBareApp, ConfidenceLow},
	}
	for _, c := range cases {
		if got := confidenceForTier(c.tier); got != c.want {
			t.Errorf("confidenceForTier(tier %d) = %q, want %q", c.tier, got, c.want)
		}
	}
}

// TestResolveSubject_CycleIsDeterministic exercises the visited-set guard: a
// corrupted ownership cycle (a↔b) must terminate AND resolve to the same
// canonical subject regardless of where the walk starts — a canonical identity
// can't depend on traversal order. The representative is the min-key member.
func TestResolveSubject_CycleIsDeterministic(t *testing.T) {
	a := Ref{Kind: "Foo", Namespace: "ns", Name: "a"}
	b := Ref{Kind: "Bar", Namespace: "ns", Name: "b"}
	owners := fakeOwners{refKey(a): b, refKey(b): a}

	fromA := ResolveSubject(a, owners, nil)
	fromB := ResolveSubject(b, owners, nil)
	if fromA.Ref == (Ref{}) || fromA.Ref != fromB.Ref {
		t.Fatalf("cycle subject must be start-independent: from a=%+v, from b=%+v", fromA.Ref, fromB.Ref)
	}
	// min(refKey) — "|Bar|ns|b" < "|Foo|ns|a", so both collapse to b.
	want := b
	if refKey(a) < refKey(b) {
		want = a
	}
	if fromA.Ref != want {
		t.Errorf("cycle representative = %+v, want min-key %+v", fromA.Ref, want)
	}
}

// TestHeuristicPodOwnerResolver_ControllerOnly pins the contract: only a
// controller==true ownerRef yields a parent. A pod with only NON-controller
// ownerRefs has no canonical owner — it must NOT be collapsed under an arbitrary
// owner (the old refs[0] fallback).
func TestHeuristicPodOwnerResolver_ControllerOnly(t *testing.T) {
	notCtrl := false
	nonController := metav1.OwnerReference{APIVersion: "example.com/v1", Kind: "Thing", Name: "t1", Controller: &notCtrl}
	pod := obj("ns", "p1", nil, nil, nonController)
	start := Ref{Kind: "Pod", Namespace: "ns", Name: "p1"}
	if got := ResolveSubject(start, HeuristicPodOwnerResolver{Pod: pod}, nil); got.Ref != start || got.Anchor != AnchorBare {
		t.Errorf("non-controller-only owner: got %+v/%s, want the pod itself (bare)", got.Ref, got.Anchor)
	}

	// A controller ref IS followed.
	podCtrl := obj("ns", "p2", nil, nil, ctrlRef("ReplicaSet", "web-7d8", "apps/v1"))
	start2 := Ref{Kind: "Pod", Namespace: "ns", Name: "p2"}
	if got := ResolveSubject(start2, HeuristicPodOwnerResolver{Pod: podCtrl}, nil); got.Ref.Kind != "Deployment" || got.Ref.Name != "web" {
		t.Errorf("controller owner: got %+v, want Deployment/web", got.Ref)
	}
}

// TestResolveSubject_StopsAtController pins the Tier-1/Tier-2 boundary: with a
// controller-ownership resolver (Pod→ReplicaSet→Deployment, Deployment having no
// further CONTROLLER), the Subject is the Deployment — even though an Argo
// Application "manages" that Deployment. Application ownership is declarative,
// not a controllerRef, so it must NEVER appear as the Subject; it belongs in
// Tier-2 AppOverlay. A topology adapter that wrapped the GitOps-inclusive
// EdgeManages walk would break this — hence the explicit OwnerResolver contract.
func TestResolveSubject_StopsAtController(t *testing.T) {
	pod := Ref{Kind: "Pod", Namespace: "ns", Name: "web-7d8-xyz"}
	rs := Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "ns", Name: "web-7d8"}
	dep := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}
	app := Ref{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "web-app"}

	// Controller chain ONLY — the Application "manages" dep but is not its
	// controllerRef, so the resolver does not return it.
	owners := fakeOwners{refKey(pod): rs, refKey(rs): dep}
	_ = app // present in the cluster as an overlay, never an owner here

	if got := ResolveSubject(pod, owners, nil); got.Ref != dep {
		t.Errorf("Pod subject = %+v, want Deployment %+v (Application must not collapse in)", got.Ref, dep)
	}
	if got := ResolveSubject(dep, owners, nil); got.Ref != dep {
		t.Errorf("Deployment subject = %+v, want itself %+v (no controller above it)", got.Ref, dep)
	}
}

// TestResolveSubject_DepthCapStops exercises maxOwnerWalkDepth: a chain longer
// than the cap must stop without hanging or panicking.
func TestResolveSubject_DepthCapStops(t *testing.T) {
	owners := fakeOwners{}
	var refs []Ref
	for i := 0; i < 18; i++ { // > maxOwnerWalkDepth (16)
		refs = append(refs, Ref{Kind: "K", Namespace: "ns", Name: "n" + string(rune('a'+i))})
	}
	for i := 0; i < len(refs)-1; i++ {
		owners[refKey(refs[i])] = refs[i+1]
	}
	if got := ResolveSubject(refs[0], owners, nil); got.Ref.Name == "" {
		t.Errorf("over-cap walk produced empty subject: %+v", got)
	}
}
