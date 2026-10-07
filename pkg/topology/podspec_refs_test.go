package topology

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"reflect"
	"testing"
)

func TestWorkloadReferenceExtractionMatchesTypedAndUnstructuredPodSpecs(t *testing.T) {
	backing := []corev1.Container{{Name: "main"}, {Name: "must-not-be-overwritten"}}
	spec := corev1.PodSpec{
		Containers: backing[:1], InitContainers: []corev1.Container{{Name: "init"}},
		EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "debug-config"}}}}}}},
		ImagePullSecrets:    []corev1.LocalObjectReference{{Name: "registry"}},
		Volumes: []corev1.Volume{
			{Name: "projected", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "projected-secret"}}}, {ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "projected-config"}}}}}}},
			{Name: "csi", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "example.csi", NodePublishSecretRef: &corev1.LocalObjectReference{Name: "driver-credential"}}}},
		},
	}
	typed := extractWorkloadReferences(spec)
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		t.Fatal(err)
	}
	dynamic, err := extractWorkloadReferencesFromMap(raw)
	if err != nil || !reflect.DeepEqual(typed, dynamic) || !typed.configMaps["debug-config"] || !typed.configMaps["projected-config"] || !typed.secrets["registry"] || !typed.secrets["driver-credential"] || !typed.secrets["projected-secret"] {
		t.Fatalf("typed=%+v dynamic=%+v err=%v", typed, dynamic, err)
	}
	if backing[1].Name != "must-not-be-overwritten" {
		t.Fatal("reference extraction mutated the PodSpec's container backing array")
	}
	if _, err := extractWorkloadReferencesFromMap(map[string]any{"containers": "invalid"}); err == nil {
		t.Fatal("invalid PodSpec silently accepted")
	}
}

func TestResourceGraphSeparatesAdmittedPodFromCurrentAndReplicaTemplates(t *testing.T) {
	makeSpec := func(name string) corev1.PodSpec {
		return corev1.PodSpec{Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}}}}
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: makeSpec("current")}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "team", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "app"}}}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: makeSpec("prior")}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "live", Namespace: "team", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "old"}}}, Spec: makeSpec("prior")}
	pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry"}}
	pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "debug"}}}}}}}
	provider := &mockProvider{deployments: []*appsv1.Deployment{dep}, replicaSets: []*appsv1.ReplicaSet{rs}, pods: []*corev1.Pod{pod}, configMaps: []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "team"}}, {ObjectMeta: metav1.ObjectMeta{Name: "prior", Namespace: "team"}}, {ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: "team"}}, {ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: "other"}}}, secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "team"}}}}
	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	opts.IncludeReplicaSets = true
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind, name string
		obj        runtime.Object
		want       int
	}{{"Deployment", "app", dep, 1}, {"ReplicaSet", "old", rs, 1}, {"Pod", "live", pod, 3}} {
		rel := GetRelationshipsWithObject(test.kind, "team", test.name, test.obj, topo, provider, nil, nil)
		if rel == nil || len(rel.ConfigRefs) != test.want {
			t.Fatalf("%s ConfigRefs=%+v want%d", test.kind, rel, test.want)
		}
		for _, ref := range rel.ConfigRefs {
			if ref.Namespace != "team" {
				t.Fatalf("cross-namespace dependency: %+v", ref)
			}
		}
	}
	reverse := GetRelationships("ConfigMap", "team", "prior", topo, provider, nil)
	if reverse == nil || len(reverse.Consumers) != 2 {
		t.Fatalf("prior ConfigMap consumers=%+v, want live Pod and ReplicaSet", reverse)
	}
}
