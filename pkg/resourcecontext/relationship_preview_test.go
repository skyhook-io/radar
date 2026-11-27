package resourcecontext

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestRelationshipPreviewReadableBoundaries(t *testing.T) {
	for _, count := range []int{0, 19, 20, 21} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var refs []topology.ResourceRef
			for i := range count {
				r := topology.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: fmt.Sprintf("app-%02d", i)}
				refs = append(refs, r, r)
			}
			rc := Build(t.Context(), &corev1.ConfigMap{}, Options{Relationships: &topology.Relationships{Dependents: refs}})
			if got := len(rc.Dependents); got != min(count, 20) || slices.Contains(rc.Omitted, OmittedField{"dependents", OmittedBudgetExceeded}) != (count > 20) {
				t.Fatalf("refs %d omissions %v", got, rc.Omitted)
			}
		})
	}

	refs := []topology.ResourceRef{{Group: "one", Kind: "Widget", Namespace: "shop", Name: "twin"}, {Group: "two", Kind: "Widget", Namespace: "shop", Name: "twin"}}
	for i := range 18 {
		refs = append(refs, topology.ResourceRef{Group: "two", Kind: "Widget", Namespace: "shop", Name: fmt.Sprint(i)})
	}
	restricted := Build(t.Context(), &corev1.ConfigMap{}, Options{
		Relationships: &topology.Relationships{Dependents: refs[:2]},
		AccessChecker: denyChecker{group: "one", kind: "Widget", namespace: "shop"},
	})
	if !slices.Equal(restricted.Dependents, []ContextRef{toContextRef(refs[1])}) {
		t.Fatalf("a grant crossed API groups: %+v", restricted)
	}
	clean := Build(t.Context(), &corev1.ConfigMap{}, Options{Relationships: &topology.Relationships{Dependents: refs}})
	for i := range 300 {
		refs = append(refs, topology.ResourceRef{Kind: "Secret", Namespace: "private-namespace", Name: fmt.Sprintf("hidden-%d", i)})
	}
	for _, tc := range []struct {
		decision ownership.Decision
		reason   OmittedReason
	}{{ownership.Denied, OmittedRBACDenied}, {ownership.Undecided, OmittedUnavailable}} {
		rc := Build(t.Context(), &corev1.ConfigMap{}, Options{Relationships: &topology.Relationships{Dependents: refs}, AccessChecker: secretDecider{tc.decision}})
		if len(rc.Dependents) != 20 || !slices.Equal(clean.Dependents, rc.Dependents) || !slices.Equal(rc.Omitted, []OmittedField{{"dependents", tc.reason}}) {
			t.Fatalf("collision, denial or failed check changed the readable preview: %+v", rc)
		}
		clean.Omitted = rc.Omitted
		got, err := json.Marshal(rc)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(clean)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("withheld population changed serialized evidence: %s", got)
		}
	}
}

func TestRelationshipPreviewAccessBudget(t *testing.T) {
	allowed := topology.ResourceRef{Group: "a.example", Kind: "Widget", Namespace: "public", Name: "first"}
	refs := []topology.ResourceRef{allowed}
	allowed.Name = "second"
	refs = append(refs, allowed)
	for i := range 81 {
		refs = append(refs, topology.ResourceRef{Group: fmt.Sprintf("z%02d.example", i), Kind: "Secret", Namespace: "private-namespace", Name: "hidden"})
	}
	checker := &countingRefChecker{deniedKind: "Secret"}
	rc := Build(t.Context(), &corev1.ConfigMap{}, Options{Relationships: &topology.Relationships{Dependents: refs}, AccessChecker: checker})
	if checker.calls != 80 || len(rc.Dependents) != 2 || rc.Dependents[1] != toContextRef(allowed) {
		t.Fatalf("checks=%d preview=%v", checker.calls, rc.Dependents)
	}
	for _, reason := range []OmittedReason{OmittedBudgetExceeded, OmittedRBACDenied} {
		if !slices.Contains(rc.Omitted, OmittedField{"dependents", reason}) {
			t.Fatalf("omissions %v", rc.Omitted)
		}
	}
	raw, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"hidden", "private-namespace", "Secret", "z80", "81", "total"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("serialized withheld evidence: %s", raw)
		}
	}

	unknown := Build(t.Context(), &corev1.ConfigMap{}, Options{
		Relationships: &topology.Relationships{Dependents: refs[2:]},
		AccessChecker: secretDecider{ownership.Undecided},
	})
	if len(unknown.Dependents) != 0 || !slices.Equal(unknown.Omitted, []OmittedField{
		{"dependents", OmittedUnavailable}, {"dependents", OmittedBudgetExceeded},
	}) {
		t.Fatalf("failed and untested tuples: %+v", unknown)
	}
}

func TestRelationshipPreviewFindsReadableRefAfterDeniedRefsWhenPreviewHasRoom(t *testing.T) {
	var refs []topology.ResourceRef
	for i := range 300 {
		refs = append(refs, topology.ResourceRef{Group: "rare", Kind: "Widget", Namespace: "a-hidden", Name: fmt.Sprint(i)})
	}
	refs = append(refs, topology.ResourceRef{Group: "rare", Kind: "Widget", Namespace: "z-visible", Name: "rare-example"}, topology.ResourceRef{Kind: "Pod", Namespace: "public", Name: "pod"})
	rc := Build(t.Context(), &corev1.ConfigMap{}, Options{Relationships: &topology.Relationships{Dependents: refs}, AccessChecker: denyChecker{group: "rare", kind: "Widget", namespace: "a-hidden"}})
	if len(rc.Dependents) != 2 || !slices.ContainsFunc(rc.Dependents, func(r ContextRef) bool { return r.Name == "rare-example" }) {
		t.Fatalf("rare readable evidence lost: %v", rc.Dependents)
	}
}

func TestRelationshipPreviewFieldContracts(t *testing.T) {
	var refs []topology.ResourceRef
	for i := range 22 {
		refs = append(refs, topology.ResourceRef{Group: "example.io", Kind: "Widget", Namespace: "ns", Name: fmt.Sprintf("w-%02d", i)})
	}
	rel := &topology.Relationships{
		Dependents: refs, Dependencies: refs, Services: refs, Backends: slices.Clone(refs[19:]),
		PDBs: refs, Protects: refs, HealthChecks: refs, StagedPolicies: refs,
		Monitors: refs, MonitorTargets: refs, Scalers: refs, Managers: refs, Ownership: &topology.OwnershipCoverage{},
	}
	top := refs[21]
	rel.ControllerChain = &topology.ControllerChain{Refs: []topology.ResourceRef{{Kind: "Pod"}, top}}
	for i := range 22 {
		rel.Backends = append(rel.Backends, topology.ResourceRef{Kind: "Service", Namespace: "ns", Name: fmt.Sprintf("svc-%02d", i)})
	}
	rc := Build(t.Context(), &corev1.Pod{}, Options{Relationships: rel})
	counts := map[string]int{
		"managedBy":      len(rc.ManagedBy),
		"dependencies":   len(rc.Dependencies),
		"dependents":     len(rc.Dependents),
		"exposedBy":      len(rc.ExposedBy),
		"backends":       len(rc.Backends),
		"selectedBy":     len(rc.SelectedBy),
		"protects":       len(rc.Protects),
		"healthChecks":   len(rc.HealthChecks),
		"stagedPolicies": len(rc.StagedPolicies),
		"monitors":       len(rc.Monitors),
		"monitorTargets": len(rc.MonitorTargets),
		"scaledBy":       len(rc.ScaledBy),
	}
	for field, n := range counts {
		if n != 20 || !slices.Contains(rc.Omitted, OmittedField{field, OmittedBudgetExceeded}) {
			t.Fatalf("%s len=%d omissions=%v", field, n, rc.Omitted)
		}
	}
	if !slices.Contains(rc.ManagedBy, toContextRef(top)) {
		t.Fatal("lost top controller")
	}
	union := slices.Concat(rc.ExposedBy, rc.Backends)
	sortContextRefs(union)
	union = slices.Compact(union)
	if len(union) != 39 || !slices.Equal(rc.Exposes, union) || !slices.Contains(rc.Omitted, OmittedField{"exposes", OmittedBudgetExceeded}) {
		t.Fatalf("displayed union %v vs %v", rc.Exposes, union)
	}
}

func TestRelationshipPreviewUnverifiedController(t *testing.T) {
	top := topology.ResourceRef{Kind: "Secret", Namespace: "private", Name: "unchecked"}
	chain := &topology.ControllerChain{Refs: []topology.ResourceRef{{Kind: "ConfigMap"}, top}}
	rc := Build(t.Context(), &corev1.ConfigMap{}, Options{
		Relationships: &topology.Relationships{Ownership: &topology.OwnershipCoverage{}, ControllerChain: chain},
		AccessChecker: secretDecider{ownership.Undecided},
	})
	if len(rc.ManagedBy) != 0 || !slices.Equal(rc.Omitted, []OmittedField{{"managedBy", OmittedUnavailable}}) {
		t.Fatalf("an unverified ancestor must leave an unavailable controller walk: %+v", rc)
	}
}

func TestRelationshipPreviewUnderCapKeepsFirstOccurrenceOrder(t *testing.T) {
	cj := &batchv1.CronJob{}
	for _, name := range strings.Fields("m c j a h f k b m e l a d g i") {
		cj.Status.Active = append(cj.Status.Active, corev1.ObjectReference{APIVersion: "batch/v1", Kind: "Job", Name: name})
	}
	rc := Build(t.Context(), cj, Options{})
	var got []string
	for _, job := range rc.CronJobSummary.ActiveJobs {
		got = append(got, job.Name)
	}
	if want := strings.Fields("m c j a h f k b e l d g i"); !slices.Equal(got, want) {
		t.Fatalf("active job order = %v, want %v", got, want)
	}
}

func TestRelationshipPreviewSummaryLists(t *testing.T) {
	cj := &batchv1.CronJob{}
	ing := &networkingv1.Ingress{}
	for i := range 21 {
		name := fmt.Sprintf("item-%02d", 20-i)
		cj.Status.Active = append(cj.Status.Active, corev1.ObjectReference{Kind: "Job", APIVersion: "batch/v1", Name: name})
		path := networkingv1.HTTPIngressPath{Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: name}}}
		rule := networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{path}}}
		ing.Spec.Rules = append(ing.Spec.Rules, networkingv1.IngressRule{IngressRuleValue: rule})
	}
	jobs := Build(t.Context(), cj, Options{})
	services := Build(t.Context(), ing, Options{})
	if len(jobs.CronJobSummary.ActiveJobs) != 20 || !slices.Contains(jobs.Omitted, OmittedField{"cronJobSummary.activeJobs", OmittedBudgetExceeded}) {
		t.Fatalf("active Jobs: %+v", jobs)
	}
	if len(services.IngressSummary.BackendServices) != 20 || !slices.Contains(services.Omitted, OmittedField{"ingressSummary.backendServices", OmittedBudgetExceeded}) {
		t.Fatalf("Ingress backends: %+v", services)
	}
	cj.Status.Active = cj.Status.Active[:20]
	jobs = Build(t.Context(), cj, Options{})
	for i, job := range jobs.CronJobSummary.ActiveJobs {
		if job.Name != cj.Status.Active[i].Name {
			t.Fatalf("active job order changed: %v", jobs.CronJobSummary.ActiveJobs)
		}
	}
}

func TestResourceContextServicePodsAndNamespaceMembership(t *testing.T) {
	svc := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}}}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: svc.Spec.Selector}}},
	}
	p := mockResourceProvider{deploys: []*appsv1.Deployment{deployment}}
	for i := range 1000 {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: fmt.Sprintf("member-%04d", i)}}
		if i < 200 {
			pod.Labels = svc.Spec.Selector
		}
		if i < 100 {
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		p.pods = append(p.pods, pod)
	}
	build := func(r resourceid.Ref, obj runtime.Object) *ResourceContext {
		session := newContextFixtureSession(r, obj, p)
		access := ownership.Unrestricted()
		rel, extras := topology.RelationsContextAnswer(session, access, access, ownership.NewView(session, access), r, obj, p, nil)
		return Build(t.Context(), obj, Options{Relationships: rel, RelationshipExtras: extras, ServiceBackends: mockServiceBackends(p.pods)})
	}
	rc := build(resourceid.NewRef("", "Service", "shop", "api"), svc)
	pods := rc.ServiceSummary.SelectedPods
	if len(rc.Backends) != 1 || rc.Backends[0].Kind != "Deployment" || pods.Total != 200 || pods.Ready != 100 || pods.NotReady != 100 || len(pods.ReadyPods) != 10 || len(pods.NotReadyPods) != 10 {
		t.Fatalf("service backends %v, pod summary %+v", rc.Backends, pods)
	}
	namespace := &corev1.Namespace{TypeMeta: metav1.TypeMeta{Kind: "Namespace", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{Name: "shop"}}
	rc = build(resourceid.NewRef("", "Namespace", "", "shop"), namespace)
	raw, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Children != nil || rc.ReferencedBy != nil || len(rc.Dependents)+len(rc.Backends) != 0 || strings.Contains(string(raw), "member-") {
		t.Fatalf("namespace invented containment: %s", raw)
	}
}

func TestRelationshipPreviewExpansion(t *testing.T) {
	widgets := func(n int) []topology.ResourceRef {
		var refs []topology.ResourceRef
		for i := range n {
			refs = append(refs, topology.ResourceRef{Group: "example.io", Kind: "Widget", Namespace: "ns", Name: fmt.Sprintf("w-%03d", i)})
		}
		return refs
	}
	for _, tc := range []struct {
		count   int
		clipped bool
	}{{199, false}, {200, false}, {250, true}} {
		t.Run(fmt.Sprint(tc.count), func(t *testing.T) {
			refs := widgets(tc.count)
			rc := Build(t.Context(), &corev1.ConfigMap{}, Options{
				Relationships:      &topology.Relationships{Dependents: refs, Dependencies: refs},
				ExpandRelationship: "dependents",
			})
			if len(rc.Dependents) != min(tc.count, MaxExpandedRelationshipItems) ||
				slices.Contains(rc.Omitted, OmittedField{"dependents", OmittedBudgetExceeded}) != tc.clipped {
				t.Fatalf("expanded dependents %d omissions %v", len(rc.Dependents), rc.Omitted)
			}
			if len(rc.Dependencies) != 20 || !slices.Contains(rc.Omitted, OmittedField{"dependencies", OmittedBudgetExceeded}) {
				t.Fatalf("a field not asked for was expanded: %d", len(rc.Dependencies))
			}
		})
	}

	routes := widgets(250)
	var services []topology.ResourceRef
	for i := range 250 {
		services = append(services, topology.ResourceRef{Kind: "Service", Namespace: "ns", Name: fmt.Sprintf("svc-%03d", i)})
	}
	rc := Build(t.Context(), &corev1.Pod{}, Options{
		Relationships:      &topology.Relationships{Services: routes, Backends: services},
		ExpandRelationship: "exposedBy",
	})
	if len(rc.ExposedBy) != 200 || len(rc.Backends) != 20 || len(rc.Exposes) != 220 {
		t.Fatalf("exposedBy expanded to exposedBy=%d backends=%d union=%d", len(rc.ExposedBy), len(rc.Backends), len(rc.Exposes))
	}

	managers := widgets(250)
	top := managers[249]
	owned := Build(t.Context(), &corev1.ConfigMap{}, Options{
		Relationships: &topology.Relationships{Managers: managers, Ownership: &topology.OwnershipCoverage{},
			ControllerChain: &topology.ControllerChain{Refs: []topology.ResourceRef{{Kind: "ConfigMap"}, top}}},
		ExpandRelationship: "managedBy",
	})
	if len(owned.ManagedBy) != 200 || !slices.Contains(owned.ManagedBy, toContextRef(top)) {
		t.Fatalf("expanded managedBy %d, top kept %v", len(owned.ManagedBy), slices.Contains(owned.ManagedBy, toContextRef(top)))
	}

	var denied []topology.ResourceRef
	for i := range 900 {
		denied = append(denied, topology.ResourceRef{Group: fmt.Sprintf("z%03d.example", i), Kind: "Secret", Namespace: "private-namespace", Name: "hidden"})
	}
	checker := &countingRefChecker{deniedKind: "Secret"}
	hidden := Build(t.Context(), &corev1.ConfigMap{}, Options{Relationships: &topology.Relationships{Dependents: denied}, AccessChecker: checker, ExpandRelationship: "dependents"})
	if checker.calls != accessChecksPerPreviewItem*MaxExpandedRelationshipItems || len(hidden.Dependents) != 0 ||
		!slices.Equal(hidden.Omitted, []OmittedField{{"dependents", OmittedRBACDenied}, {"dependents", OmittedBudgetExceeded}}) {
		t.Fatalf("expanded access checks = %d, %+v", checker.calls, hidden)
	}
	raw, err := json.Marshal(hidden)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"hidden", "private-namespace", "Secret", "900", "800"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("serialized withheld evidence: %s", raw)
		}
	}
}
