package server

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// appEverySourceEvaluation evaluates every source of every family with an
// attachment field or in a composition, and joins every composition: the
// reference appRowEvaluation's rows must match.
func appEverySourceEvaluation(families []*relations.Family, compositions []*relations.Composition) appEvaluation {
	e := appEvaluation{sources: map[*relations.Family]map[resourceid.GroupKind]bool{}, compositions: compositions}
	for _, f := range families {
		needed := appNeedsDisposition(f.Projection)
		for _, c := range compositions {
			needed = needed || f == c.Producer || slices.Contains(c.Consumers, f)
		}
		if !needed {
			continue
		}
		for _, source := range f.Sources {
			e.evaluate(f, source.Kind)
		}
	}
	return e
}

func (e appEvaluation) clone() appEvaluation {
	out := appEvaluation{sources: map[*relations.Family]map[resourceid.GroupKind]bool{}, compositions: slices.Clone(e.compositions)}
	for f, sources := range e.sources {
		out.sources[f] = maps.Clone(sources)
	}
	return out
}

type appEvaluatedPair struct {
	family *relations.Family
	source resourceid.GroupKind
}

func (p appEvaluatedPair) String() string { return p.family.Name + " × " + p.source.String() }

func (e appEvaluation) pairs(families []*relations.Family) []appEvaluatedPair {
	var out []appEvaluatedPair
	for _, f := range families {
		for _, s := range f.Sources {
			if e.sources[f][s.Kind] {
				out = append(out, appEvaluatedPair{f, s.Kind})
			}
		}
	}
	return out
}

// appPruningFixture holds, for every (family, source) pair the batch could
// evaluate, an object that declares a target present in the cluster, aimed
// where possible at what the rows hold: the workloads, the Service api they
// share, and the objects those mount.
func appPruningFixture() (typed []runtime.Object, dynamic []*unstructured.Unstructured) {
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "prod", Name: name} }
	controller := true
	mounts := func(configMap string, more ...corev1.Volume) corev1.PodSpec {
		return corev1.PodSpec{Volumes: append([]corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMap}}}}}, more...)}
	}
	labels := map[string]string{"app": "api"}
	api := mounts("api-config",
		corev1.Volume{Name: "sealed", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "sealed"}}},
		corev1.Volume{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "api-data"}}},
	)
	api.ServiceAccountName = "api"
	api.Containers = []corev1.Container{{Name: "api", Env: []corev1.EnvVar{{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-flags"}, Key: "mode"}}}}}}
	snapGroup := "snapshot.storage.k8s.io"
	typed = append(testNamespaces("prod"),
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api", UID: "api"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: api}}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api-1", UID: "api-1", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", UID: "api", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: mounts("api-config")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api-1-a", Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-1", UID: "api-1", Controller: &controller}}}, Spec: mounts("api-config")},
		&appsv1.StatefulSet{ObjectMeta: meta("db"), Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: mounts("db-config")}}},
		&appsv1.DaemonSet{ObjectMeta: meta("agent"), Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: mounts("agent-config")}}},
		&batchv1.CronJob{ObjectMeta: meta("nightly"), Spec: batchv1.CronJobSpec{Schedule: "@daily", JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: mounts("nightly-config")}}}}},
		&batchv1.Job{ObjectMeta: meta("migrate"), Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: mounts("migrate-config")}}},
		&corev1.Service{ObjectMeta: meta("api"), Spec: corev1.ServiceSpec{Selector: labels}},
		&corev1.Service{ObjectMeta: meta("web-stable")},
		&networkingv1.Ingress{ObjectMeta: meta("edge"), Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{SecretName: "edge-tls"}}, DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api", Port: networkingv1.ServiceBackendPort{Number: 80}}}}},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("api"), Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}}},
		&corev1.ServiceAccount{ObjectMeta: meta("api"), Secrets: []corev1.ObjectReference{{Name: "api-token"}}, ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}}},
		&corev1.PersistentVolumeClaim{ObjectMeta: meta("api-data")},
		&corev1.PersistentVolumeClaim{ObjectMeta: meta("api-restore"), Spec: corev1.PersistentVolumeClaimSpec{DataSource: &corev1.TypedLocalObjectReference{APIGroup: &snapGroup, Kind: "VolumeSnapshot", Name: "daily"}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "mirror-tls", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "prod/edge-tls"}}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "mirror-config", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "prod/api-config"}}},
	)
	for _, name := range []string{"api-config", "api-flags", "db-config", "agent-config", "nightly-config", "migrate-config", "web-config", "transport-ca"} {
		typed = append(typed, &corev1.ConfigMap{ObjectMeta: meta(name)})
	}
	for _, name := range []string{"sealed", "edge-tls", "cert-tls", "transport-secret", "client-ca", "store-tls", "contour-ca", "api-token", "registry"} {
		typed = append(typed, &corev1.Secret{ObjectMeta: meta(name)})
	}

	object := func(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"namespace": "prod", "name": name}, "spec": spec}}
	}
	cluster := func(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
		u := object(apiVersion, kind, name, spec)
		unstructured.RemoveNestedField(u.Object, "metadata", "namespace")
		return u
	}
	withStatus := func(u *unstructured.Unstructured, status map[string]any) *unstructured.Unstructured {
		u.Object["status"] = status
		return u
	}
	toAPI := []any{map[string]any{"name": "api", "port": int64(80)}}
	toService := map[string]any{"ref": map[string]any{"apiVersion": "v1", "kind": "Service", "name": "api"}}
	dynamic = []*unstructured.Unstructured{
		object("argoproj.io/v1alpha1", "Rollout", "web", map[string]any{
			"strategy": map[string]any{"canary": map[string]any{"stableService": "web-stable"}},
			"template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"name": "config", "configMap": map[string]any{"name": "web-config"}}}}},
		}),
		object("bitnami.com/v1alpha1", "SealedSecret", "sealed", map[string]any{}),
		object("autoscaling.k8s.io/v1", "VerticalPodAutoscaler", "db", map[string]any{"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "db"}}),
		object("keda.sh/v1alpha1", "ScaledObject", "web", map[string]any{
			"scaleTargetRef": map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "name": "web"},
			"triggers":       []any{map[string]any{"type": "rabbitmq", "authenticationRef": map[string]any{"name": "web-auth"}}},
		}),
		object("keda.sh/v1alpha1", "ScaledJob", "queue", map[string]any{"triggers": []any{map[string]any{"type": "rabbitmq", "authenticationRef": map[string]any{"name": "queue-auth"}}}}),
		object("keda.sh/v1alpha1", "TriggerAuthentication", "web-auth", nil),
		object("keda.sh/v1alpha1", "TriggerAuthentication", "queue-auth", nil),
		object("gateway.networking.k8s.io/v1", "Gateway", "gateway", map[string]any{}),
		object("networking.istio.io/v1", "VirtualService", "istio", map[string]any{"gateways": []any{"istio-gateway"}, "http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "api"}}}}}}),
		object("networking.istio.io/v1", "Gateway", "istio-gateway", map[string]any{}),
		object("networking.istio.io/v1", "DestinationRule", "istio", map[string]any{"host": "api"}),
		object("projectcontour.io/v1", "HTTPProxy", "contour", map[string]any{
			"virtualhost": map[string]any{"fqdn": "api.example", "tls": map[string]any{"secretName": "cert-tls", "clientValidation": map[string]any{"caSecret": "contour-ca"}}},
			"routes":      []any{map[string]any{"services": toAPI}},
		}),
		object("eventing.knative.dev/v1", "Trigger", "orders", map[string]any{"broker": "default", "subscriber": toService}),
		object("eventing.knative.dev/v1", "Broker", "default", map[string]any{}),
		withStatus(object("messaging.knative.dev/v1", "Channel", "orders", map[string]any{}), map[string]any{"channel": map[string]any{"apiVersion": "messaging.knative.dev/v1", "kind": "InMemoryChannel", "name": "orders"}}),
		withStatus(object("messaging.knative.dev/v1", "InMemoryChannel", "orders", map[string]any{}), map[string]any{"channel": map[string]any{"apiVersion": "v1", "kind": "Service", "name": "api"}}),
		withStatus(object("serving.knative.dev/v1", "Route", "handler", map[string]any{}), map[string]any{"traffic": []any{map[string]any{"revisionName": "handler-00001", "percent": int64(100)}}}),
		object("serving.knative.dev/v1", "Revision", "handler-00001", map[string]any{}),
		object("cert-manager.io/v1", "Certificate", "cert", map[string]any{"secretName": "cert-tls", "issuerRef": map[string]any{"name": "issuer"}}),
		object("cert-manager.io/v1", "CertificateRequest", "cert-1", map[string]any{"issuerRef": map[string]any{"name": "issuer"}}),
		object("acme.cert-manager.io/v1", "Order", "cert-1", map[string]any{"issuerRef": map[string]any{"name": "issuer"}}),
		object("acme.cert-manager.io/v1", "Challenge", "cert-1", map[string]any{"issuerRef": map[string]any{"name": "issuer"}}),
		object("cert-manager.io/v1", "Issuer", "issuer", map[string]any{}),
		object("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "apps", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}),
		object("helm.toolkit.fluxcd.io/v2", "HelmRelease", "chart", map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "charts"}}}}),
		object("source.toolkit.fluxcd.io/v1", "HelmChart", "prod-chart", map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "charts"}}),
		object("source.toolkit.fluxcd.io/v1", "GitRepository", "repo", map[string]any{}),
		object("source.toolkit.fluxcd.io/v1", "HelmRepository", "charts", map[string]any{}),
		object("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "bundle", map[string]any{"sources": []any{map[string]any{"alias": "repo", "kind": "GitRepository", "name": "repo"}}}),
		object("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "bundle-app", map[string]any{"sourceRef": map[string]any{"apiVersion": "source.extensions.fluxcd.io/v1beta1", "kind": "ArtifactGenerator", "name": "bundle"}}),
		cluster("karpenter.sh/v1", "NodePool", "general", map[string]any{"template": map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default"}}}}),
		cluster("karpenter.k8s.aws/v1", "EC2NodeClass", "default", map[string]any{}),
		object("cluster.x-k8s.io/v1beta2", "Cluster", "fleet", map[string]any{"topology": map[string]any{"classRef": map[string]any{"name": "quick-start"}}}),
		object("cluster.x-k8s.io/v1beta2", "ClusterClass", "quick-start", map[string]any{}),
		object("argoproj.io/v1alpha1", "Workflow", "release", map[string]any{"templates": []any{map[string]any{"name": "main", "steps": []any{[]any{map[string]any{"name": "build", "templateRef": map[string]any{"name": "build"}}}}}}}),
		object("argoproj.io/v1alpha1", "CronWorkflow", "nightly", map[string]any{"workflowSpec": map[string]any{"templates": []any{map[string]any{"name": "main", "steps": []any{[]any{map[string]any{"name": "tell", "templateRef": map[string]any{"name": "notify", "clusterScope": true}}}}}}}}),
		object("argoproj.io/v1alpha1", "WorkflowTemplate", "build", map[string]any{}),
		cluster("argoproj.io/v1alpha1", "ClusterWorkflowTemplate", "notify", map[string]any{}),
		cluster("snapshot.storage.k8s.io/v1", "VolumeSnapshotClass", "csi-snapclass", map[string]any{}),
		cluster("snapshot.storage.k8s.io/v1", "VolumeSnapshotContent", "snapcontent-1", map[string]any{
			"volumeSnapshotClassName": "csi-snapclass",
			"volumeSnapshotRef":       map[string]any{"namespace": "prod", "name": "daily", "uid": "uid-1"},
		}),
		withStatus(func() *unstructured.Unstructured {
			u := object("snapshot.storage.k8s.io/v1", "VolumeSnapshot", "daily", map[string]any{
				"volumeSnapshotClassName": "csi-snapclass",
				"source":                  map[string]any{"persistentVolumeClaimName": "api-data"},
			})
			u.SetUID("uid-1")
			return u
		}(), map[string]any{"boundVolumeSnapshotContentName": "snapcontent-1"}),
	}
	for _, kind := range []string{"HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute", "UDPRoute"} {
		dynamic = append(dynamic, object("gateway.networking.k8s.io/v1", kind, strings.ToLower(kind), map[string]any{"parentRefs": []any{map[string]any{"name": "gateway"}}, "rules": []any{map[string]any{"backendRefs": toAPI}}}))
	}
	for _, sources := range []string{"PingSource", "ApiServerSource", "ContainerSource", "SinkBinding"} {
		dynamic = append(dynamic, object("sources.knative.dev/v1", sources, strings.ToLower(sources), map[string]any{"sink": toService}))
	}
	for _, group := range []string{"traefik.io", "traefik.containo.us"} {
		v := group + "/v1alpha1"
		suffix := strings.SplitN(group, ".", 2)[1]
		dynamic = append(dynamic,
			object(v, "IngressRoute", "route-"+suffix, map[string]any{
				"routes": []any{map[string]any{"services": []any{
					map[string]any{"name": "api", "port": int64(80), "serversTransport": "transport"},
					map[string]any{"kind": "TraefikService", "name": "split"},
				}, "middlewares": []any{map[string]any{"name": "chain"}}}},
				"tls": map[string]any{"secretName": "cert-tls", "options": map[string]any{"name": "options"}},
			}),
			object(v, "IngressRouteTCP", "tcp-"+suffix, map[string]any{
				"routes": []any{map[string]any{"services": []any{map[string]any{"name": "api", "port": int64(80), "serversTransport": "transport-tcp"}}, "middlewares": []any{map[string]any{"name": "tcp"}}}},
				"tls":    map[string]any{"secretName": "cert-tls", "options": map[string]any{"name": "options"}},
			}),
			object(v, "IngressRouteUDP", "udp-"+suffix, map[string]any{"routes": []any{map[string]any{"services": toAPI}}}),
			object(v, "TraefikService", "split", map[string]any{"weighted": map[string]any{"services": toAPI}}),
			object(v, "Middleware", "chain", map[string]any{"chain": map[string]any{"middlewares": []any{map[string]any{"name": "leaf"}}}}),
			object(v, "Middleware", "leaf", map[string]any{}),
			object(v, "MiddlewareTCP", "tcp", map[string]any{}),
			object(v, "ServersTransport", "transport", map[string]any{"rootCAs": []any{map[string]any{"configMap": "transport-ca"}}, "certificatesSecrets": []any{"transport-secret"}}),
			object(v, "ServersTransportTCP", "transport-tcp", map[string]any{"tls": map[string]any{"rootCAsSecrets": []any{"transport-secret"}}}),
			object(v, "TLSOption", "options", map[string]any{"clientAuth": map[string]any{"secretNames": []any{"client-ca"}}}),
			object(v, "TLSStore", "default", map[string]any{"defaultCertificate": map[string]any{"secretName": "store-tls"}}),
		)
	}
	return typed, dynamic
}

// appFixturePlural is the resource a fixture kind is served as.
func appFixturePlural(kind string) string {
	lower := strings.ToLower(kind)
	switch {
	case strings.HasSuffix(lower, "y") && !strings.HasSuffix(lower, "ay"):
		return strings.TrimSuffix(lower, "y") + "ies"
	case strings.HasSuffix(lower, "s"):
		return lower + "es"
	default:
		return lower + "s"
	}
}

// Each family source the batch could evaluate either fills something a row
// reads, and is evaluated, or provably doesn't: the fixture holds an object
// for every pair that declares a present target, removing an evaluated pair
// changes the rows, and adding back a skipped one doesn't.
// useAppFixtureCaches serves typed and dynamic as the cluster, every kind
// watched and loaded.
func useAppFixtureCaches(t *testing.T, typed []runtime.Object, dynamicObjects []*unstructured.Unstructured) {
	t.Helper()
	useTestResourceCache(t, fake.NewClientset(typed...))
	kinds := map[schema.GroupVersionResource]string{}
	var resources []k8s.APIResource
	for _, u := range dynamicObjects {
		gvr := u.GroupVersionKind().GroupVersion().WithResource(appFixturePlural(u.GetKind()))
		if _, ok := kinds[gvr]; ok {
			continue
		}
		kinds[gvr] = u.GetKind() + "List"
		resources = append(resources, k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: u.GetKind(), Namespaced: u.GetNamespace() != "", IsCRD: true, Verbs: []string{"get", "list", "watch"}})
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds)
	for _, u := range dynamicObjects {
		if err := client.Tracker().Create(u.GroupVersionKind().GroupVersion().WithResource(appFixturePlural(u.GetKind())), u, u.GetNamespace()); err != nil {
			t.Fatal(err)
		}
	}
	if err := k8s.InitTestDynamicResourceCache(client, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	addApplicationTypedDiscovery()
	dc := k8s.GetDynamicResourceCache()
	for gvr := range kinds {
		if err := dc.EnsureWatching(gvr); err != nil {
			t.Fatalf("watch %v: %v", gvr, err)
		}
	}
	for gvr := range kinds {
		if !dc.WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("watch %v did not sync", gvr)
		}
	}
}

func TestApplicationsBatchEvaluatesOnlyWhatRowsRead(t *testing.T) {
	typed, dynamicObjects := appPruningFixture()
	useAppFixtureCaches(t, typed, dynamicObjects)

	cache := k8s.GetResourceCache()
	session, err := k8s.BeginOwnershipRead(cache)
	if err != nil {
		t.Fatal(err)
	}
	families := relations.Families("cluster.local")
	compositions := relations.Compositions()
	every := appEverySourceEvaluation(families, compositions)
	pruned := appRowEvaluation(families, compositions)

	// Every pair the batch could evaluate has an object declaring a present
	// target, and every composition a producer and a consumer that name the
	// same object, so each comparison below has something to compare.
	scope := ownership.SearchScope{Namespaces: []string{"prod"}, IncludeClusterScoped: true}
	view := relations.NewView(session.(relations.Session), ownership.Unrestricted()).WithLimit(0)
	declared := func(f *relations.Family, source resourceid.GroupKind) map[resourceid.Ref]bool {
		out := map[resourceid.Ref]bool{}
		for _, o := range session.(relations.Session).Indexed(source, "", nil, scope).Objects {
			for _, d := range view.References(f, o.Ref, o.Object).Declared {
				if d.State == relations.TargetPresent {
					out[d.Ref] = true
				}
			}
		}
		return out
	}
	for _, pair := range every.pairs(families) {
		if len(declared(pair.family, pair.source)) == 0 {
			t.Errorf("%v: no fixture object declares a present target", pair)
		}
	}
	for _, c := range every.compositions {
		produced := map[resourceid.Ref]bool{}
		for _, s := range c.Producer.Sources {
			maps.Copy(produced, declared(c.Producer, s.Kind))
		}
		joined := false
		for _, family := range c.Consumers {
			for _, s := range family.Sources {
				if len(c.ConsumerKinds) > 0 && !slices.Contains(c.ConsumerKinds, s.Kind) {
					continue
				}
				for ref := range declared(family, s.Kind) {
					joined = joined || produced[ref]
				}
			}
		}
		if !joined {
			t.Errorf("composition %s: no fixture producer and consumer name the same object", c.Name)
		}
	}

	rows := func(e appEvaluation) []appRow {
		s, err := k8s.BeginOwnershipRead(cache)
		if err != nil {
			t.Fatal(err)
		}
		v := ownership.NewView(s, ownership.Unrestricted())
		// The batch builds its own families (Istio's hosts per mesh domain), so
		// the plan names them by name.
		plan := func(batchFamilies []*relations.Family, _ []*relations.Composition) appEvaluation {
			out := appEvaluation{sources: map[*relations.Family]map[resourceid.GroupKind]bool{}, compositions: e.compositions}
			for _, bf := range batchFamilies {
				for f, sources := range e.sources {
					if f.Name == bf.Name {
						out.sources[bf] = sources
					}
				}
			}
			return out
		}
		inventory := &applicationInventory{batch: buildPlannedAppRelationBatch(context.Background(), s.(relations.Session), []string{"prod"}, plan), session: s.(relations.Session), owners: v}
		return groupApplications(collectAppWorkloads(context.Background(), cache, []string{"prod"}, inventory, v, false))
	}
	want := rows(pruned)
	if got := rows(every); !reflect.DeepEqual(got, want) {
		t.Errorf("rows differ between every source and the pruned plan:\n%s", appRowsDiff(want, got))
	}
	for _, pair := range every.pairs(families) {
		e := pruned.clone()
		if pruned.sources[pair.family][pair.source] {
			delete(e.sources[pair.family], pair.source)
			if reflect.DeepEqual(rows(e), want) {
				t.Errorf("%v is evaluated, but the rows don't change without it", pair)
			}
			continue
		}
		e.evaluate(pair.family, pair.source)
		if got := rows(e); !reflect.DeepEqual(got, want) {
			t.Errorf("%v is skipped, but evaluating it changes the rows:\n%s", pair, appRowsDiff(want, got))
		}
	}
	for _, c := range every.compositions {
		e := pruned.clone()
		if slices.Contains(pruned.compositions, c) {
			e.compositions = slices.DeleteFunc(e.compositions, func(x *relations.Composition) bool { return x == c })
			if reflect.DeepEqual(rows(e), want) {
				t.Errorf("composition %s is joined, but the rows don't change without it", c.Name)
			}
			continue
		}
		e.compositions = append(e.compositions, c)
		for _, s := range c.Producer.Sources {
			e.evaluate(c.Producer, s.Kind)
		}
		for _, family := range c.Consumers {
			for _, s := range family.Sources {
				e.evaluate(family, s.Kind)
			}
		}
		if got := rows(e); !reflect.DeepEqual(got, want) {
			t.Errorf("composition %s is skipped, but joining it changes the rows:\n%s", c.Name, appRowsDiff(want, got))
		}
	}
}

func appRowsDiff(want, got []appRow) string {
	var out []string
	for i := range max(len(want), len(got)) {
		var w, g appRow
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		if !reflect.DeepEqual(w, g) {
			out = append(out, fmt.Sprintf("row %d: want %+v rels %+v\n       got %+v rels %+v", i, w.Key, w.Relationships, g.Key, g.Relationships))
		}
	}
	return strings.Join(out, "\n")
}

type appIndexedRead struct {
	kind  resourceid.GroupKind
	index string
	keys  []string
	all   bool
}

// appIndexRecorder records each indexed read; unindexed reads every store in
// full, as the batch once did, so the two can be compared.
type appIndexRecorder struct {
	relations.Session
	reads     *[]appIndexedRead
	unindexed bool
}

func (s appIndexRecorder) Indexed(kind resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	*s.reads = append(*s.reads, appIndexedRead{kind, index, keys, scope.AllNamespaces})
	if s.unindexed && index == cache.NamespaceIndex {
		index, keys = "", nil
	}
	return s.Session.Indexed(kind, index, keys, scope)
}

// A request naming namespaces looks namespaced kinds up in the namespace
// index rather than scanning cluster-wide stores, and reads the same rows: a
// cluster-scoped kind and the mesh domain's Deployments are still read whole.
func TestApplicationsBatchReadsNamedNamespacesThroughTheIndex(t *testing.T) {
	typed, dynamicObjects := appPruningFixture()
	labels := map[string]string{"app": "api"}
	typed = append(typed, testNamespaces("other")...)
	typed = append(typed,
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "api"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}}}}}}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "api"}, Spec: corev1.ServiceSpec{Selector: labels}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "api-config"}},
	)
	dynamicObjects = append(dynamicObjects, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "projectcalico.org/v3", "kind": "GlobalNetworkPolicy", "metadata": map[string]any{"name": "global"},
		"spec": map[string]any{"selector": "app == 'api'"},
	}})
	useAppFixtureCaches(t, typed, dynamicObjects)
	rc := k8s.GetResourceCache()

	build := func(unindexed bool) ([]appRow, []appIndexedRead) {
		session, err := k8s.BeginOwnershipRead(rc)
		if err != nil {
			t.Fatal(err)
		}
		var reads []appIndexedRead
		recorder := appIndexRecorder{session.(relations.Session), &reads, unindexed}
		view := ownership.NewView(session, ownership.Unrestricted())
		inventory := &applicationInventory{batch: buildAppRelationBatch(context.Background(), recorder, []string{"prod"}), session: session.(relations.Session), owners: view}
		return groupApplications(collectAppWorkloads(context.Background(), rc, []string{"prod"}, inventory, view, false)), reads
	}
	indexed, reads := build(false)
	whole, _ := build(true)
	if !reflect.DeepEqual(indexed, whole) {
		t.Fatalf("rows differ between indexed and whole-store reads:\n%s", appRowsDiff(whole, indexed))
	}
	if api := rowByName(indexed, "api"); api == nil || api.Relationships == nil || api.Relationships.NetworkPolicies != 1 || len(api.Relationships.ServiceRefs) != 1 || api.Namespace != "prod" {
		t.Fatalf("fixture rows don't exercise a scoped and a cluster-scoped read: %+v", api)
	}

	session, err := k8s.BeginOwnershipRead(rc)
	if err != nil {
		t.Fatal(err)
	}
	global := resourceid.GroupKind{Group: "projectcalico.org", Kind: "GlobalNetworkPolicy"}
	sawGlobal, sawMesh, indexedKinds := false, false, 0
	for _, r := range reads {
		namespaced, known := session.Catalog().Scope(r.kind)
		switch {
		case r.all:
			sawMesh = sawMesh || r.kind == resourceid.GroupKind{Group: "apps", Kind: "Deployment"}
			if r.index != "" {
				t.Errorf("all-namespace read of %v used index %q", r.kind, r.index)
			}
		case known && namespaced:
			indexedKinds++
			if r.index != cache.NamespaceIndex || !slices.Equal(r.keys, []string{"prod"}) {
				t.Errorf("%v read with index %q keys %v, want the namespace index for prod", r.kind, r.index, r.keys)
			}
		default:
			sawGlobal = sawGlobal || r.kind == global
			if r.index != "" {
				t.Errorf("%v (cluster-scoped or not served) read through index %q", r.kind, r.index)
			}
		}
	}
	if !sawGlobal || !sawMesh || indexedKinds == 0 {
		t.Fatalf("reads missed the cluster-scoped policy (%v), the mesh domain's Deployments (%v) or any namespaced kind (%d): %+v", sawGlobal, sawMesh, indexedKinds, reads)
	}
}
