package topology

import (
	"encoding/json"
	"reflect"
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
