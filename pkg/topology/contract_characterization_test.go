package topology

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	k8score "github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// The characterization goldens record what the public topology functions
// return today for one fixture cluster: the graph, then for every fixture
// object the Related Resources, ManagedBy and delete preview production
// computes for it, plus selected neighborhoods and a per-user projection.
// They pin behavior, not correctness — several recorded answers are known
// defects. A change meant to leave public answers untouched keeps them
// byte-identical; a change that alters them regenerates them with
// -update-characterization and justifies every changed line.
var updateCharacterization = flag.Bool("update-characterization", false, "rewrite the topology characterization goldens")

// charDynamic serves the fixture's custom resources. Discovery questions go to
// the real k8score.ResourceDiscovery, registered with the built-in kinds and
// the fixture's CRDs, so the builder and relationship lookups take the same
// discovery paths production does.
type charDynamic struct {
	disc      *k8score.ResourceDiscovery
	watched   []schema.GroupVersionResource
	resources map[schema.GroupVersionResource][]*unstructured.Unstructured
}

func newCharDynamic() *charDynamic {
	disc, err := k8score.NewResourceDiscovery(fake.NewSimpleClientset().Discovery())
	if err != nil {
		panic(err)
	}
	for _, builtin := range resourceid.Builtins {
		version := builtin.Version
		if version == "" {
			version = "v1"
		}
		disc.AddAPIResource(k8score.APIResource{Group: builtin.Group, Version: version, Kind: builtin.Kind, Name: builtin.Resource, Namespaced: builtin.Namespaced, Verbs: []string{"get", "list", "watch"}})
	}
	return &charDynamic{disc: disc, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{}}
}

func (d *charDynamic) add(group, version, kind, resource string, namespaced bool, objs ...*unstructured.Unstructured) {
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
	if _, known := d.resources[gvr]; !known {
		d.disc.AddAPIResource(k8score.APIResource{Group: group, Version: version, Kind: kind, Name: resource, Namespaced: namespaced, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
		d.watched = append(d.watched, gvr)
	}
	d.resources[gvr] = append(d.resources[gvr], objs...)
}

func (d *charDynamic) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if namespace == "" {
		return d.resources[gvr], nil
	}
	var out []*unstructured.Unstructured
	for _, obj := range d.resources[gvr] {
		if obj.GetNamespace() == namespace {
			out = append(out, obj)
		}
	}
	return out, nil
}

func (d *charDynamic) ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error) {
	if len(namespaces) == 0 {
		return d.resources[gvr], nil
	}
	var out []*unstructured.Unstructured
	for _, obj := range d.resources[gvr] {
		if obj.GetNamespace() == "" || MatchesNamespace(namespaces, obj.GetNamespace()) {
			out = append(out, obj)
		}
	}
	return out, nil
}

func (d *charDynamic) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	for _, obj := range d.resources[gvr] {
		if obj.GetNamespace() == namespace && obj.GetName() == name {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("%s %s/%s not found", gvr.Resource, namespace, name)
}

func (d *charDynamic) GetWatchedResources() []schema.GroupVersionResource {
	return append([]schema.GroupVersionResource(nil), d.watched...)
}

func (d *charDynamic) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return k8score.CRDDiscoveryComplete
}

func (d *charDynamic) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	return d.disc.GetGVR(kindOrName)
}

func (d *charDynamic) GetGVRWithGroup(kindOrName, group string) (schema.GroupVersionResource, bool) {
	return d.disc.GetGVRWithGroup(kindOrName, group)
}

func (d *charDynamic) GetKindForGVR(gvr schema.GroupVersionResource) string {
	return d.disc.GetKindForGVR(gvr)
}

func (d *charDynamic) IsCRD(kind string) bool { return d.disc.IsCRD(kind) }

func (d *charDynamic) IsCRDGVR(gvr schema.GroupVersionResource) bool { return d.disc.IsCRDGVR(gvr) }

// charObject is one fixture object with the exact identity production
// addresses it by.
type charObject struct {
	group, kind, resource string
	obj                   metav1.Object
}

func (o charObject) ref() ResourceRef {
	return ResourceRef{Kind: o.kind, Group: o.group, Namespace: o.obj.GetNamespace(), Name: o.obj.GetName()}
}

func (o charObject) key() string {
	return resourceid.ResourceKey(o.group, o.kind, o.obj.GetNamespace(), o.obj.GetName())
}

type charFixture struct {
	provider *mockProvider
	dynamic  *charDynamic
	objects  []charObject
}

func charController(apiVersion, kind, name, uid string) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(uid), Controller: &controller}
}

func charMeta(namespace, name string, labels map[string]string, owners ...metav1.OwnerReference) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID("uid-" + namespace + "-" + name), Labels: labels, OwnerReferences: owners}
}

func charUnstructured(apiVersion, kind, namespace, name string, labels map[string]string, spec, status map[string]any, owners ...metav1.OwnerReference) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}}}
	if namespace != "" {
		obj.SetNamespace(namespace)
	}
	obj.SetUID(types.UID("uid-" + namespace + "-" + name))
	if labels != nil {
		obj.SetLabels(labels)
	}
	if spec != nil {
		obj.Object["spec"] = spec
	}
	if status != nil {
		obj.Object["status"] = status
	}
	if len(owners) > 0 {
		obj.SetOwnerReferences(owners)
	}
	return obj
}

func charPodSpec(configMap, secret string, mountedSecrets ...string) corev1.PodSpec {
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example/app:1"}}}
	for _, name := range mountedSecrets {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name}}})
	}
	if configMap != "" {
		spec.Containers[0].EnvFrom = append(spec.Containers[0].EnvFrom, corev1.EnvFromSource{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMap}}})
	}
	if secret != "" {
		spec.Containers[0].EnvFrom = append(spec.Containers[0].EnvFrom, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secret}}})
	}
	return spec
}

func charPod(namespace, name, node string, labels map[string]string, spec corev1.PodSpec, owner metav1.OwnerReference) *corev1.Pod {
	spec.NodeName = node
	return &corev1.Pod{ObjectMeta: charMeta(namespace, name, labels, owner), Spec: spec, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

func charDeployment(namespace, name string, replicas int32, labels map[string]string, spec corev1.PodSpec) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: charMeta(namespace, name, labels),
		Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}, Spec: spec}},
		Status: appsv1.DeploymentStatus{Replicas: replicas, ReadyReplicas: replicas, AvailableReplicas: replicas},
	}
}

func charReplicaSet(namespace, name string, replicas int32, owner metav1.OwnerReference, app string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: charMeta(namespace, name, map[string]string{"app": app}, owner),
		Spec:       appsv1.ReplicaSetSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}}},
		Status:     appsv1.ReplicaSetStatus{Replicas: replicas, ReadyReplicas: replicas},
	}
}

// charCluster is the fixture: every family of ownership, management and
// association producer the contract classifies, in one cluster.
func charCluster() *charFixture {
	p := &mockProvider{}
	d := newCharDynamic()
	f := &charFixture{provider: p, dynamic: d}
	typed := func(group, kind, resource string, obj metav1.Object) {
		f.objects = append(f.objects, charObject{group: group, kind: kind, resource: resource, obj: obj})
	}
	dyn := func(group, version, kind, resource string, namespaced bool, objs ...*unstructured.Unstructured) {
		d.add(group, version, kind, resource, namespaced, objs...)
		for _, obj := range objs {
			f.objects = append(f.objects, charObject{group: group, kind: kind, resource: resource, obj: obj})
		}
	}
	addPod := func(pod *corev1.Pod) { p.pods = append(p.pods, pod); typed("", "Pod", "pods", pod) }
	addDeployment := func(o *appsv1.Deployment) {
		p.deployments = append(p.deployments, o)
		typed("apps", "Deployment", "deployments", o)
	}
	addReplicaSet := func(o *appsv1.ReplicaSet) {
		p.replicaSets = append(p.replicaSets, o)
		typed("apps", "ReplicaSet", "replicasets", o)
	}
	addSecret := func(o *corev1.Secret) { p.secrets = append(p.secrets, o); typed("", "Secret", "secrets", o) }
	addConfigMap := func(o *corev1.ConfigMap) {
		p.configMaps = append(p.configMaps, o)
		typed("", "ConfigMap", "configmaps", o)
	}
	addJob := func(o *batchv1.Job) { p.jobs = append(p.jobs, o); typed("batch", "Job", "jobs", o) }

	// shop/web: Argo-managed (annotation and label tracking) Deployment with
	// config, secret, service, ingress, HPA, PDB.
	web := charDeployment("shop", "web", 2, map[string]string{"argocd.argoproj.io/instance": "shop", "app.kubernetes.io/instance": "shop"}, charPodSpec("web-config", "web-secret", "web-tls"))
	web.Annotations = map[string]string{"argocd.argoproj.io/tracking-id": "argocd_shop:apps/Deployment:shop/web"}
	addDeployment(web)
	webRS := charReplicaSet("shop", "web-7d9f8", 2, charController("apps/v1", "Deployment", "web", string(web.UID)), "web")
	addReplicaSet(webRS)
	for i := 0; i < 2; i++ {
		addPod(charPod("shop", fmt.Sprintf("web-7d9f8-%d", i), "node-a", map[string]string{"app": "web"}, charPodSpec("web-config", "web-secret", "web-tls"), charController("apps/v1", "ReplicaSet", webRS.Name, string(webRS.UID))))
	}
	addConfigMap(&corev1.ConfigMap{ObjectMeta: charMeta("shop", "web-config", nil)})
	addSecret(&corev1.Secret{ObjectMeta: charMeta("shop", "web-secret", nil)})
	addSecret(&corev1.Secret{ObjectMeta: charMeta("shop", "web-tls", nil), Type: corev1.SecretTypeTLS})
	webSvc := &corev1.Service{ObjectMeta: charMeta("shop", "web", nil), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}, Ports: []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(8080)}}}}
	p.services = append(p.services, webSvc)
	typed("", "Service", "services", webSvc)
	pathType := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{ObjectMeta: charMeta("shop", "web", nil), Spec: networkingv1.IngressSpec{
		TLS: []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}, SecretName: "web-tls"}},
		Rules: []networkingv1.IngressRule{{Host: "shop.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pathType,
			Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web", Port: networkingv1.ServiceBackendPort{Number: 80}}}}}}}}}}}
	p.ingresses = append(p.ingresses, ing)
	typed("networking.k8s.io", "Ingress", "ingresses", ing)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: charMeta("shop", "web", nil), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "web"}, MaxReplicas: 4}}
	p.hpas = append(p.hpas, hpa)
	typed("autoscaling", "HorizontalPodAutoscaler", "horizontalpodautoscalers", hpa)
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: charMeta("shop", "web", nil), Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}
	p.pdbs = append(p.pdbs, pdb)
	typed("policy", "PodDisruptionBudget", "poddisruptionbudgets", pdb)
	netpol := &networkingv1.NetworkPolicy{ObjectMeta: charMeta("shop", "default-deny", nil), Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}}}
	p.networkPolicies = append(p.networkPolicies, netpol)
	typed("networking.k8s.io", "NetworkPolicy", "networkpolicies", netpol)

	// shop/cart: Kustomization shop records it; its Pod carries stale labels
	// naming Kustomization platform.
	cart := charDeployment("shop", "cart", 1, map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}, charPodSpec("", ""))
	addDeployment(cart)
	cartRS := charReplicaSet("shop", "cart-5c6", 1, charController("apps/v1", "Deployment", "cart", string(cart.UID)), "cart")
	addReplicaSet(cartRS)
	addPod(charPod("shop", "cart-5c6-a", "node-a", map[string]string{"app": "cart", "kustomize.toolkit.fluxcd.io/name": "platform", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}, charPodSpec("", ""), charController("apps/v1", "ReplicaSet", cartRS.Name, string(cartRS.UID))))

	// shop/big: seven replicas, grouped in every graph.
	big := charDeployment("shop", "big", 7, nil, charPodSpec("", ""))
	addDeployment(big)
	bigRS := charReplicaSet("shop", "big-8f9", 7, charController("apps/v1", "Deployment", "big", string(big.UID)), "big")
	addReplicaSet(bigRS)
	for i := 0; i < 7; i++ {
		addPod(charPod("shop", fmt.Sprintf("big-8f9-%d", i), "node-a", map[string]string{"app": "big"}, charPodSpec("", ""), charController("apps/v1", "ReplicaSet", bigRS.Name, string(bigRS.UID))))
	}

	// shop/api: replaced Deployment; the live ReplicaSet still names the old UID.
	api := charDeployment("shop", "api", 1, nil, charPodSpec("", ""))
	addDeployment(api)
	apiRS := charReplicaSet("shop", "api-6b7", 1, charController("apps/v1", "Deployment", "api", "uid-shop-api-OLD"), "api")
	addReplicaSet(apiRS)
	addPod(charPod("shop", "api-6b7-a", "node-a", map[string]string{"app": "api"}, charPodSpec("", ""), charController("apps/v1", "ReplicaSet", apiRS.Name, string(apiRS.UID))))

	// Flux HelmRelease shop/redis: paired origin labels on every kind its label
	// producer reads; redis-legacy only by the bare instance label; a hook Job.
	redisLabels := map[string]string{"helm.toolkit.fluxcd.io/name": "redis", "helm.toolkit.fluxcd.io/namespace": "shop"}
	addDeployment(charDeployment("shop", "redis", 1, redisLabels, charPodSpec("", "")))
	addDeployment(charDeployment("shop", "redis-legacy", 1, map[string]string{"app.kubernetes.io/instance": "redis"}, charPodSpec("", "")))
	redisSvc := &corev1.Service{ObjectMeta: charMeta("shop", "redis", redisLabels), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "redis"}}}
	p.services = append(p.services, redisSvc)
	typed("", "Service", "services", redisSvc)
	one := int32(1)
	redisSts := &appsv1.StatefulSet{ObjectMeta: charMeta("shop", "redis-replica", redisLabels), Spec: appsv1.StatefulSetSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "redis-replica"}}}}
	p.statefulSets = append(p.statefulSets, redisSts)
	typed("apps", "StatefulSet", "statefulsets", redisSts)
	redisDs := &appsv1.DaemonSet{ObjectMeta: charMeta("shop", "redis-exporter", redisLabels), Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "redis-exporter"}}}}
	p.daemonSets = append(p.daemonSets, redisDs)
	typed("apps", "DaemonSet", "daemonsets", redisDs)
	hookJob := &batchv1.Job{ObjectMeta: charMeta("shop", "redis-migrate", map[string]string{"helm.toolkit.fluxcd.io/name": "redis", "helm.toolkit.fluxcd.io/namespace": "shop"})}
	hookJob.Annotations = map[string]string{"helm.sh/hook": "post-install"}
	addJob(hookJob)
	redisBackup := &batchv1.CronJob{ObjectMeta: charMeta("shop", "redis-backup", redisLabels), Spec: batchv1.CronJobSpec{Schedule: "0 3 * * *"}}
	p.cronJobs = append(p.cronJobs, redisBackup)
	typed("batch", "CronJob", "cronjobs", redisBackup)

	// Native Helm release (annotations only, no Flux CR).
	legacyHelm := charDeployment("shop", "legacy-helm", 1, map[string]string{"app.kubernetes.io/managed-by": "Helm"}, charPodSpec("", ""))
	legacyHelm.Annotations = map[string]string{"meta.helm.sh/release-name": "legacy", "meta.helm.sh/release-namespace": "shop"}
	addDeployment(legacyHelm)

	// team/nightly: CronJob -> Job -> Pod.
	cron := &batchv1.CronJob{ObjectMeta: charMeta("team", "nightly", nil), Spec: batchv1.CronJobSpec{Schedule: "0 1 * * *"}}
	p.cronJobs = append(p.cronJobs, cron)
	typed("batch", "CronJob", "cronjobs", cron)
	job := &batchv1.Job{ObjectMeta: charMeta("team", "nightly-29001", map[string]string{"batch.kubernetes.io/job-name": "nightly-29001"}, charController("batch/v1", "CronJob", "nightly", string(cron.UID)))}
	addJob(job)
	addPod(charPod("team", "nightly-29001-x", "node-a", map[string]string{"batch.kubernetes.io/job-name": "nightly-29001"}, charPodSpec("", ""), charController("batch/v1", "Job", job.Name, string(job.UID))))
	// An older Job still names the CronJob's previous incarnation beside its
	// controller: two records of one pair, which the graph keeps as two edges.
	previousCron := charController("batch/v1", "CronJob", "nightly", "uid-team-nightly-previous")
	previousCron.Controller = nil
	addJob(&batchv1.Job{ObjectMeta: charMeta("team", "nightly-28999", map[string]string{"batch.kubernetes.io/job-name": "nightly-28999"}, charController("batch/v1", "CronJob", "nightly", string(cron.UID)), previousCron)})

	// team/db: StatefulSet with a bound claim and a SealedSecret-produced Secret.
	db := &appsv1.StatefulSet{ObjectMeta: charMeta("team", "db", nil), Spec: appsv1.StatefulSetSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "db"}}, Spec: charPodSpec("", "db-creds")}}}
	p.statefulSets = append(p.statefulSets, db)
	typed("apps", "StatefulSet", "statefulsets", db)
	dbPodSpec := charPodSpec("", "db-creds")
	dbPodSpec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-db-0"}}}}
	addPod(charPod("team", "db-0", "node-b", map[string]string{"app": "db"}, dbPodSpec, charController("apps/v1", "StatefulSet", "db", string(db.UID))))
	storageClass := "standard"
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: charMeta("team", "data-db-0", nil), Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-db-0", StorageClassName: &storageClass}}
	p.pvcs = append(p.pvcs, pvc)
	typed("", "PersistentVolumeClaim", "persistentvolumeclaims", pvc)
	pv := &corev1.PersistentVolume{ObjectMeta: charMeta("", "pv-db-0", nil), Spec: corev1.PersistentVolumeSpec{StorageClassName: storageClass, ClaimRef: &corev1.ObjectReference{Namespace: "team", Name: "data-db-0"}}}
	p.pvs = append(p.pvs, pv)
	typed("", "PersistentVolume", "persistentvolumes", pv)
	addSecret(&corev1.Secret{ObjectMeta: charMeta("team", "db-creds", nil)})

	// infra/agent: DaemonSet Pods on two Nodes.
	agent := &appsv1.DaemonSet{ObjectMeta: charMeta("infra", "agent", nil), Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "agent"}}}}
	p.daemonSets = append(p.daemonSets, agent)
	typed("apps", "DaemonSet", "daemonsets", agent)
	for _, node := range []string{"node-a", "node-b"} {
		addPod(charPod("infra", "agent-"+node, node, map[string]string{"app": "agent"}, charPodSpec("", ""), charController("apps/v1", "DaemonSet", "agent", string(agent.UID))))
		n := &corev1.Node{ObjectMeta: charMeta("", node, map[string]string{"karpenter.sh/nodepool": "default"})}
		p.nodes = append(p.nodes, n)
		typed("", "Node", "nodes", n)
	}

	// GitOps managers.
	dyn("argoproj.io", "v1alpha1", "Application", "applications", true, charUnstructured("argoproj.io/v1alpha1", "Application", "argocd", "shop", nil,
		map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}, "source": map[string]any{"repoURL": "https://example.com/shop.git", "path": "deploy"}},
		map[string]any{"sync": map[string]any{"status": "Synced"}, "health": map[string]any{"status": "Healthy"}, "resources": []any{
			map[string]any{"group": "apps", "kind": "Deployment", "namespace": "shop", "name": "web"},
			map[string]any{"group": "", "kind": "Service", "namespace": "shop", "name": "web"},
			map[string]any{"group": "networking.k8s.io", "kind": "Ingress", "namespace": "shop", "name": "web"},
		}}))
	inventory := func(ids ...string) map[string]any {
		entries := []any{}
		for _, id := range ids {
			entries = append(entries, map[string]any{"id": id, "v": "v1"})
		}
		return map[string]any{"inventory": map[string]any{"entries": entries}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	}
	dyn("kustomize.toolkit.fluxcd.io", "v1", "Kustomization", "kustomizations", true,
		charUnstructured("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "shop", nil, map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}, "path": "./shop"},
			inventory("shop_cart_apps_Deployment", "shop_redis_helm.toolkit.fluxcd.io_HelmRelease")),
		charUnstructured("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "platform", nil, map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}, "path": "./platform"},
			inventory("flux-system_shop_kustomize.toolkit.fluxcd.io_Kustomization")))
	dyn("source.toolkit.fluxcd.io", "v1", "GitRepository", "gitrepositories", true, charUnstructured("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "repo", nil, map[string]any{"url": "https://example.com/repo.git"}, nil))
	dyn("helm.toolkit.fluxcd.io", "v2", "HelmRelease", "helmreleases", true, charUnstructured("helm.toolkit.fluxcd.io/v2", "HelmRelease", "shop", "redis",
		map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"},
		map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "redis", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "bitnami", "namespace": "flux-system"}}}}, nil))

	// Argo Rollouts: an active canary with a background analysis run, a
	// settled canary (its hidden ReplicaSet collapses to a shortcut), and a
	// Rollout carrying the HelmRelease's origin labels.
	checkout := charUnstructured("argoproj.io/v1alpha1", "Rollout", "shop", "checkout", nil,
		map[string]any{"replicas": int64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": "checkout"}}, "strategy": map[string]any{"canary": map[string]any{}}},
		map[string]any{"replicas": int64(1), "readyReplicas": int64(1), "phase": "Healthy", "canary": map[string]any{"currentBackgroundAnalysisRunStatus": map[string]any{"name": "checkout-bg-1", "status": "Running"}}})
	payments := charUnstructured("argoproj.io/v1alpha1", "Rollout", "shop", "payments", nil,
		map[string]any{"replicas": int64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": "payments"}}, "strategy": map[string]any{"canary": map[string]any{}}},
		map[string]any{"replicas": int64(1), "readyReplicas": int64(1), "phase": "Healthy", "stableRS": "6d8f", "currentPodHash": "6d8f"})
	redisRollout := charUnstructured("argoproj.io/v1alpha1", "Rollout", "shop", "redis-proxy", map[string]string{"helm.toolkit.fluxcd.io/name": "redis", "helm.toolkit.fluxcd.io/namespace": "shop"},
		map[string]any{"replicas": int64(1), "selector": map[string]any{"matchLabels": map[string]any{"app": "redis-proxy"}}, "strategy": map[string]any{"canary": map[string]any{}}},
		map[string]any{"replicas": int64(1), "readyReplicas": int64(1), "phase": "Healthy", "stableRS": "1a2b", "currentPodHash": "1a2b"})
	dyn("argoproj.io", "v1alpha1", "Rollout", "rollouts", true, checkout, payments, redisRollout)
	checkoutRS := charReplicaSet("shop", "checkout-abc", 1, charController("argoproj.io/v1alpha1", "Rollout", "checkout", string(checkout.GetUID())), "checkout")
	addReplicaSet(checkoutRS)
	addPod(charPod("shop", "checkout-abc-a", "node-b", map[string]string{"app": "checkout"}, charPodSpec("", ""), charController("apps/v1", "ReplicaSet", checkoutRS.Name, string(checkoutRS.UID))))
	paymentsRS := charReplicaSet("shop", "payments-6d8f", 1, charController("argoproj.io/v1alpha1", "Rollout", "payments", string(payments.GetUID())), "payments")
	paymentsRS.Labels["rollouts-pod-template-hash"] = "6d8f"
	addReplicaSet(paymentsRS)
	addPod(charPod("shop", "payments-6d8f-a", "node-b", map[string]string{"app": "payments", "rollouts-pod-template-hash": "6d8f"}, charPodSpec("", ""), charController("apps/v1", "ReplicaSet", paymentsRS.Name, string(paymentsRS.UID))))

	// Associations: Certificate -> Secret (spec.secretName), SealedSecret -> Secret (name).
	dyn("cert-manager.io", "v1", "Certificate", "certificates", true, charUnstructured("cert-manager.io/v1", "Certificate", "shop", "web-tls", nil,
		map[string]any{"secretName": "web-tls", "dnsNames": []any{"shop.example.com"}, "issuerRef": map[string]any{"name": "letsencrypt", "kind": "ClusterIssuer"}},
		map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}))
	dyn("bitnami.com", "v1alpha1", "SealedSecret", "sealedsecrets", true, charUnstructured("bitnami.com/v1alpha1", "SealedSecret", "team", "db-creds", nil, map[string]any{"encryptedData": map[string]any{"password": "AgB..."}}, nil))

	// Gateway API: class binding, Gateway, HTTPRoute -> Service.
	dyn("gateway.networking.k8s.io", "v1", "GatewayClass", "gatewayclasses", false, charUnstructured("gateway.networking.k8s.io/v1", "GatewayClass", "", "envoy", nil, map[string]any{"controllerName": "example.com/gateway"}, nil))
	dyn("gateway.networking.k8s.io", "v1", "Gateway", "gateways", true, charUnstructured("gateway.networking.k8s.io/v1", "Gateway", "shop", "public", nil,
		map[string]any{"gatewayClassName": "envoy", "listeners": []any{map[string]any{"name": "http", "port": int64(80), "protocol": "HTTP"}}}, nil))
	dyn("gateway.networking.k8s.io", "v1", "HTTPRoute", "httproutes", true, charUnstructured("gateway.networking.k8s.io/v1", "HTTPRoute", "shop", "web", nil,
		map[string]any{"parentRefs": []any{map[string]any{"name": "public"}}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "web", "port": int64(80)}}}}}, nil))

	// Karpenter: NodePool -> NodeClaim (ownerRef) -> Node (status.nodeName).
	pool := charUnstructured("karpenter.sh/v1", "NodePool", "", "default", nil, map[string]any{"template": map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"}}}}, nil)
	dyn("karpenter.sh", "v1", "NodePool", "nodepools", false, pool)
	dyn("karpenter.sh", "v1", "NodeClaim", "nodeclaims", false, charUnstructured("karpenter.sh/v1", "NodeClaim", "", "default-x1", map[string]string{"karpenter.sh/nodepool": "default"}, nil,
		map[string]any{"nodeName": "node-b", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}, charController("karpenter.sh/v1", "NodePool", "default", string(pool.GetUID()))))

	// Generic CRD owned by a workload.
	dyn("example.io", "v1", "Widget", "widgets", true, charUnstructured("example.io/v1", "Widget", "shop", "web-widget", nil, map[string]any{"size": int64(1)}, nil,
		charController("apps/v1", "Deployment", "web", string(web.UID))))

	// Reflection: a late producer that runs after the resource builder.
	addConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "web-config-mirror", UID: "uid-team-web-config-mirror",
		Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "shop/web-config"}}})

	// Knative Serving: Service -> Configuration/Route -> Revision -> typed
	// Deployment; Eventing: a Broker owning its backing channel.
	ksvc := charUnstructured("serving.knative.dev/v1", "Service", "shop", "hello", nil, map[string]any{"template": map[string]any{}}, map[string]any{"latestReadyRevisionName": "hello-00001"})
	kcfg := charUnstructured("serving.knative.dev/v1", "Configuration", "shop", "hello", nil, nil, nil, charController("serving.knative.dev/v1", "Service", "hello", string(ksvc.GetUID())))
	kroute := charUnstructured("serving.knative.dev/v1", "Route", "shop", "hello", nil, nil, nil, charController("serving.knative.dev/v1", "Service", "hello", string(ksvc.GetUID())))
	krev := charUnstructured("serving.knative.dev/v1", "Revision", "shop", "hello-00001", nil, nil, nil, charController("serving.knative.dev/v1", "Configuration", "hello", string(kcfg.GetUID())))
	dyn("serving.knative.dev", "v1", "Service", "services", true, ksvc)
	dyn("serving.knative.dev", "v1", "Configuration", "configurations", true, kcfg)
	dyn("serving.knative.dev", "v1", "Route", "routes", true, kroute)
	dyn("serving.knative.dev", "v1", "Revision", "revisions", true, krev)
	helloDeploy := charDeployment("shop", "hello-00001-deployment", 1, nil, charPodSpec("", ""))
	helloDeploy.OwnerReferences = []metav1.OwnerReference{charController("serving.knative.dev/v1", "Revision", "hello-00001", string(krev.GetUID()))}
	addDeployment(helloDeploy)
	broker := charUnstructured("eventing.knative.dev/v1", "Broker", "shop", "default", nil, map[string]any{}, map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}})
	dyn("eventing.knative.dev", "v1", "Broker", "brokers", true, broker)
	dyn("messaging.knative.dev", "v1", "InMemoryChannel", "inmemorychannels", true, charUnstructured("messaging.knative.dev/v1", "InMemoryChannel", "shop", "default-kne-trigger", nil, map[string]any{}, nil,
		charController("eventing.knative.dev/v1", "Broker", "default", string(broker.GetUID()))))

	// Cluster API: Cluster -> control plane / MachineDeployment / MachinePool,
	// MachineDeployment -> MachineSet -> Machine -> Node.
	capiCluster := charUnstructured("cluster.x-k8s.io/v1beta1", "Cluster", "capi", "prod", nil, map[string]any{}, map[string]any{"phase": "Provisioned"})
	clusterOwner := charController("cluster.x-k8s.io/v1beta1", "Cluster", "prod", string(capiCluster.GetUID()))
	md := charUnstructured("cluster.x-k8s.io/v1beta1", "MachineDeployment", "capi", "prod-md", nil, map[string]any{"clusterName": "prod"}, nil, clusterOwner)
	ms := charUnstructured("cluster.x-k8s.io/v1beta1", "MachineSet", "capi", "prod-md-abc", nil, map[string]any{"clusterName": "prod"}, nil, charController("cluster.x-k8s.io/v1beta1", "MachineDeployment", "prod-md", string(md.GetUID())))
	machine := charUnstructured("cluster.x-k8s.io/v1beta1", "Machine", "capi", "prod-md-abc-x", nil, map[string]any{"clusterName": "prod"}, map[string]any{"nodeRef": map[string]any{"name": "node-a"}, "phase": "Running"}, charController("cluster.x-k8s.io/v1beta1", "MachineSet", "prod-md-abc", string(ms.GetUID())))
	dyn("cluster.x-k8s.io", "v1beta1", "Cluster", "clusters", true, capiCluster)
	dyn("cluster.x-k8s.io", "v1beta1", "MachineDeployment", "machinedeployments", true, md)
	dyn("cluster.x-k8s.io", "v1beta1", "MachineSet", "machinesets", true, ms)
	dyn("cluster.x-k8s.io", "v1beta1", "Machine", "machines", true, machine)
	dyn("cluster.x-k8s.io", "v1beta1", "MachinePool", "machinepools", true, charUnstructured("cluster.x-k8s.io/v1beta1", "MachinePool", "capi", "prod-mp", nil, map[string]any{"clusterName": "prod"}, nil, clusterOwner))
	dyn("controlplane.cluster.x-k8s.io", "v1beta1", "KubeadmControlPlane", "kubeadmcontrolplanes", true, charUnstructured("controlplane.cluster.x-k8s.io/v1beta1", "KubeadmControlPlane", "capi", "prod-cp", nil, map[string]any{"replicas": int64(1)}, nil, clusterOwner))

	// Argo Workflows: CronWorkflow -> Workflow -> Pod (label-linked).
	cronWF := charUnstructured("argoproj.io/v1alpha1", "CronWorkflow", "team", "report", nil, map[string]any{"schedule": "0 2 * * *"}, nil)
	wf := charUnstructured("argoproj.io/v1alpha1", "Workflow", "team", "report-1700", map[string]string{"workflows.argoproj.io/cron-workflow": "report"}, map[string]any{}, map[string]any{"phase": "Running"},
		charController("argoproj.io/v1alpha1", "CronWorkflow", "report", string(cronWF.GetUID())))
	dyn("argoproj.io", "v1alpha1", "CronWorkflow", "cronworkflows", true, cronWF)
	dyn("argoproj.io", "v1alpha1", "Workflow", "workflows", true, wf)
	addPod(charPod("team", "report-1700-main", "node-b", map[string]string{"workflows.argoproj.io/workflow": "report-1700"}, charPodSpec("", ""), charController("argoproj.io/v1alpha1", "Workflow", "report-1700", string(wf.GetUID()))))

	// KEDA: ScaledJob -> Job -> Pod.
	scaledJob := charUnstructured("keda.sh/v1alpha1", "ScaledJob", "team", "queue-worker", nil, map[string]any{"jobTargetRef": map[string]any{}}, nil)
	dyn("keda.sh", "v1alpha1", "ScaledJob", "scaledjobs", true, scaledJob)
	kedaJob := &batchv1.Job{ObjectMeta: charMeta("team", "queue-worker-abc", map[string]string{"batch.kubernetes.io/job-name": "queue-worker-abc"}, charController("keda.sh/v1alpha1", "ScaledJob", "queue-worker", string(scaledJob.GetUID())))}
	addJob(kedaJob)
	addPod(charPod("team", "queue-worker-abc-x", "node-b", map[string]string{"batch.kubernetes.io/job-name": "queue-worker-abc"}, charPodSpec("", ""), charController("batch/v1", "Job", kedaJob.Name, string(kedaJob.UID))))

	// Crossplane v1: Claim -> XR -> composed managed resource (spec references).
	dyn("demo.example.io", "v1alpha1", "DatabaseClaim", "databaseclaims", true, charUnstructured("demo.example.io/v1alpha1", "DatabaseClaim", "shop", "orders-db", nil,
		map[string]any{"compositionRef": map[string]any{"name": "xdatabases.demo.example.io"}, "resourceRef": map[string]any{"apiVersion": "demo.example.io/v1alpha1", "kind": "XDatabase", "name": "orders-db-xr"}}, nil))
	dyn("demo.example.io", "v1alpha1", "XDatabase", "xdatabases", false, charUnstructured("demo.example.io/v1alpha1", "XDatabase", "", "orders-db-xr", nil,
		map[string]any{"resourceRefs": []any{map[string]any{"apiVersion": "kubernetes.crossplane.io/v1alpha2", "kind": "Object", "name": "orders-db-config"}}}, nil))
	// The composed resource also carries its XR's controller reference, so two
	// producers link the same pair.
	dyn("kubernetes.crossplane.io", "v1alpha2", "Object", "objects", false, charUnstructured("kubernetes.crossplane.io/v1alpha2", "Object", "", "orders-db-config", nil,
		map[string]any{"providerConfigRef": map[string]any{"name": "default"}}, nil, charController("demo.example.io/v1alpha1", "XDatabase", "orders-db-xr", "uid--orders-db-xr")))

	return f
}

type charCase struct {
	name string
	opts func() BuildOptions
}

func charCases() []charCase {
	return []charCase{
		// The SSE broadcaster's relationship cache (internal/server/sse.go buildFullTopology).
		{"sse-relationship-cache", charSSEOptions},
		{"relationship-options", RelationshipBuildOptions},
		{"resources-default", DefaultBuildOptions},
		{"resources-shop", func() BuildOptions { o := DefaultBuildOptions(); o.Namespaces = []string{"shop"}; return o }},
		{"resources-no-generic-crds", func() BuildOptions { o := DefaultBuildOptions(); o.IncludeGenericCRDs = false; return o }},
		{"traffic", func() BuildOptions { o := DefaultBuildOptions(); o.ViewMode = ViewModeTraffic; return o }},
	}
}

func charSSEOptions() BuildOptions {
	o := DefaultBuildOptions()
	o.IncludeReplicaSets, o.ForRelationshipCache, o.IncludeSecrets = true, true, true
	return o
}

// canonicalRefLists sorts every []ResourceRef list inside v. Main's builder
// emits some lists (a Secret's consumers, for one) in map order, so list order
// is not part of the pinned contract; singular fields and ManagedBy, whose
// first element callers treat as primary, stay exactly as returned.
func canonicalRefLists(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			canonicalRefLists(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).Name == "ManagedBy" || !v.Type().Field(i).IsExported() {
				continue
			}
			canonicalRefLists(v.Field(i))
		}
	case reflect.Slice:
		if refs, ok := v.Interface().([]ResourceRef); ok {
			sort.SliceStable(refs, func(i, j int) bool {
				a, b := refs[i], refs[j]
				return a.Group+"/"+a.Kind+"/"+a.Namespace+"/"+a.Name < b.Group+"/"+b.Kind+"/"+b.Namespace+"/"+b.Name
			})
		}
	}
}

type charRootRecord struct {
	Root          string                `json:"root"`
	Relationships *Relationships        `json:"relationships"`
	ManagedBy     []ResourceRef         `json:"managedBy"`
	DeletePreview *CascadeDeletePreview `json:"deletePreview"`
}

type charNeighborhoodRecord struct {
	Root     string    `json:"root"`
	Profile  Profile   `json:"profile"`
	Hops     int       `json:"hops"`
	MaxNodes int       `json:"maxNodes"`
	Subgraph *Subgraph `json:"subgraph"`
}

// charNeighborhoodRoots are roots whose bounded neighborhoods depend on edge
// order (BFS budget selection), so a reorder that leaves every other answer
// alone still shows.
var charNeighborhoodRoots = []ResourceRef{
	{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"},
	{Kind: "Application", Group: "argoproj.io", Namespace: "argocd", Name: "shop"},
	{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Namespace: "flux-system", Name: "shop"},
	{Kind: "Pod", Namespace: "shop", Name: "web-7d9f8-0"},
	{Kind: "Node", Name: "node-a"},
	{Kind: "CronJob", Group: "batch", Namespace: "team", Name: "nightly"},
	{Kind: "GatewayClass", Group: "gateway.networking.k8s.io", Name: "envoy"},
}

func charRefKey(ref ResourceRef) string {
	return resourceid.ResourceKey(ref.Group, ref.Kind, ref.Namespace, ref.Name)
}

// charRoots records what production computes for every fixture object: the
// fetched-object Related Resources the drawer and AI context use (production
// passes the URL's plural kind), ManagedBy from the object's own metadata,
// and the delete preview.
func charRoots(f *charFixture, topo *Topology, idx *RelationshipsIndex) []charRootRecord {
	records := make([]charRootRecord, 0, len(f.objects))
	for _, o := range f.objects {
		ref := o.ref()
		rel := GetRelationshipsWithObject(o.resource, ref.Namespace, ref.Name, o.obj, topo, f.provider, f.dynamic, idx)
		preview := GetCascadeDeletePreview(ref, topo, f.dynamic)
		canonicalRefLists(reflect.ValueOf(rel))
		canonicalRefLists(reflect.ValueOf(preview))
		records = append(records, charRootRecord{
			Root:          o.key(),
			Relationships: rel,
			ManagedBy:     SynthesizeManagedBy(o.obj, o.kind, ref.Namespace, ref.Name, topo, f.dynamic, idx),
			DeletePreview: preview,
		})
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Root < records[j].Root })
	return records
}

func charNeighborhoods(topo *Topology, idx *RelationshipsIndex, dp DynamicProvider) []charNeighborhoodRecord {
	var records []charNeighborhoodRecord
	for _, root := range charNeighborhoodRoots {
		for _, opts := range []NeighborhoodOptions{{Profile: ProfileAuto, Hops: 1}, {Profile: ProfileAll, Hops: 2}, {Profile: ProfileAll, Hops: 1, MaxNodes: 2}} {
			sub := BuildNeighborhoodWithIndex(topo, root, opts, idx, dp)
			if sub != nil {
				// List order follows the builder's partly map-ordered edges;
				// membership (which nodes the budget admitted) is the answer.
				canonical := charGraph(&Topology{Nodes: sub.Nodes, Edges: sub.Edges})
				sub.Nodes, sub.Edges = canonical.Nodes, canonical.Edges
			}
			records = append(records, charNeighborhoodRecord{Root: charRefKey(root), Profile: opts.Profile, Hops: opts.Hops, MaxNodes: opts.MaxNodes, Subgraph: sub})
		}
	}
	return records
}

// charGraph is the topology exactly as serialized, with nodes and edges in a
// canonical order (the builder emits them partly in map order).
func charGraph(topo *Topology) Topology {
	graph := *topo
	graph.Nodes = append([]Node(nil), topo.Nodes...)
	graph.Edges = append([]Edge(nil), topo.Edges...)
	sort.SliceStable(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].ID < graph.Nodes[j].ID })
	sort.SliceStable(graph.Edges, func(i, j int) bool {
		a, b := graph.Edges[i], graph.Edges[j]
		return a.ID+"\x00"+a.Source+"\x00"+a.Target+"\x00"+string(a.Type) < b.ID+"\x00"+b.Source+"\x00"+b.Target+"\x00"+string(b.Type)
	})
	return graph
}

var charRefLine = regexp.MustCompile(`\{\s*"kind": ("[^"]*"),\s*"namespace": ("[^"]*"),\s*"name": ("[^"]*")(?:,\s*"group": ("[^"]*"))?\s*\}`)

// charJSON marshals for review: indented, with every ResourceRef on one line
// so an intentional change reads as the refs it changed.
func charJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out = charRefLine.ReplaceAllFunc(out, func(m []byte) []byte {
		parts := charRefLine.FindSubmatch(m)
		line := `{"kind": ` + string(parts[1]) + `, "namespace": ` + string(parts[2]) + `, "name": ` + string(parts[3])
		if len(parts[4]) > 0 {
			line += `, "group": ` + string(parts[4])
		}
		return []byte(line + "}")
	})
	return append(out, '\n')
}

func charBuild(t *testing.T, f *charFixture, opts BuildOptions) *Topology {
	t.Helper()
	topo, err := NewBuilder(f.provider).WithDynamic(f.dynamic).Build(opts)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return topo
}

func charRecord(t *testing.T, f *charFixture, opts BuildOptions) []byte {
	t.Helper()
	topo := charBuild(t, f, opts)
	return charSnapshotRecord(t, f, topo, IndexByResource(topo))
}

// charSnapshotRecord is everything a built topology answers through idx.
func charSnapshotRecord(t *testing.T, f *charFixture, topo *Topology, idx *RelationshipsIndex) []byte {
	t.Helper()
	return charJSON(t, struct {
		Graph         Topology                 `json:"graph"`
		Roots         []charRootRecord         `json:"roots"`
		Neighborhoods []charNeighborhoodRecord `json:"neighborhoods"`
	}{charGraph(topo), charRoots(f, topo, idx), charNeighborhoods(topo, idx, f.dynamic)})
}

// charProjection is a per-user projection as the REST drawer serves it: a
// struct copy with fresh slices (sharing everything else with the snapshot),
// stripped in place.
func charProjection(full *Topology, kinds map[NodeKind]bool, ids map[string]bool) *Topology {
	projected := *full
	projected.Nodes = append([]Node(nil), full.Nodes...)
	projected.Edges = append([]Edge(nil), full.Edges...)
	projected.StripNodeKinds(kinds)
	projected.StripNodeIDs(ids)
	return &projected
}

// charProjectionRecord is what a projection answers, queried without the
// shared index.
func charProjectionRecord(t *testing.T, f *charFixture, projected *Topology) []byte {
	t.Helper()
	return charJSON(t, struct {
		Graph Topology         `json:"graph"`
		Roots []charRootRecord `json:"roots"`
	}{charGraph(projected), charRoots(f, projected, nil)})
}

func charStrippedProjection(full *Topology) *Topology {
	return charProjection(full,
		map[NodeKind]bool{KindSecret: true, KindNodePool: true},
		map[string]bool{"application/argocd/shop": true, "replicaset/shop/web-7d9f8": true, "xdatabase//orders-db-xr/demo.example.io": true})
}

func charStrippedRecord(t *testing.T, f *charFixture) []byte {
	t.Helper()
	return charProjectionRecord(t, f, charStrippedProjection(charBuild(t, f, charSSEOptions())))
}

func charCompareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "characterization", name+".json")
	if *updateCharacterization {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update-characterization): %v", err)
	}
	if bytes.Equal(got, want) {
		return
	}
	gotLines, wantLines := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		var g, w string
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if g != w {
			t.Fatalf("%s: public topology answers changed at line %d\n  golden: %s\n  now:    %s\nregenerate with -update-characterization and justify every changed line", path, i+1, w, g)
		}
	}
}

func TestTopologyCharacterization(t *testing.T) {
	for _, tc := range charCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := charRecord(t, charCluster(), tc.opts())
			if again := charRecord(t, charCluster(), tc.opts()); !bytes.Equal(got, again) {
				t.Fatalf("fixture build is not deterministic for %s", tc.name)
			}
			charCompareGolden(t, tc.name, got)
		})
	}
	t.Run("sse-stripped-projection", func(t *testing.T) {
		charCompareGolden(t, "sse-stripped-projection", charStrippedRecord(t, charCluster()))
	})
}

// Builds must not change their inputs, and a second build from the same
// cached objects (as the SSE broadcaster does on every debounce) must answer
// exactly like the first, including after a projection was stripped.
func TestTopologyCharacterizationIsRepeatableOnSharedInputs(t *testing.T) {
	f := charCluster()
	before := charJSON(t, charInputs(f))
	first := charRecord(t, f, charSSEOptions())
	_ = charStrippedRecord(t, f)
	second := charRecord(t, f, charSSEOptions())
	if !bytes.Equal(first, second) {
		t.Fatal("a second build from the same objects answered differently")
	}
	if after := charJSON(t, charInputs(f)); !bytes.Equal(before, after) {
		t.Fatal("building or querying modified the fixture's objects")
	}
}

// Per-user projections share the snapshot's private state with the cached
// graph every other user reads. Making and querying two different projections
// must leave the snapshot answering exactly as before, through the index built
// before them and through a fresh one, and must leave each projection
// answering as it did on its own.
func TestTopologyCharacterizationProjectionsLeaveTheSnapshotIntact(t *testing.T) {
	f := charCluster()
	full := charBuild(t, f, charSSEOptions())
	idx := IndexByResource(full)
	before := charSnapshotRecord(t, f, full, idx)

	first := charStrippedProjection(full)
	firstAnswers := charProjectionRecord(t, f, first)
	second := charProjection(full,
		map[NodeKind]bool{KindDeployment: true, KindConfigMap: true},
		map[string]bool{"helmrelease/shop/redis": true, "kustomization/flux-system/shop": true, "job/team/nightly-29001": true})
	_ = charProjectionRecord(t, f, second)

	if again := charProjectionRecord(t, f, first); !bytes.Equal(firstAnswers, again) {
		t.Fatal("making a second projection changed what the first one answers")
	}
	if got := charSnapshotRecord(t, f, full, idx); !bytes.Equal(before, got) {
		t.Fatal("projections changed what the snapshot answers through its existing index")
	}
	if got := charSnapshotRecord(t, f, full, IndexByResource(full)); !bytes.Equal(before, got) {
		t.Fatal("projections changed what the snapshot answers through a fresh index")
	}
}

// The SSE broadcaster reuses one Builder for every client group, each with
// its own options; a build must not depend on the builds before it. Every
// build on the reused Builder, starting from a namespace-restricted one,
// must answer exactly like a fresh Builder given the same options.
func TestTopologyCharacterizationReusesOneBuilder(t *testing.T) {
	f := charCluster()
	b := NewBuilder(f.provider).WithDynamic(f.dynamic)
	restricted := charSSEOptions()
	restricted.Namespaces = []string{"shop"}
	sequence := []charCase{{"restricted relationship cache", func() BuildOptions { return restricted }}}
	sequence = append(sequence, charCases()...)
	sequence = append(sequence, sequence[0])
	for i, tc := range sequence {
		reused, err := b.Build(tc.opts())
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		got := charSnapshotRecord(t, f, reused, IndexByResource(reused))
		if want := charRecord(t, f, tc.opts()); !bytes.Equal(want, got) {
			t.Fatalf("build %d (%s) on a reused Builder answered differently from a fresh Builder", i, tc.name)
		}
	}
}

// One pair recorded twice stays two edges: the graph and Children keep the
// multiplicity, and nothing in the stack may collapse it silently.
func TestTopologyCharacterizationKeepsDuplicatePairs(t *testing.T) {
	f := charCluster()
	topo := charBuild(t, f, charSSEOptions())
	edges := 0
	for _, e := range topo.Edges {
		if e.Source == "cronjob/team/nightly" && e.Target == "job/team/nightly-28999" && e.Type == EdgeManages {
			edges++
		}
	}
	if edges != 2 {
		t.Fatalf("CronJob -> Job with two owner records: %d manages edges, want 2", edges)
	}
	rel := GetRelationships("cronjobs", "team", "nightly", topo, f.provider, f.dynamic)
	children := 0
	for _, child := range rel.Children {
		if child.Kind == "Job" && child.Name == "nightly-28999" {
			children++
		}
	}
	if children != 2 {
		t.Fatalf("CronJob Children list nightly-28999 %d times, want 2", children)
	}
}

func charInputs(f *charFixture) []any {
	inputs := make([]any, 0, len(f.objects))
	for _, o := range f.objects {
		inputs = append(inputs, o.obj)
	}
	return inputs
}

// Discovery order is informer order in production; answers must not depend on it.
func TestTopologyCharacterizationIgnoresWatchOrder(t *testing.T) {
	f := charCluster()
	want := charRecord(t, f, charSSEOptions())
	for i, j := 0, len(f.dynamic.watched)-1; i < j; i, j = i+1, j-1 {
		f.dynamic.watched[i], f.dynamic.watched[j] = f.dynamic.watched[j], f.dynamic.watched[i]
	}
	if got := charRecord(t, f, charSSEOptions()); !bytes.Equal(got, want) {
		t.Fatal("reversing the watched-resource order changed public answers")
	}
}

// charRoute is one manages-edge producer route and the exact edge it must
// emit in the named build. Node presence alone does not prove a route ran.
type charRoute struct {
	build, source, target string
	skipIfKindVisible     string
}

var charRoutes = []charRoute{
	{"sse-relationship-cache", "nodepool//default", "nodeclaim//default-x1", ""},
	{"sse-relationship-cache", "nodeclaim//default-x1", "node//node-b", ""},
	{"sse-relationship-cache", "capicluster/capi/prod", "kubeadmcontrolplane/capi/prod-cp", ""},
	{"sse-relationship-cache", "capicluster/capi/prod", "machinedeployment/capi/prod-md", ""},
	{"sse-relationship-cache", "capicluster/capi/prod", "machinepool/capi/prod-mp", ""},
	{"sse-relationship-cache", "machinedeployment/capi/prod-md", "machineset/capi/prod-md-abc", ""},
	{"sse-relationship-cache", "machineset/capi/prod-md-abc", "machine/capi/prod-md-abc-x", ""},
	{"sse-relationship-cache", "machine/capi/prod-md-abc-x", "node//node-a", ""},
	{"sse-relationship-cache", "cronworkflow/team/report", "workflow/team/report-1700", ""},
	{"sse-relationship-cache", "workflow/team/report-1700", "pod/team/report-1700-main", ""},
	{"sse-relationship-cache", "cronworkflow/team/report", "pod/team/report-1700-main", "Workflow"},
	{"sse-relationship-cache", "cronjob/team/nightly", "job/team/nightly-29001", ""},
	{"sse-relationship-cache", "job/team/nightly-29001", "pod/team/nightly-29001-x", ""},
	{"sse-relationship-cache", "cronjob/team/nightly", "pod/team/nightly-29001-x", "Job"},
	{"sse-relationship-cache", "scaledjob/team/queue-worker", "job/team/queue-worker-abc", ""},
	{"sse-relationship-cache", "scaledjob/team/queue-worker", "pod/team/queue-worker-abc-x", "Job"},
	{"sse-relationship-cache", "deployment/shop/web", "replicaset/shop/web-7d9f8", ""},
	{"sse-relationship-cache", "replicaset/shop/web-7d9f8", "pod/shop/web-7d9f8-0", ""},
	{"sse-relationship-cache", "rollout/shop/checkout", "replicaset/shop/checkout-abc", ""},
	{"sse-relationship-cache", "rollout/shop/checkout", "pod/shop/checkout-abc-a", "ReplicaSet"},
	{"sse-relationship-cache", "rollout/shop/checkout", "analysisrun/shop/checkout-bg-1", ""},
	{"sse-relationship-cache", "statefulset/team/db", "pod/team/db-0", ""},
	{"sse-relationship-cache", "daemonset/infra/agent", "pod/infra/agent-node-a", ""},
	{"sse-relationship-cache", "gatewayclass//envoy", "gateway/shop/public", ""},
	{"sse-relationship-cache", "sealedsecret/team/db-creds", "secret/team/db-creds", ""},
	{"sse-relationship-cache", "certificate/shop/web-tls", "secret/shop/web-tls", ""},
	{"sse-relationship-cache", "helmrelease/shop/redis", "deployment/shop/redis", ""},
	{"sse-relationship-cache", "helmrelease/shop/redis", "deployment/shop/redis-legacy", ""},
	{"sse-relationship-cache", "helmrelease/shop/redis", "service/shop/redis", ""},
	{"sse-relationship-cache", "helmrelease/shop/redis", "statefulset/shop/redis-replica", ""},
	{"sse-relationship-cache", "helmrelease/shop/redis", "daemonset/shop/redis-exporter", ""},
	{"sse-relationship-cache", "helmrelease/shop/redis", "job/shop/redis-migrate", ""},
	{"sse-relationship-cache", "helmrelease/shop/redis", "cronjob/shop/redis-backup", ""},
	{"sse-relationship-cache", "helmrelease/shop/redis", "rollout/shop/redis-proxy", ""},
	{"sse-relationship-cache", "application/argocd/shop", "deployment/shop/web", ""},
	{"sse-relationship-cache", "application/argocd/shop", "service/shop/web", ""},
	{"sse-relationship-cache", "kustomization/flux-system/shop", "deployment/shop/cart", ""},
	{"sse-relationship-cache", "knativeservice/shop/hello", "knativeconfiguration/shop/hello", ""},
	{"sse-relationship-cache", "knativeservice/shop/hello", "knativeroute/shop/hello", ""},
	{"sse-relationship-cache", "knativeconfiguration/shop/hello", "knativerevision/shop/hello-00001", ""},
	{"sse-relationship-cache", "knativerevision/shop/hello-00001", "deployment/shop/hello-00001-deployment", ""},
	{"sse-relationship-cache", "broker/shop/default", "inmemorychannel/shop/default-kne-trigger", ""},
	{"sse-relationship-cache", "deployment/shop/web", "widget/shop/web-widget/example.io", ""},
	{"sse-relationship-cache", "databaseclaim/shop/orders-db/demo.example.io", "xdatabase//orders-db-xr/demo.example.io", ""},
	{"sse-relationship-cache", "xdatabase//orders-db-xr/demo.example.io", "object//orders-db-config/kubernetes.crossplane.io", ""},
	// Hidden ReplicaSets: the grandparent links straight to the Pod.
	{"resources-default", "deployment/shop/web", "pod/shop/web-7d9f8-0", ""},
	{"resources-default", "rollout/shop/payments", "pod/shop/payments-6d8f-a", ""},
}

func TestTopologyCharacterizationExercisesEveryManagesRoute(t *testing.T) {
	builds := map[string]*Topology{}
	for _, tc := range charCases() {
		builds[tc.name] = charBuild(t, charCluster(), tc.opts())
	}
	for _, route := range charRoutes {
		matches := 0
		for _, edge := range builds[route.build].Edges {
			if edge.Source == route.source && edge.Target == route.target && edge.Type == EdgeManages && edge.SkipIfKindVisible == route.skipIfKindVisible {
				matches++
			}
		}
		if matches != 1 {
			t.Errorf("%s: want exactly one manages edge %s -> %s (skipIfKindVisible=%q), got %d", route.build, route.source, route.target, route.skipIfKindVisible, matches)
		}
	}
	grouped := false
	for _, node := range builds["sse-relationship-cache"].Nodes {
		grouped = grouped || node.Kind == KindPodGroup
	}
	if !grouped {
		t.Error("fixture no longer produces a grouped Pod tier")
	}
}

// Built-in roots must resolve through discovery exactly as in production; an
// unresolved Deployment preview would pin a path production never takes.
func TestTopologyCharacterizationResolvesBuiltinRoots(t *testing.T) {
	f := charCluster()
	topo := charBuild(t, f, charSSEOptions())
	for _, ref := range []ResourceRef{
		{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"},
		{Kind: "CronJob", Group: "batch", Namespace: "team", Name: "nightly"},
		{Kind: "StatefulSet", Group: "apps", Namespace: "team", Name: "db"},
	} {
		if preview := GetCascadeDeletePreview(ref, topo, f.dynamic); !preview.RootResolved {
			t.Errorf("%s/%s: delete preview root did not resolve", ref.Kind, ref.Name)
		}
	}
}
