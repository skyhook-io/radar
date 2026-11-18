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
	storagev1 "k8s.io/api/storage/v1"
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
	_, extras := RelationsContextAnswer(s, access, access, ownership.NewView(s, access), projectionRef(secret), secret, nil, nil)
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
	undecided := undecidedKinds{"ScaledObject"}
	_, extras := RelationsContextAnswer(s, undecided, ownership.Unrestricted(), ownership.NewView(s, undecided), projectionRef(dep), dep, nil, nil)
	if gaps := extras.Fields["ScalerManagers"].NotChecked; len(gaps) != 1 || gaps[0].Reason != readreason.UserPermissionCheckFailed {
		t.Fatalf("an unchecked scaler manager's gap = %+v", gaps)
	}
	for _, deny := range []bool{false, true} {
		access := projectionAccess{}
		if deny {
			access.denied = []string{"ScaledObject"}
		}
		_, extras := RelationsContextAnswer(s, access, ownership.Unrestricted(), ownership.NewView(s, access), projectionRef(dep), dep, nil, nil)
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
		_, extras := RelationsContextAnswer(s, access, access, ownership.NewView(s, access), projectionRef(obj), obj, nil, nil)
		if len(extras.Fields["ReferencedBy"].NotChecked) == 0 {
			t.Fatalf("empty cold source lost for %s: %+v", projectionRef(obj).Kind, extras)
		}
		denied := projectionAccess{denied: []string{"Pod"}}
		_, extras = RelationsContextAnswer(s, denied, access, ownership.NewView(s, denied), projectionRef(obj), obj, nil, nil)
		if !extras.Fields["ReferencedBy"].Narrowed || len(extras.Consumers) > 0 {
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
	RelationsContextAnswer(s, empty, ownership.Unrestricted(), ownership.NewView(s, empty), projectionRef(dep), dep, nil, nil)
	if empty.asked[inShop] != 0 {
		t.Fatalf("no Service in shop should cost no check: %v", empty.asked)
	}

	s = newProjectionSession(t, dep, svc)
	there := countingProjectionAccess{projectionAccess{denied: []string{"Service"}}, map[resourceid.Ref]int{}}
	rel, extras := RelationsContextAnswer(s, there, ownership.Unrestricted(), ownership.NewView(s, there), projectionRef(dep), dep, nil, nil)
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

// A Secret the subject's own spec names is passed on by name, marked, without
// narrowing any field, whether the caller is denied it or the check failed.
func TestRelationsContextDeclaredUnreadableTargetIsNamed(t *testing.T) {
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "shop"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "creds", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "credential"}}}}}}, "", "Pod")
	s := newProjectionSession(t, pod)
	denied := projectionAccess{denied: []string{"Secret"}}
	_, extras := RelationsContextAnswer(s, denied, ownership.Unrestricted(), ownership.NewView(s, denied), projectionRef(pod), pod, nil, nil)
	for field, c := range extras.Fields {
		if c.Narrowed {
			t.Fatalf("field %q narrowed although its Secret is named: %+v", field, extras.Fields)
		}
	}
	if !slices.ContainsFunc(extras.Declarations, func(d relations.Declared) bool {
		return d.Ref.Kind == "Secret" && DeclaredState(d) == ResourceStateDenied
	}) {
		t.Fatalf("the denied Secret wasn't passed on: %+v", extras.Declarations)
	}

	undecided := undecidedKinds{"Secret"}
	_, extras = RelationsContextAnswer(s, undecided, ownership.Unrestricted(), ownership.NewView(s, undecided), projectionRef(pod), pod, nil, nil)
	if !slices.ContainsFunc(extras.Declarations, func(d relations.Declared) bool {
		return d.Ref.Kind == "Secret" && DeclaredState(d) == ResourceStateUnverified
	}) {
		t.Fatalf("the Secret whose check failed wasn't passed on as unverified: %+v", extras.Declarations)
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

// countingIndexedSession counts inventory reads by kind.
type countingIndexedSession struct {
	*projectionSession
	reads map[string]int
}

func (s countingIndexedSession) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	s.reads[gk.Kind]++
	return s.projectionSession.Indexed(gk, index, keys, scope)
}

// The drawer reads a Node's Pods and a StorageClass's volumes once, for the
// relationship projection; only the AI context also reads the full inventories
// it reports coverage for.
func TestDrawerAnswerSkipsContextOnlyInventories(t *testing.T) {
	node := projectionObject(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}}, "", "Node")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "shop"}, Spec: corev1.PodSpec{NodeName: "node"}}, "", "Pod")
	class := projectionObject(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}}, "storage.k8s.io", "StorageClass")
	pv := projectionObject(&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "fast"}}, "", "PersistentVolume")
	access := ownership.Unrestricted()
	for _, tc := range []struct {
		subject runtime.Object
		related runtime.Object
		kind    string
	}{{node, pod, "Pod"}, {class, pv, "PersistentVolume"}} {
		reads := func(answer func(relations.Session, ownership.Access, ownership.Access, *ownership.View, resourceid.Ref, runtime.Object, ResourceProvider, DynamicProvider) (*Relationships, *RelationshipExtras)) int {
			s := countingIndexedSession{newProjectionSession(t, tc.subject, tc.related), map[string]int{}}
			answer(s, access, access, ownership.NewView(s, access), projectionRef(tc.subject), tc.subject, nil, nil)
			return s.reads[tc.kind]
		}
		if drawer, context := reads(RelationsAnswerWithExtras), reads(RelationsContextAnswer); drawer >= context {
			t.Fatalf("%s: drawer read %s %d times, context %d", projectionRef(tc.subject).Kind, tc.kind, drawer, context)
		}
	}
}

// A volume whose class the caller may not read narrows its dependencies only:
// its readable claim is still listed among its dependents in full.
func TestVolumeStorageCoverageMarksOnlyTheWithheldTarget(t *testing.T) {
	pv := projectionObject(&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: "fast", ClaimRef: &corev1.ObjectReference{Namespace: "shop", Name: "data"}}}, "", "PersistentVolume")
	pvc := projectionObject(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "shop"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "disk"}}, "", "PersistentVolumeClaim")
	class := projectionObject(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}}, "storage.k8s.io", "StorageClass")
	s := newProjectionSession(t, pv, pvc, class)
	denied := projectionAccess{denied: []string{"StorageClass"}}
	rel, extras := RelationsContextAnswer(s, denied, ownership.Unrestricted(), ownership.NewView(s, denied), projectionRef(pv), pv, nil, nil)
	if !extras.Fields["Dependencies"].Narrowed || extras.Fields["Dependents"].Narrowed {
		t.Fatalf("fields = %+v", extras.Fields)
	}
	if !slices.ContainsFunc(rel.Consumers, func(r ResourceRef) bool { return r.Kind == "PersistentVolumeClaim" && r.Name == "data" }) {
		t.Fatalf("readable claim not listed: %+v", rel)
	}
}

// The family answers are shared with walks that collect no extras, such as
// the neighborhood's, so answering without them must not fail.
func TestFamilyAnswersWithoutExtras(t *testing.T) {
	cm := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "shop"}}, "", "ConfigMap")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "shop"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "c", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "config"}}}}}}}, "", "Pod")
	s := newProjectionSession(t, cm, pod)
	access := ownership.Unrestricted()
	n := &neighborhood{nodes: map[resourceid.Ref]bool{}, edges: map[string]bool{}, coverage: map[string]relations.Coverage{}, access: access}
	for _, subject := range []runtime.Object{pod, cm} {
		n.answer(s, relations.NewView(s, access), relations.Families(""), relations.SelectorFamilies(), projectionRef(subject), subject, false)
	}
	if len(n.edges) == 0 {
		t.Fatal("no edge between the Pod and the ConfigMap it mounts")
	}
}

// What a subject declares is listed whether or not Radar holds the targets,
// so only a failure reading the subject's own spec leaves the declarations
// incomplete: not a target kind Radar doesn't collect or is still loading,
// nor a SealedSecret the caller may not read. The drawer's fields still carry
// what wasn't read about the targets.
func TestRelationsContextUsesCoverageIsTheSubjectsOwn(t *testing.T) {
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		ServiceAccountName: "runner",
		ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "registry"}},
		Containers:         []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}}},
		Volumes:            []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}},
	}}}}, "apps", "Deployment")
	sealed := projectionCR("bitnami.com", "SealedSecret", "shop", "registry", map[string]any{})
	secret := resourceid.GroupKind{Kind: "Secret"}
	for _, tc := range []struct {
		name   string
		reason readreason.Code
		denied []string
	}{
		{"secrets not collected", readreason.RadarKindNotCollected, nil},
		{"secrets loading", readreason.KindSyncPending, nil},
		{"sealed secret withheld", "", []string{"SealedSecret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newProjectionSession(t, dep, sealed)
			if tc.reason != "" {
				s.gaps = map[resourceid.GroupKind]ownership.Gap{secret: {Kind: secret, Namespace: "shop", Reason: tc.reason}}
			}
			access := projectionAccess{denied: tc.denied}
			_, extras := RelationsContextAnswer(s, access, ownership.Unrestricted(), ownership.NewView(s, access), projectionRef(dep), dep, nil, nil)
			if c, ok := extras.Fields["Uses"]; ok {
				t.Fatalf("declarations marked incomplete: %+v", c)
			}
			declared := map[string]string{}
			for _, d := range extras.Declarations {
				declared[d.Ref.Kind] = d.Ref.Name
			}
			if want := map[string]string{"ConfigMap": "settings", "Secret": "registry", "PersistentVolumeClaim": "data", "ServiceAccount": "runner"}; !reflect.DeepEqual(declared, want) {
				t.Fatalf("declarations = %v, want %v", declared, want)
			}
			if refs := extras.Fields["ConfigRefs"]; tc.reason != "" && len(refs.NotChecked) == 0 || tc.denied != nil && !refs.Narrowed {
				t.Fatalf("the drawer's field lost what wasn't read: %+v", extras.Fields)
			}
		})
	}

	rollout := projectionCR("argoproj.io", "Rollout", "shop", "web", map[string]any{"spec": map[string]any{"template": map[string]any{"spec": "not a pod spec"}}})
	s := newProjectionSession(t, rollout)
	access := ownership.Unrestricted()
	_, extras := RelationsContextAnswer(s, access, access, ownership.NewView(s, access), projectionRef(rollout), rollout, nil, nil)
	if c := extras.Fields["Uses"]; len(c.NotChecked) != 1 || c.NotChecked[0].Kind != projectionRef(rollout).GroupKind() || c.NotChecked[0].Reason != readreason.ReadError {
		t.Fatalf("a spec that couldn't be read left the declarations complete: %+v", extras.Fields)
	}
}

// A context's reverse references are the configuration references that name
// the subject, so other families that list the subject's consumers in the
// drawer, such as Contour's, don't mark them.
func TestRelationsContextReferencedByCoverageIsItsOwnEvidence(t *testing.T) {
	secret := projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "shop"}}, "", "Secret")
	proxy := resourceid.GroupKind{Group: "projectcontour.io", Kind: "HTTPProxy"}
	pod := resourceid.GroupKind{Kind: "Pod"}
	for _, kind := range []resourceid.GroupKind{proxy, pod} {
		s := newProjectionSession(t, secret)
		s.gaps = map[resourceid.GroupKind]ownership.Gap{kind: {Kind: kind, Namespace: "shop", Reason: readreason.KindSyncPending}}
		access := ownership.Unrestricted()
		_, extras := RelationsContextAnswer(s, access, access, ownership.NewView(s, access), projectionRef(secret), secret, nil, nil)
		if len(extras.Fields["Consumers"].NotChecked) == 0 {
			t.Fatalf("%s: the drawer's field lost the gap: %+v", kind.Kind, extras.Fields)
		}
		if got := len(extras.Fields["ReferencedBy"].NotChecked) > 0; got != (kind == pod) {
			t.Fatalf("%s: referencedBy gap = %v: %+v", kind.Kind, got, extras.Fields)
		}
	}
}

// A Service no route reaches still says when the routes Radar searched were
// incomplete: a route store still loading in a namespace the caller can't see
// leaves its entrypoints unknown, not only withheld.
func TestRelationsContextEntrypointsKeepTheRouteGapWithoutRoutes(t *testing.T) {
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}}, "", "Service")
	route := resourceid.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}
	gap := ownership.Gap{Kind: route, Namespace: "other", Reason: readreason.KindSyncPending}
	s := newProjectionSession(t, svc)
	s.gaps = map[resourceid.GroupKind]ownership.Gap{route: gap}
	emit := projectionAccess{namespaces: []string{"shop"}}
	rel, extras := RelationsContextAnswer(s, emit, ownership.Unrestricted(), ownership.NewView(s, emit), projectionRef(svc), svc, nil, nil)
	if len(rel.Routes)+len(rel.Gateways) != 0 {
		t.Fatalf("no route reaches the Service: %+v", rel)
	}
	if !slices.Contains(extras.Fields["Entrypoints"].NotChecked, gap) {
		t.Fatalf("entrypoints lost the route store's gap: %+v", extras.Fields)
	}
}

// A retired revision's context declarations come from its own template, while
// Related Resources still draws none for it, as the Topology screen does.
func TestRelationsRetiredRevisionDeclaresOnlyForItsContext(t *testing.T) {
	zero := int32(0)
	rs := projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop"}, Spec: appsv1.ReplicaSetSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}},
	}}}}, "apps", "ReplicaSet")
	secret := projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "shop"}}, "", "Secret")
	s := newProjectionSession(t, rs, secret)
	access := ownership.Unrestricted()
	rel, extras := RelationsContextAnswer(s, access, access, ownership.NewView(s, access), projectionRef(rs), rs, nil, nil)
	if len(extras.Declarations) != 1 || extras.Declarations[0].Ref.Name != "registry" || extras.Declarations[0].State != relations.TargetPresent {
		t.Fatalf("declarations = %+v", extras.Declarations)
	}
	if len(rel.ConfigRefs) != 0 {
		t.Fatalf("Related Resources drew a retired revision's Secret: %+v", rel.ConfigRefs)
	}
}
