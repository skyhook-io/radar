package configrefs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestPodSpecReferences(t *testing.T) {
	var spec corev1.PodSpec
	if err := json.Unmarshal([]byte(`{
		"serviceAccountName":"runner",
		"imagePullSecrets":[{"name":"registry"}],
		"containers":[{"name":"app","envFrom":[{"configMapRef":{"name":"config","optional":true}}],"env":[{"name":"TOKEN","valueFrom":{"secretKeyRef":{"name":"token","key":"value"}}},{"name":"PASSWORD","value":"do-not-emit"}]}],
		"initContainers":[{"name":"init","envFrom":[{"secretRef":{"name":"token"}}]}],
		"ephemeralContainers":[{"name":"debug","env":[{"name":"DEBUG","valueFrom":{"configMapKeyRef":{"name":"debug","key":"setting"}}}]}],
		"volumes":[
			{"name":"projected","projected":{"sources":[{"configMap":{"name":"config","optional":true}},{"secret":{"name":"token"}},{"serviceAccountToken":{"path":"token"}}]}},
			{"name":"direct-config","configMap":{"name":"config"}},
			{"name":"direct-secret","secret":{"secretName":"token"}},
			{"name":"data","persistentVolumeClaim":{"claimName":"data"}},
			{"name":"csi","csi":{"driver":"example.com","nodePublishSecretRef":{"name":"driver"}}}
		]
	}`), &spec); err != nil {
		t.Fatal(err)
	}
	before := spec.DeepCopy()
	got := PodSpecReferences("team", spec)
	want := []PodSpecReference{
		{Ref{"ConfigMap", "team", "config"}, "containers[].envFrom[].configMapRef.name", true},
		{Ref{"ConfigMap", "team", "config"}, "volumes[].configMap.name", false},
		{Ref{"ConfigMap", "team", "config"}, "volumes[].projected.sources[].configMap.name", true},
		{Ref{"ConfigMap", "team", "debug"}, "ephemeralContainers[].env[].valueFrom.configMapKeyRef.name", false},
		{Ref{"PersistentVolumeClaim", "team", "data"}, "volumes[].persistentVolumeClaim.claimName", false},
		{Ref{"Secret", "team", "driver"}, "volumes[].csi.nodePublishSecretRef.name", false},
		{Ref{"Secret", "team", "registry"}, "imagePullSecrets[].name", false},
		{Ref{"Secret", "team", "token"}, "containers[].env[].valueFrom.secretKeyRef.name", false},
		{Ref{"Secret", "team", "token"}, "initContainers[].envFrom[].secretRef.name", false},
		{Ref{"Secret", "team", "token"}, "volumes[].projected.sources[].secret.name", false},
		{Ref{"Secret", "team", "token"}, "volumes[].secret.secretName", false},
		{Ref{"ServiceAccount", "team", "runner"}, "serviceAccountName", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("references = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(&spec, before) {
		t.Fatal("collector mutated PodSpec")
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "do-not-emit") {
		t.Fatal("collector emitted an environment value")
	}
	if !reflect.DeepEqual(got, PodSpecReferences("team", spec)) {
		t.Fatal("references are not deterministic")
	}
}

func TestPodSpecReferencesEmptyAndDuplicateFields(t *testing.T) {
	spec := corev1.PodSpec{ImagePullSecrets: []corev1.LocalObjectReference{{Name: "pull"}, {Name: "pull"}, {}}}
	got := PodSpecReferences("ns", spec)
	want := []PodSpecReference{{Ref{"Secret", "ns", "pull"}, "imagePullSecrets[].name", false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("references = %#v, want %#v", got, want)
	}
	if refs := PodSpecReferences("ns", corev1.PodSpec{}); len(refs) != 0 {
		t.Fatalf("empty PodSpec invented references: %#v", refs)
	}
}

func TestPodSpecReferencesHistoricalVolumeSecrets(t *testing.T) {
	var spec corev1.PodSpec
	if err := json.Unmarshal([]byte(`{"volumes":[
		{"name":"flex","flexVolume":{"driver":"example.com","secretRef":{"name":"flex"}}},
		{"name":"azure","azureFile":{"secretName":"azure","shareName":"data"}},
		{"name":"ceph","cephfs":{"secretRef":{"name":"ceph"}}},
		{"name":"rbd","rbd":{"secretRef":{"name":"rbd"}}},
		{"name":"cinder","cinder":{"secretRef":{"name":"cinder"}}},
		{"name":"scale","scaleIO":{"secretRef":{"name":"scale"}}},
		{"name":"iscsi","iscsi":{"secretRef":{"name":"iscsi"}}},
		{"name":"storage","storageos":{"secretRef":{"name":"storage"}}}
	]}`), &spec); err != nil {
		t.Fatal(err)
	}
	got := PodSpecReferences("ns", spec)
	want := []PodSpecReference{
		{Ref{"Secret", "ns", "azure"}, "volumes[].azureFile.secretName", false},
		{Ref{"Secret", "ns", "ceph"}, "volumes[].cephfs.secretRef.name", false},
		{Ref{"Secret", "ns", "cinder"}, "volumes[].cinder.secretRef.name", false},
		{Ref{"Secret", "ns", "flex"}, "volumes[].flexVolume.secretRef.name", false},
		{Ref{"Secret", "ns", "iscsi"}, "volumes[].iscsi.secretRef.name", false},
		{Ref{"Secret", "ns", "rbd"}, "volumes[].rbd.secretRef.name", false},
		{Ref{"Secret", "ns", "scale"}, "volumes[].scaleIO.secretRef.name", false},
		{Ref{"Secret", "ns", "storage"}, "volumes[].storageos.secretRef.name", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("historical volume references = %#v, want %#v", got, want)
	}
}

func TestPodSpecReferencesPreservesDistinctOptionality(t *testing.T) {
	optional := true
	spec := corev1.PodSpec{Containers: []corev1.Container{
		{EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Optional: &optional}}}},
		{EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}},
	}}
	want := []PodSpecReference{{Ref{"ConfigMap", "ns", "settings"}, "containers[].envFrom[].configMapRef.name", false}, {Ref{"ConfigMap", "ns", "settings"}, "containers[].envFrom[].configMapRef.name", true}}
	if got := PodSpecReferences("ns", spec); !reflect.DeepEqual(got, want) {
		t.Fatalf("distinct declaration requirements = %#v, want %#v", got, want)
	}
}

func TestVisitPodSpecReferencesDoesNotAllocate(t *testing.T) {
	optional := true
	spec := corev1.PodSpec{
		ServiceAccountName: "runner",
		ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "pull"}, {Name: "pull"}, {}},
		Containers: []corev1.Container{{EnvFrom: []corev1.EnvFromSource{
			{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}, Optional: &optional}},
			{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}},
		}}},
	}
	before := spec.DeepCopy()
	count, optionalCount := 0, 0
	allocs := testing.AllocsPerRun(100, func() {
		count, optionalCount = 0, 0
		VisitPodSpecReferences("team", spec, func(ref PodSpecReference) {
			count++
			if ref.Optional {
				optionalCount++
			}
			if ref.Namespace != "team" || ref.Name == "" || ref.Path == "" {
				t.Errorf("invalid declaration: %+v", ref)
			}
		})
	})
	if count != 5 || optionalCount != 1 {
		t.Fatalf("visited %d declarations (%d optional), want 5 (1 optional)", count, optionalCount)
	}
	if allocs != 0 {
		t.Fatalf("visitor allocated %.0f times per scan", allocs)
	}
	if !reflect.DeepEqual(&spec, before) {
		t.Fatal("visitor mutated PodSpec")
	}
}
