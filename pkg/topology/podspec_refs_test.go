package topology

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	typed := extractWorkloadReferences(spec, true)
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		t.Fatal(err)
	}
	dynamic, err := extractWorkloadReferencesFromMap(raw, true)
	if err != nil || !reflect.DeepEqual(typed, dynamic) || !typed.configMaps["debug-config"] || !typed.configMaps["projected-config"] || !typed.secrets["registry"] || !typed.secrets["driver-credential"] || !typed.secrets["projected-secret"] {
		t.Fatalf("typed=%+v dynamic=%+v err=%v", typed, dynamic, err)
	}
	if backing[1].Name != "must-not-be-overwritten" {
		t.Fatal("reference extraction mutated the PodSpec's container backing array")
	}
	if _, err := extractWorkloadReferencesFromMap(map[string]any{"containers": "invalid"}, true); err == nil {
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
	opts.ForRelationshipCache = true
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

func TestAdmittedPodFactsDoNotJoinNormalGraphComponents(t *testing.T) {
	provider := &mockProvider{configMaps: []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: "team"}}}, serviceAccounts: []*corev1.ServiceAccount{{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "team"}}}, secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "team"}}}}
	for i, name := range []string{"a", "b", "six"} {
		provider.deployments = append(provider.deployments, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}}})
		provider.deployments[i].Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "explicit-root-ca", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}}}}}}}}
		provider.replicaSets = append(provider.replicaSets, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: name}}}, Spec: appsv1.ReplicaSetSpec{Template: provider.deployments[i].Spec.Template}})
		count := 1
		if name == "six" {
			count = 6
		}
		for j := 0; j < count; j++ {
			config := fmt.Sprintf("%s-%d", name, j)
			provider.configMaps = append(provider.configMaps, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: config, Namespace: "team"}})
			provider.pods = append(provider.pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: config, Namespace: "team", Labels: map[string]string{"app": name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name}}}, Spec: corev1.PodSpec{ServiceAccountName: "default", ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}}, Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: config}}}}}}, Volumes: []corev1.Volume{{Name: "admitted", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}}}}}}}}}})
		}
	}
	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	opts.IncludeReplicaSets = true
	normal, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	adjacent := map[string][]string{}
	for _, edge := range normal.Edges {
		adjacent[edge.Source] = append(adjacent[edge.Source], edge.Target)
		adjacent[edge.Target] = append(adjacent[edge.Target], edge.Source)
	}
	visited := map[string]bool{}
	components := 0
	for _, name := range []string{"a", "b", "six"} {
		root := "deployment/team/" + name
		if visited[root] {
			continue
		}
		components++
		queue := []string{root}
		visited[root] = true
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, next := range adjacent[id] {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
	}
	if components != 3 {
		t.Fatalf("normal graph merged unrelated Deployments: %d components", components)
	}
	for _, edge := range normal.Edges {
		if edge.Source == "configmap/team/kube-root-ca.crt" || edge.Source == "serviceaccount/team/default" || edge.Source == "secret/team/registry" {
			t.Fatalf("admitted-only reference enrolled in normal graph: %+v", edge)
		}
	}
	opts.ForRelationshipCache = true
	cached, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range provider.pods {
		rel := GetRelationshipsWithObject("Pod", "team", pod.Name, pod, cached, provider, nil, nil)
		if rel == nil || len(rel.ConfigRefs) != 3 || rel.ServiceAccount == nil || rel.ServiceAccount.Name != "default" {
			t.Fatalf("individual/grouped Pod lost admitted facts: %s %+v", pod.Name, rel)
		}
		for _, ref := range rel.ConfigRefs {
			if ref.Name != pod.Name && ref.Name != "kube-root-ca.crt" && ref.Name != "registry" {
				t.Fatalf("Pod borrowed sibling configuration: %+v", ref)
			}
		}
	}
	reverse := GetRelationships("ConfigMap", "team", "six-0", cached, provider, nil)
	if reverse == nil || len(reverse.Consumers) != 1 || reverse.Consumers[0].Name != "six-0" {
		t.Fatalf("grouped Pod missing from reverse relationships: %+v", reverse)
	}
	indexed := GetRelationshipsWithObject("ConfigMap", "team", "six-0", provider.configMaps[3], cached, nil, nil, IndexByResource(cached))
	if indexed == nil || len(indexed.Consumers) != 1 || indexed.Consumers[0].Name != "six-0" {
		t.Fatalf("reverse lookup needs raw provider instead of cached facts: %+v", indexed)
	}
	account := GetRelationships("ServiceAccount", "team", "default", cached, provider, nil)
	if account == nil || len(account.Consumers) != 8 {
		t.Fatalf("ServiceAccount lost grouped Pod consumers: %+v", account)
	}
	for _, edge := range cached.Edges {
		if edge.Type == EdgeConfigures && len(edge.Target) >= 4 && edge.Target[:4] == "pod/" {
			t.Fatalf("admitted Pod projection enrolled a neighborhood hub: %+v", edge)
		}
	}
	opts.SummaryMode = true
	summarized, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	summaryRel := GetRelationshipsWithObject("Pod", "team", "six-0", provider.pods[2], summarized, provider, nil, nil)
	if summaryRel == nil || len(summaryRel.ConfigRefs) != 3 {
		t.Fatalf("summary Pod lost admitted configuration: %+v", summaryRel)
	}
	filtered := *cached
	filtered.Nodes = append([]Node(nil), cached.Nodes...)
	filtered.Edges = append([]Edge(nil), cached.Edges...)
	filtered.StripNodeKinds(map[NodeKind]bool{KindPod: true, KindPodGroup: true})
	denied := GetRelationships("ConfigMap", "team", "six-0", &filtered, provider, nil)
	if denied != nil {
		for _, consumer := range denied.Consumers {
			if consumer.Kind == "Pod" {
				t.Fatalf("reverse projection bypassed filtered Pod visibility: %+v", consumer)
			}
		}
	}
	cached.StripSecretsExcept(map[SARTuple]bool{})
	rel := GetRelationshipsWithObject("Pod", "team", "six-0", provider.pods[2], cached, provider, nil, nil)
	for _, ref := range rel.ConfigRefs {
		if ref.Kind == "Secret" {
			t.Fatalf("direct projection bypassed Secret filtering: %+v", ref)
		}
	}
}

func TestMalformedRolloutRetainsReadableConfiguration(t *testing.T) {
	rollout := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
		"metadata": map[string]any{"name": "app", "namespace": "team"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "resources": map[string]any{"requests": map[string]any{"cpu": "invalid-quantity"}}, "envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "settings"}}}}},
		}}},
	}}
	before := rollout.DeepCopy()
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			provider := &mockProvider{configMaps: []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "team"}}}}
			dp := &rolloutDynamicProvider{gvr: schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}, rollouts: []*unstructured.Unstructured{rollout}}
			opts := DefaultBuildOptions()
			opts.ForRelationshipCache = full
			topo, err := NewBuilder(provider).WithDynamic(dp).Build(opts)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, edge := range topo.Edges {
				if edge.Source == "configmap/team/settings" && edge.Target == "rollout/team/app" && edge.Type == EdgeConfigures {
					found = true
				}
			}
			if !found || len(topo.Warnings) == 0 {
				t.Fatalf("readable reference or decode warning lost: %+v", topo)
			}
			if !reflect.DeepEqual(rollout, before) {
				t.Fatal("recovery mutated the Rollout")
			}
		})
	}
}

func TestPodConsumerLookupPreservesFilteredResults(t *testing.T) {
	key := resourceid.ResourceKey("", "ConfigMap", "team", "settings")
	hiddenKey := resourceid.ResourceKey("", "Secret", "team", "hidden")
	refs := []ResourceRef{{Kind: "Pod", Namespace: "team", Name: "z"}, {Kind: "Pod", Namespace: "team", Name: "denied"}, {Kind: "Pod", Namespace: "team", Name: "a"}, {Kind: "Pod", Namespace: "team", Name: "a"}, {Kind: "Pod", Namespace: "other", Name: "a"}}
	topo := &Topology{Nodes: []Node{
		{ID: "configmap/team/settings", Kind: KindConfigMap, Name: "settings", Data: map[string]any{"namespace": "team"}},
		{ID: "pod/team/z", Kind: KindPod, Name: "z", Data: map[string]any{"namespace": "team"}},
		{ID: "podgroup/team/group", Kind: KindPodGroup, Name: "group", Data: map[string]any{"namespace": "team", "pods": []map[string]any{{"namespace": "team", "name": "a"}}}},
	}, podConsumers: map[string][]ResourceRef{key: refs, hiddenKey: refs}}
	beforeRefs := slices.Clone(refs)
	idx := IndexByResource(topo)
	want := []ResourceRef{{Kind: "Pod", Namespace: "team", Name: "a"}, {Kind: "Pod", Namespace: "team", Name: "z"}}
	for _, index := range []*RelationshipsIndex{nil, idx} {
		rel := GetRelationshipsWithObject("ConfigMap", "team", "settings", nil, topo, nil, nil, index)
		if !reflect.DeepEqual(rel.Consumers, want) {
			t.Fatalf("consumer results differ: %+v", rel.Consumers)
		}
		hidden := GetRelationshipsWithObject("Secret", "team", "hidden", nil, topo, nil, nil, index)
		if hidden != nil && len(hidden.Consumers) != 0 {
			t.Fatalf("hidden target leaked consumers: %+v", hidden)
		}
	}
	if !slices.Equal(topo.podConsumers[key], beforeRefs) {
		t.Fatal("lookup mutated the shared consumer list")
	}
	rel := GetRelationshipsWithObject("ConfigMap", "team", "settings", nil, topo, nil, nil, idx)
	rel.Consumers[0].Name = "changed"
	again := GetRelationshipsWithObject("ConfigMap", "team", "settings", nil, topo, nil, nil, idx)
	if !reflect.DeepEqual(again.Consumers, want) {
		t.Fatalf("request mutated subsequent results: %+v", again)
	}
}

func TestWorkloadReferenceRecoveryRetainsIndependentSubtrees(t *testing.T) {
	spec := map[string]any{
		"containers":          []any{"invalid", map[string]any{"resources": map[string]any{"requests": map[string]any{"cpu": "invalid"}}, "envFrom": []any{"invalid", map[string]any{"configMapRef": map[string]any{"name": "regular"}}}, "env": []any{map[string]any{"value": int64(5), "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "env-secret", "key": "token"}}}}}},
		"initContainers":      []any{map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "init"}}}}},
		"ephemeralContainers": []any{map[string]any{"envFrom": []any{map[string]any{"configMapRef": map[string]any{"name": "debug"}}}}},
		"volumes":             []any{"invalid", map[string]any{"name": int64(1), "configMap": map[string]any{"name": "volume"}}, map[string]any{"projected": map[string]any{"defaultMode": "invalid", "sources": []any{"invalid", map[string]any{"configMap": map[string]any{"name": "projected"}}}}}, map[string]any{"persistentVolumeClaim": map[string]any{"claimName": "claim"}}},
		"imagePullSecrets":    []any{"invalid", map[string]any{"name": "pull"}}, "serviceAccountName": "runner",
	}
	before := runtime.DeepCopyJSON(spec)
	for _, full := range []bool{false, true} {
		got, err := extractWorkloadReferencesFromMap(spec, full)
		configs := map[string]bool{"regular": true, "init": true, "volume": true}
		secrets := map[string]bool{"env-secret": true}
		if full {
			configs["debug"] = true
			configs["projected"] = true
			secrets["pull"] = true
		}
		want := workloadRefs{configMaps: configs, secrets: secrets, pvcs: map[string]bool{"claim": true}, serviceAccount: "runner"}
		if err == nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("full=%v got=%+v want=%+v err=%v", full, got, want, err)
		}
	}
	if !reflect.DeepEqual(spec, before) {
		t.Fatal("recovery mutated reference-bearing subtrees")
	}
}

func TestPodConsumerIndexStoresEachPodOncePerTarget(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team"}, Spec: corev1.PodSpec{
		Containers:       []corev1.Container{{EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "credential"}}}}, Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "credential"}, Key: "token"}}}}}},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "credential"}, {Name: "credential"}},
	}}
	provider := &mockProvider{pods: []*corev1.Pod{pod}, secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "team"}}}}
	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	opts.ForRelationshipCache = true
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	consumers := topo.podConsumers[resourceid.ResourceKey("", "Secret", "team", "credential")]
	if len(consumers) != 1 || consumers[0].Name != pod.Name {
		t.Fatalf("index stored repeated declarations as repeated consumers: %+v", consumers)
	}
}
