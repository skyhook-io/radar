package topology

import (
	"encoding/json"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
)

func declaringDeployment() runtime.Object {
	return projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		ServiceAccountName: "web-sa",
		Containers:         []corev1.Container{{Name: "web", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"}}}}}},
		Volumes: []corev1.Volume{
			{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "web-tls"}}},
			{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "web-data"}}},
		},
	}}}}, "apps", "Deployment")
}

func declaredAnswer(t *testing.T, access ownership.Access, withSecret bool) (*Relationships, *RelationshipExtras, *projectionSession) {
	t.Helper()
	objects := []runtime.Object{
		declaringDeployment(),
		projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-config"}}, "", "ConfigMap"),
		projectionObject(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-sa"}}, "", "ServiceAccount"),
		projectionObject(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-data"}}, "", "PersistentVolumeClaim"),
	}
	if withSecret {
		objects = append(objects, projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-tls"}}, "", "Secret"))
	}
	s := newProjectionSession(t, objects...)
	subject := objects[0]
	captured, ok := relations.CaptureSubject(s, projectionRef(subject), subject)
	if !ok {
		t.Fatal("capture failed")
	}
	rel, extras := RelationsAnswerWithExtras(captured, access, access, ownership.NewView(captured, access), projectionRef(subject), subject, nil, nil)
	return rel, extras, s
}

// A Secret the subject's own spec names is listed by name, marked denied,
// and the answer doesn't depend on whether it exists.
func TestDeclaredReferencesNameWhatTheCallerMayNotRead(t *testing.T) {
	access := projectionAccess{denied: []string{"Secret"}}
	rel, extras, s := declaredAnswer(t, access, true)
	want := ResourceRef{Kind: "Secret", Namespace: "shop", Name: "web-tls", Access: ResourceAccessDenied}
	if !slices.Contains(rel.ConfigRefs, want) {
		t.Fatalf("configRefs = %+v, want %+v listed", rel.ConfigRefs, want)
	}
	if !slices.Contains(rel.ConfigRefs, ResourceRef{Kind: "ConfigMap", Namespace: "shop", Name: "web-config"}) {
		t.Fatalf("readable ConfigMap missing or marked: %+v", rel.ConfigRefs)
	}
	for _, r := range s.reads {
		if r.Kind == "Secret" {
			t.Fatalf("the unreadable Secret was read: %v", s.reads)
		}
	}
	for field, c := range extras.Fields {
		if c.Narrowed {
			t.Fatalf("field %q marked withheld although the Secret is named", field)
		}
	}
	absent, _, _ := declaredAnswer(t, access, false)
	a, _ := json.Marshal(rel)
	b, _ := json.Marshal(absent)
	if string(a) != string(b) {
		t.Fatalf("the answer depends on whether the unreadable Secret exists:\n%s\n%s", a, b)
	}
}

// Every kind a pod template declares is named in its own field, and a failed
// permission check reads as unverified rather than as a gap.
func TestDeclaredReferencesMarkEachKindAndAFailedCheck(t *testing.T) {
	rel, _, _ := declaredAnswer(t, projectionAccess{denied: []string{"ServiceAccount", "PersistentVolumeClaim"}}, true)
	if rel.ServiceAccount == nil || *rel.ServiceAccount != (ResourceRef{Kind: "ServiceAccount", Namespace: "shop", Name: "web-sa", Access: ResourceAccessDenied}) {
		t.Fatalf("serviceAccount = %+v", rel.ServiceAccount)
	}
	if !slices.Contains(rel.StorageRefs, ResourceRef{Kind: "PersistentVolumeClaim", Namespace: "shop", Name: "web-data", Access: ResourceAccessDenied}) {
		t.Fatalf("storageRefs = %+v", rel.StorageRefs)
	}
	rel, extras, _ := declaredAnswer(t, undecidedKinds{"Secret"}, true)
	if !slices.Contains(rel.ConfigRefs, ResourceRef{Kind: "Secret", Namespace: "shop", Name: "web-tls", Access: ResourceAccessUnverified}) {
		t.Fatalf("configRefs = %+v, want the Secret unverified", rel.ConfigRefs)
	}
	for field, c := range extras.Fields {
		for _, gap := range c.NotChecked {
			if gap.Kind.Kind == "Secret" {
				t.Fatalf("field %q still reports a gap for the named Secret: %+v", field, c)
			}
		}
	}
}
