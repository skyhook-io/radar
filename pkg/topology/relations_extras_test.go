package topology

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestRelationsExtrasConsumerPathsAndNoWireData(t *testing.T) {
	secret := projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "shop"}}, "", "Secret")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "shop"}, Spec: corev1.PodSpec{ImagePullSecrets: []corev1.LocalObjectReference{{Name: "shared"}}}}, "", "Pod")
	sa := projectionObject(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "account", Namespace: "shop"}, Secrets: []corev1.ObjectReference{{Name: "shared"}}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "shared"}}}, "", "ServiceAccount")
	ingress := projectionObject(&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "ingress", Namespace: "shop"}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "shared"}}}}, "networking.k8s.io", "Ingress")
	s := newProjectionSession(t, secret, pod, sa, ingress)
	access := ownership.Unrestricted()
	_, extras := RelationsContextAnswer(s, access, access, ownership.NewView(s, access), projectionRef(secret), secret, "", nil, nil)
	paths := map[string][]string{}
	for _, consumer := range extras.Consumers {
		paths[consumer.Ref.Kind] = consumer.Paths
	}
	want := map[string][]string{"Pod": {"imagePullSecrets[].name"}, "ServiceAccount": {"secrets[]", "imagePullSecrets[]"}, "Ingress": {"spec.tls[].secretName"}}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("consumer paths=%v, want %v", paths, want)
	}
	raw, _ := json.Marshal(extras)
	if string(raw) != "{}" {
		t.Fatalf("in-process evidence serialized: %s", raw)
	}
}

func TestRelationsExtrasScalerAttributionUsesEmit(t *testing.T) {
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}}, "apps", "Deployment")
	hpa := projectionObject(&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Name: "hpa", Namespace: "shop", Labels: map[string]string{"scaledobject.keda.sh/name": "autoscale"}}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}}}, "autoscaling", "HorizontalPodAutoscaler")
	s := newProjectionSession(t, dep, hpa)
	for _, deny := range []bool{false, true} {
		access := projectionAccess{}
		if deny {
			access.denied = []string{"ScaledObject"}
		}
		_, extras := RelationsContextAnswer(s, access, ownership.Unrestricted(), ownership.NewView(s, access), projectionRef(dep), dep, "", nil, nil)
		manager, ok := extras.ScalerManagers[projectionRef(hpa)]
		if deny && (ok || !extras.Fields["ScalerManagers"].Narrowed) || !deny && (!ok || manager.Name != "autoscale") {
			t.Fatalf("deny=%v: %+v", deny, extras)
		}
	}
}

type inventoryGapSession struct {
	*projectionSession
	kind   resourceid.GroupKind
	reason readreason.Code
}

func (s inventoryGapSession) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	if gk == s.kind {
		return relations.IndexedRead{Gaps: []ownership.Gap{{Kind: gk, Reason: s.reason}}}
	}
	return s.projectionSession.Indexed(gk, index, keys, scope)
}

func TestRelationsExtrasEmptyInventoriesKeepReadinessAndNarrowing(t *testing.T) {
	node := projectionObject(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}, "", "Node")
	secret := projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: "shop"}}, "", "Secret")
	for _, obj := range []runtime.Object{node, secret} {
		s := inventoryGapSession{newProjectionSession(t, obj), resourceid.GroupKind{Kind: "Pod"}, readreason.KindSyncPending}
		access := ownership.Unrestricted()
		_, extras := RelationsContextAnswer(s, access, access, ownership.NewView(s, access), projectionRef(obj), obj, "", nil, nil)
		if len(extras.Fields["Consumers"].NotChecked) == 0 {
			t.Fatalf("empty cold source lost for %s: %+v", projectionRef(obj).Kind, extras)
		}
		denied := projectionAccess{denied: []string{"Pod"}}
		_, extras = RelationsContextAnswer(s, denied, access, ownership.NewView(s, denied), projectionRef(obj), obj, "", nil, nil)
		if !extras.Fields["Consumers"].Narrowed || len(extras.Consumers) > 0 {
			t.Fatalf("empty denied source lost: %+v", extras)
		}
	}
}

// countingProjectionAccess counts what the caller's access is asked, by kind
// and namespace.
type countingProjectionAccess struct {
	projectionAccess
	asked map[resourceid.Ref]int
}

func (a countingProjectionAccess) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	a.asked[resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: ns}]++
	return a.projectionAccess.CanRead(gk, ns)
}

// The context checks a kind only for objects that relate, so it says a field
// was withheld only where a related object is hidden.
func TestRelationsContextWithholdsOnlyWhatIsThere(t *testing.T) {
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "Service")
	inShop := resourceid.Ref{Kind: "Service", Namespace: "shop"}

	s := newProjectionSession(t, dep)
	empty := countingProjectionAccess{projectionAccess{denied: []string{"Service"}}, map[resourceid.Ref]int{}}
	RelationsContextAnswer(s, empty, ownership.Unrestricted(), ownership.NewView(s, empty), projectionRef(dep), dep, "", nil, nil)
	if empty.asked[inShop] != 0 {
		t.Fatalf("no Service in shop should cost no check: %v", empty.asked)
	}

	s = newProjectionSession(t, dep, svc)
	there := countingProjectionAccess{projectionAccess{denied: []string{"Service"}}, map[resourceid.Ref]int{}}
	rel, extras := RelationsContextAnswer(s, there, ownership.Unrestricted(), ownership.NewView(s, there), projectionRef(dep), dep, "", nil, nil)
	if there.asked[inShop] == 0 || len(rel.Services) != 0 {
		t.Fatalf("a Service the caller may not read must be checked and withheld: asked=%v services=%v", there.asked, rel.Services)
	}
	narrowed := false
	for _, c := range extras.Fields {
		narrowed = narrowed || c.Narrowed
	}
	if !narrowed {
		t.Fatalf("the withheld Service must be reported: %+v", extras.Fields)
	}
}

// One withheld Secret narrows the field that lists it, not the family's other
// fields, and a target whose permission check failed is not passed on.
func TestRelationsContextWithheldTargetNarrowsItsOwnField(t *testing.T) {
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "shop"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "creds", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "credential"}}}}}}, "", "Pod")
	s := newProjectionSession(t, pod)
	denied := projectionAccess{denied: []string{"Secret"}}
	_, extras := RelationsContextAnswer(s, denied, ownership.Unrestricted(), ownership.NewView(s, denied), projectionRef(pod), pod, "", nil, nil)
	if !extras.Fields["ConfigRefs"].Narrowed || extras.Fields["StorageRefs"].Narrowed || extras.Fields["ServiceAccount"].Narrowed {
		t.Fatalf("fields = %+v, want only ConfigRefs narrowed", extras.Fields)
	}
	if got := extras.WithheldKinds["ConfigRefs"]; len(got) != 1 || got[0].Kind != "Secret" {
		t.Fatalf("withheld kinds = %v", extras.WithheldKinds)
	}

	undecided := undecidedKinds{"Secret"}
	_, extras = RelationsContextAnswer(s, undecided, ownership.Unrestricted(), ownership.NewView(s, undecided), projectionRef(pod), pod, "", nil, nil)
	for _, d := range extras.Declarations {
		if d.Ref.Kind == "Secret" {
			t.Fatalf("a Secret whose check failed was passed on: %+v", d)
		}
	}
}

// undecidedKinds can't decide the kinds it names.
type undecidedKinds []string

func (u undecidedKinds) CanRead(gk resourceid.GroupKind, _ string) ownership.Decision {
	if slices.Contains(u, gk.Kind) {
		return ownership.Undecided
	}
	return ownership.Allowed
}
func (undecidedKinds) Namespaces() ([]string, bool) { return nil, true }
