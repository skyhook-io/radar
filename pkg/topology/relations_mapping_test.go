package topology

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func projectionOwnedAnswer(t *testing.T, s *projectionSession, obj runtime.Object, access ownership.Access) *Relationships {
	t.Helper()
	captured, ok := relations.CaptureSubject(s, projectionRef(obj), obj)
	if !ok {
		t.Fatal("capture failed")
	}
	ov := ownership.NewView(captured, access)
	rel, _ := answerWithCoverage(captured, access, access, ov, projectionRef(obj), obj, nil, nil)
	m, _ := apimeta.Accessor(obj)
	ApplyOwnership(rel, ov, ownership.Observe(projectionRef(obj), m))
	return rel
}

// readableRefs leaves out references listed by name only, marked unreadable.
func readableRefs(refs []ResourceRef) []ResourceRef {
	return slices.DeleteFunc(slices.Clone(refs), func(r ResourceRef) bool { return r.State != "" })
}

func requireProjectionRefs(t *testing.T, got []ResourceRef, want ...runtime.Object) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("refs = %+v; want %d", got, len(want))
	}
	for _, o := range want {
		if !slices.Contains(got, refOf(projectionRef(o))) {
			t.Fatalf("refs = %+v; missing %v", got, projectionRef(o))
		}
	}
}
func TestRelationsAnswerCertificateRoutes(t *testing.T) {
	cert := projectionCR("cert-manager.io", "Certificate", "shop", "cert", map[string]any{"spec": map[string]any{"secretName": "tls"}})
	contour := projectionCR("projectcontour.io", "HTTPProxy", "edge", "front", map[string]any{"spec": map[string]any{"virtualhost": map[string]any{"tls": map[string]any{"secretName": "shop/tls"}}}})
	for _, group := range []string{"traefik.io", "traefik.containo.us"} {
		t.Run(group, func(t *testing.T) {
			route := projectionCR(group, "IngressRoute", "shop", "front", map[string]any{"spec": map[string]any{"tls": map[string]any{"secretName": "tls"}}})
			transport := projectionCR(group, "ServersTransport", "shop", "transport", map[string]any{"spec": map[string]any{"certificatesSecrets": []any{"tls"}}})
			s := newProjectionSession(t, cert, contour, route, transport)
			access := projectionAccess{denied: []string{"Secret"}}
			requireProjectionRefs(t, projectionAnswer(t, s, cert, access, access).Consumers, route, contour)
			requireProjectionRefs(t, projectionAnswer(t, s, route, access, access).ConfigRefs, cert)
			requireProjectionRefs(t, projectionAnswer(t, s, contour, access, access).ConfigRefs, cert)
			requireProjectionRefs(t, projectionAnswer(t, s, transport, access, access).ConfigRefs)
			requireProjectionRefs(t, projectionAnswer(t, s, cert, projectionAccess{namespaces: []string{"shop"}, denied: []string{"Secret"}}, access).Consumers, route)
			requireProjectionRefs(t, projectionAnswer(t, s, route, projectionAccess{denied: []string{"Certificate", "Secret"}}, access).ConfigRefs)
			for _, r := range s.reads {
				if r.Kind == "Secret" {
					t.Fatalf("borrowed logical Secret: %v", r)
				}
			}
		})
	}
}
func TestRelationsAnswerMappedDirections(t *testing.T) {
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "stable"}}, "", "Service")
	rollout := projectionCR("argoproj.io", "Rollout", "shop", "web", map[string]any{"spec": map[string]any{"strategy": map[string]any{"canary": map[string]any{"stableService": "stable"}}}})
	class := projectionCR("gateway.networking.k8s.io", "GatewayClass", "", "implementation", nil)
	gw := projectionCR("gateway.networking.k8s.io", "Gateway", "shop", "gateway", map[string]any{"spec": map[string]any{"gatewayClassName": "implementation"}})
	route := projectionCR("gateway.networking.k8s.io", "HTTPRoute", "shop", "route", map[string]any{"spec": map[string]any{"parentRefs": []any{map[string]any{"name": "gateway"}}}})
	istio := projectionCR("networking.istio.io", "Gateway", "shop", "istio", nil)
	vs := projectionCR("networking.istio.io", "VirtualService", "shop", "vs", map[string]any{"spec": map[string]any{"gateways": []any{"istio"}}})
	broker := projectionCR("eventing.knative.dev", "Broker", "shop", "broker", nil)
	trigger := projectionCR("eventing.knative.dev", "Trigger", "shop", "trigger", map[string]any{"spec": map[string]any{"broker": "broker"}})
	cluster := projectionCR("cluster.x-k8s.io", "Cluster", "shop", "cluster", nil)
	cluster.SetUID("cluster-uid")
	mhc := projectionCR("cluster.x-k8s.io", "MachineHealthCheck", "shop", "check", nil)
	mhc.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "cluster.x-k8s.io/v1", Kind: "Cluster", Name: cluster.GetName(), UID: cluster.GetUID()}})
	s := newProjectionSession(t, svc, rollout, class, gw, route, istio, vs, broker, trigger, cluster, mhc)
	a := ownership.Unrestricted()
	requireProjectionRefs(t, projectionAnswer(t, s, rollout, a, a).Services, svc)
	requireProjectionRefs(t, projectionAnswer(t, s, svc, a, a).Backends, rollout)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, class, a).Manages, gw)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, gw, a).Managers, class)
	requireProjectionRefs(t, projectionAnswer(t, s, gw, a, a).Backends, route)
	requireProjectionRefs(t, projectionAnswer(t, s, route, a, a).Gateways, gw)
	requireProjectionRefs(t, projectionAnswer(t, s, istio, a, a).Backends, vs)
	requireProjectionRefs(t, projectionAnswer(t, s, vs, a, a).RoutedFrom, istio)
	requireProjectionRefs(t, projectionAnswer(t, s, broker, a, a).Backends, trigger)
	requireProjectionRefs(t, projectionAnswer(t, s, trigger, a, a).RoutedFrom, broker)
	requireProjectionRefs(t, projectionAnswer(t, s, mhc, a, a).Dependencies, cluster)
	requireProjectionRefs(t, projectionAnswer(t, s, cluster, a, a).HealthChecks, mhc)
}
func TestRelationsAnswerManagementFallbacks(t *testing.T) {
	hr := projectionCR("helm.toolkit.fluxcd.io", "HelmRelease", "flux", "release", nil)
	d := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "app", Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "release", "helm.toolkit.fluxcd.io/namespace": "flux"}}}, "apps", "Deployment")
	cron := projectionCR("argoproj.io", "CronWorkflow", "shop", "cron", nil)
	wf := projectionCR("argoproj.io", "Workflow", "shop", "run", nil)
	wf.SetLabels(map[string]string{"workflows.argoproj.io/cron-workflow": "cron"})
	a := ownership.Unrestricted()
	s := newProjectionSession(t, hr, d, cron, wf)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, hr, a).Manages, d)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, d, a).Managers, hr)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, cron, a).Manages, wf)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, wf, a).Managers, cron)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, hr, projectionAccess{namespaces: []string{"flux"}}).Manages)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, d, projectionAccess{denied: []string{"HelmRelease"}}).Managers)
	for _, fields := range []map[string]any{
		{"status": map[string]any{"inventory": map[string]any{"entries": []any{}}}},
		{"spec": map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "remote"}}}},
	} {
		release := projectionCR(hr.GroupVersionKind().Group, "HelmRelease", "flux", "release", fields)
		s = newProjectionSession(t, release, d)
		requireProjectionRefs(t, projectionOwnedAnswer(t, s, release, a).Manages)
		requireProjectionRefs(t, projectionOwnedAnswer(t, s, d, a).Managers)
	}
	hook := d.(*appsv1.Deployment).DeepCopy()
	hook.Annotations = map[string]string{"helm.sh/hook": "pre-install"}
	s = newProjectionSession(t, hr, hook)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, hr, a).Manages)
}

func TestRelationsAnswerSealedSecretConsumers(t *testing.T) {
	sealed := projectionCR("bitnami.com", "SealedSecret", "shop", "db", map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"name": "ignored"}}}})
	d := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "app"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "db", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db"}}}}}}}}, "apps", "Deployment")
	wrong := d.(*appsv1.Deployment).DeepCopy()
	wrong.Name = "wrong"
	wrong.Spec.Template.Spec.Volumes[0].Secret.SecretName = "ignored"
	secret := projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db"}}, "", "Secret")
	access := projectionAccess{denied: []string{"Secret"}}
	for _, traverse := range []ownership.Access{access, ownership.Unrestricted()} {
		var prior []byte
		for _, present := range []bool{false, true} {
			objects := []runtime.Object{sealed, d, wrong}
			if present {
				objects = append(objects, secret)
			}
			s := newProjectionSession(t, objects...)
			var answers []any
			for _, obj := range []runtime.Object{sealed, d} {
				captured, _ := relations.CaptureSubject(s, projectionRef(obj), obj)
				ov := ownership.NewView(captured, access)
				rel, cov := answerWithCoverage(captured, access, traverse, ov, projectionRef(obj), obj, nil, nil)
				m, _ := apimeta.Accessor(obj)
				ApplyOwnership(rel, ov, ownership.Observe(projectionRef(obj), m))
				if obj == sealed {
					requireProjectionRefs(t, rel.Consumers, d)
				} else {
					requireProjectionRefs(t, readableRefs(rel.ConfigRefs), sealed)
					if !slices.Contains(rel.ConfigRefs, ResourceRef{Kind: "Secret", Namespace: "shop", Name: "db", State: ResourceStateDenied}) {
						t.Fatalf("the mounted Secret isn't named: %+v", rel.ConfigRefs)
					}
				}
				answers = append(answers, rel, cov)
			}
			data, err := json.Marshal(answers)
			if err != nil {
				t.Fatal(err)
			}
			if prior != nil && !bytes.Equal(prior, data) {
				t.Fatalf("Secret existence changed denied answer:\n%s\n%s", prior, data)
			}
			prior = data
			for _, r := range s.reads {
				if r.Kind == "Secret" {
					t.Fatalf("borrowed denied Secret: %v", r)
				}
			}
			requireProjectionRefs(t, readableRefs(projectionAnswer(t, s, wrong, access, traverse).ConfigRefs))
			requireProjectionRefs(t, projectionAnswer(t, s, sealed, projectionAccess{denied: []string{"Secret", "Deployment"}}, traverse).Consumers)
			requireProjectionRefs(t, readableRefs(projectionAnswer(t, s, d, projectionAccess{denied: []string{"Secret", "SealedSecret"}}, traverse).ConfigRefs))
		}
	}
	// The composition itself never asks for the intermediate, including when
	// Radar could not collect it but the caller is allowed to read Secrets.
	s := newProjectionSession(t, sealed, d, secret)
	for _, state := range []ownership.ReadState{ownership.Absent, ownership.Unavailable, ownership.Present} {
		logical := &logicalSecretSession{projectionSession: s, state: state}
		for _, obj := range []runtime.Object{sealed, d} {
			a := relations.NewView(logical, ownership.Unrestricted()).Composed(relations.SealedSecretConsumers, projectionRef(obj), obj)
			if len(a.Edges) != 1 || !a.Coverage.Complete() {
				t.Fatalf("logical state %v: %+v", state, a)
			}
		}
		if logical.secretReads != 0 {
			t.Fatal("composition observed logical Secret")
		}
	}
}

type logicalSecretSession struct {
	*projectionSession
	state       ownership.ReadState
	secretReads int
}

func (s *logicalSecretSession) Borrow(r resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	if r.Group == "" && r.Kind == "Secret" {
		s.secretReads++
		return nil, ownership.ObjectRead{State: s.state, Gap: ownership.Gap{Kind: r.GroupKind(), Namespace: r.Namespace, Reason: readreason.RadarKindNotCollected}}
	}
	return s.projectionSession.Borrow(r)
}
func (s *logicalSecretSession) Object(r resourceid.Ref) ownership.ObjectRead {
	_, read := s.Borrow(r)
	return read
}

func TestRelationsAnswerInventoryLabelFallbacks(t *testing.T) {
	pool := projectionCR("karpenter.sh", "NodePool", "", "pool", nil)
	pool.SetUID("pool-uid")
	claim := projectionCR("karpenter.sh", "NodeClaim", "", "claim", nil)
	claim.SetLabels(map[string]string{"karpenter.sh/nodepool": "pool"})
	wf := projectionCR("argoproj.io", "Workflow", "shop", "run", nil)
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "step", Labels: map[string]string{"workflows.argoproj.io/workflow": "run"}}}, "", "Pod")
	s := newProjectionSession(t, pool, claim, wf, pod)
	a := ownership.Unrestricted()
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, pool, a).Manages, claim)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, claim, a).Managers, pool)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, wf, a).Manages, pod)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, pod, a).Managers, wf)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, pool, projectionAccess{denied: []string{"NodeClaim"}}).Manages)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, wf, projectionAccess{denied: []string{"Pod"}}).Manages)
	claim.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodePool", Name: "pool", UID: "replaced"}})
	s = newProjectionSession(t, pool, claim)
	requireProjectionRefs(t, projectionOwnedAnswer(t, s, pool, a).Manages)
}

func TestRelationsMappingsAgainstTheOldGraph(t *testing.T) {
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "stable"}}, "", "Service")
	deployment := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "app", Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "release", "helm.toolkit.fluxcd.io/namespace": "flux"}}}, "apps", "Deployment")
	cert := projectionCR("cert-manager.io", "Certificate", "shop", "cert", map[string]any{"spec": map[string]any{"secretName": "tls"}})
	traefik := projectionCR("traefik.io", "IngressRoute", "shop", "traefik", map[string]any{"spec": map[string]any{"tls": map[string]any{"secretName": "tls"}}})
	contour := projectionCR("projectcontour.io", "HTTPProxy", "shop", "contour", map[string]any{"spec": map[string]any{"virtualhost": map[string]any{"tls": map[string]any{"secretName": "tls"}}}})
	rollout := projectionCR("argoproj.io", "Rollout", "shop", "rollout", map[string]any{"spec": map[string]any{"strategy": map[string]any{"canary": map[string]any{"stableService": "stable"}}}})
	class := projectionCR("gateway.networking.k8s.io", "GatewayClass", "", "implementation", nil)
	gw := projectionCR("gateway.networking.k8s.io", "Gateway", "shop", "gateway", map[string]any{"spec": map[string]any{"gatewayClassName": "implementation"}})
	route := projectionCR("gateway.networking.k8s.io", "HTTPRoute", "shop", "route", map[string]any{"spec": map[string]any{"parentRefs": []any{map[string]any{"name": "gateway"}}}})
	istio := projectionCR("networking.istio.io", "Gateway", "shop", "istio", nil)
	vs := projectionCR("networking.istio.io", "VirtualService", "shop", "vs", map[string]any{"spec": map[string]any{"gateways": []any{"istio"}}})
	broker := projectionCR("eventing.knative.dev", "Broker", "shop", "broker", nil)
	trigger := projectionCR("eventing.knative.dev", "Trigger", "shop", "trigger", map[string]any{"spec": map[string]any{"broker": "broker"}})
	cluster := projectionCR("cluster.x-k8s.io", "Cluster", "shop", "cluster", nil)
	cluster.SetUID("cluster-uid")
	mhc := projectionCR("cluster.x-k8s.io", "MachineHealthCheck", "shop", "check", nil)
	mhc.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "cluster.x-k8s.io/v1", Kind: "Cluster", Name: "cluster", UID: cluster.GetUID()}})
	hr := projectionCR("helm.toolkit.fluxcd.io", "HelmRelease", "flux", "release", nil)
	cron := projectionCR("argoproj.io", "CronWorkflow", "shop", "cron", nil)
	workflow := projectionCR("argoproj.io", "Workflow", "shop", "run", nil)
	workflow.SetLabels(map[string]string{"workflows.argoproj.io/cron-workflow": "cron"})
	objects := []runtime.Object{svc, deployment, cert, traefik, contour, rollout, class, gw, route, istio, vs, broker, trigger, cluster, mhc, hr, cron, workflow}
	dynamic := newCharDynamic()
	for _, obj := range objects {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			g := u.GroupVersionKind()
			dynamic.add(g.Group, g.Version, g.Kind, strings.ToLower(g.Kind)+"s", u.GetNamespace() != "", u)
		}
	}
	provider := &mockProvider{services: []*corev1.Service{svc.(*corev1.Service)}, deployments: []*appsv1.Deployment{deployment.(*appsv1.Deployment)}}
	graph, err := NewBuilder(provider).WithDynamic(dynamic).Build(RelationshipBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	graph.StripSecretsExcept(map[SARTuple]bool{})
	access := projectionAccess{denied: []string{"Secret"}}
	s := newProjectionSession(t, objects...)
	for _, tc := range []struct {
		obj   runtime.Object
		field string
		want  []runtime.Object
	}{
		{cert, "Consumers", []runtime.Object{traefik, contour}}, {traefik, "ConfigRefs", []runtime.Object{cert}}, {contour, "ConfigRefs", []runtime.Object{cert}},
		{rollout, "Services", []runtime.Object{svc}}, {svc, "Backends", []runtime.Object{rollout}},
		{class, "Manages", []runtime.Object{gw}}, {gw, "Managers", []runtime.Object{class}},
		{gw, "Backends", []runtime.Object{route}}, {route, "Gateways", []runtime.Object{gw}},
		{istio, "Backends", []runtime.Object{vs}}, {vs, "RoutedFrom", []runtime.Object{istio}},
		{broker, "Backends", []runtime.Object{trigger}}, {trigger, "RoutedFrom", []runtime.Object{broker}},
		{mhc, "Dependencies", []runtime.Object{cluster}}, {cluster, "HealthChecks", []runtime.Object{mhc}},
		{hr, "Manages", []runtime.Object{deployment}}, {deployment, "Managers", []runtime.Object{hr}},
		{cron, "Manages", []runtime.Object{workflow}}, {workflow, "Managers", []runtime.Object{cron}},
	} {
		ref := projectionRef(tc.obj)
		t.Run(ref.String()+"/"+tc.field, func(t *testing.T) {
			old := GetRelationshipsWithObject(ref.Kind, ref.Namespace, ref.Name, tc.obj, graph, provider, dynamic, nil)
			m, _ := apimeta.Accessor(tc.obj)
			ApplyOwnership(old, ownership.NewView(s, access), ownership.Observe(ref, m))
			next := projectionOwnedAnswer(t, s, tc.obj, access)
			requireProjectionRefs(t, reflect.ValueOf(old).Elem().FieldByName(tc.field).Interface().([]ResourceRef), tc.want...)
			requireProjectionRefs(t, reflect.ValueOf(next).Elem().FieldByName(tc.field).Interface().([]ResourceRef), tc.want...)
		})
	}
}

type indexCountingSession struct {
	*projectionSession
	indexed map[resourceid.GroupKind]int
}

func (s indexCountingSession) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	s.indexed[gk]++
	return s.projectionSession.Indexed(gk, index, keys, scope)
}

func TestRelationsAnswerSkipsHelmOriginsAnInventorySupersedes(t *testing.T) {
	release := projectionCR("helm.toolkit.fluxcd.io", "HelmRelease", "flux", "release", map[string]any{"status": map[string]any{"inventory": map[string]any{"entries": []any{}}}})
	d := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "app", Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "release", "helm.toolkit.fluxcd.io/namespace": "flux"}}}, "apps", "Deployment")
	s := indexCountingSession{projectionSession: newProjectionSession(t, release, d), indexed: map[resourceid.GroupKind]int{}}
	a := projectionAccess{denied: []string{"Deployment"}}
	captured, ok := relations.CaptureSubject(s, projectionRef(release), release)
	if !ok {
		t.Fatal("capture failed")
	}
	_, coverage := answerWithCoverage(captured, a, a, ownership.NewView(captured, a), projectionRef(release), release, nil, nil)
	if n := s.indexed[resourceid.GroupKind{Group: "apps", Kind: "Deployment"}]; n != 0 {
		t.Fatalf("a release whose inventory names its members searched Deployments %d times", n)
	}
	if c, ok := coverage[relations.FluxHelmOrigins.Name]; ok {
		t.Fatalf("a search whose answers aren't shown narrowed the answer: %+v", c)
	}
}
