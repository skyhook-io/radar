package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/rightsizing"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
	"maps"
	"slices"
)

func TestRightsizingScopeTunnelAndCaller(t *testing.T) {
	s := &Server{}
	for _, fn := range []http.HandlerFunc{s.handleWorkloadRightsizingScope, s.handleWorkloadRightsizingInventory} {
		for _, tc := range []struct {
			h    http.Handler
			want int
		}{{fn, 404}, {cloud.AuthenticatedTunnelHandler(fn), 401}} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/agent/test", nil)
			req.Header.Set("X-Forwarded-User", "owner")
			tc.h.ServeHTTP(w, req)
			if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("got %d %#v", w.Code, w.Header())
			}
		}
	}
}

func TestSizingInventoryExactReads(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	meta := metav1.ObjectMeta{Name: "hidden", Namespace: "team"}
	client := dynamicfake.NewSimpleDynamicClient(scheme, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "idle"}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](0)}}, &appsv1.Deployment{ObjectMeta: meta}, &appsv1.StatefulSet{ObjectMeta: meta}, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "team"}})
	deny := func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(a.GetResource().GroupResource(), "", nil)
	}
	client.PrependReactor("get", "statefulsets", deny)
	client.PrependReactor("list", "replicasets", deny)
	got := readSizingInventory(t.Context(), client, fake.NewSimpleClientset(), []string{"team"})
	if !got.read || !slices.Contains(got.Gaps, readreason.UserForbidden) || len(got.Items) != 1 || got.Items[0].Name != "ok" {
		t.Fatal(got)
	}
	if slices.ContainsFunc(client.Actions(), func(a k8stesting.Action) bool {
		if get, ok := a.(k8stesting.GetAction); ok && a.GetVerb() == "get" {
			return get.GetName() == "idle"
		}
		return false
	}) {
		t.Fatal("scaled-to-zero workload used a named lookup")
	}
	// An exact self-review avoids per-object GETs for a namespace-wide reader.
	broad := fake.NewSimpleClientset()
	broad.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		req := a.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		attrs := req.Spec.ResourceAttributes
		if attrs.Group != "apps" || attrs.Verb != "get" || attrs.Namespace != "team" {
			t.Fatalf("unexpected broad read %#v", attrs)
		}
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: attrs.Resource == "daemonsets"}}, nil
	})
	client.ClearActions()
	got = readSizingInventory(t.Context(), client, broad, []string{"team"})
	if len(got.Items) != 1 || slices.ContainsFunc(client.Actions(), func(a k8stesting.Action) bool { return a.Matches("get", "daemonsets") }) {
		t.Fatal(got, client.Actions())
	}
	client.PrependReactor("list", "pods", deny)
	got = readSizingInventory(t.Context(), client, fake.NewSimpleClientset(), []string{"team"})
	if got.read || !slices.Contains(got.Gaps, readreason.UserForbidden) || len(got.Items) != 0 {
		t.Fatal(got)
	}
}

func TestSizingTemplateAndCurrentReplicaGuards(t *testing.T) {
	container := corev1.Container{Name: "api", Image: "api:v2", Env: []corev1.EnvVar{{Name: "TOKEN", Value: "sensitive-example"}}}
	owner := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api", UID: "controller", Generation: 2, CreationTimestamp: metav1.NewTime(time.Now())},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{container}}}},
		Status:     appsv1.StatefulSetStatus{Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, ObservedGeneration: 2, UpdateRevision: "new", CurrentRevision: "new"},
	}
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api-0", UID: "pod-new", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&owner, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))}, Labels: map[string]string{"app": "api", appsv1.ControllerRevisionHashLabelKey: "new"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{container}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}
	for _, tc := range []struct {
		name   string
		change func(*appsv1.StatefulSet, *corev1.Pod)
	}{
		{"stable", func(*appsv1.StatefulSet, *corev1.Pod) {}},
		{"on-delete fully replaced", func(sts *appsv1.StatefulSet, _ *corev1.Pod) {
			sts.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
			sts.Status.CurrentRevision = "old"
		}},
		{"unobserved", func(sts *appsv1.StatefulSet, _ *corev1.Pod) { sts.Generation++ }},
		{"partition", func(sts *appsv1.StatefulSet, _ *corev1.Pod) { sts.Status.CurrentRevision = "old" }},
		{"partial", func(sts *appsv1.StatefulSet, _ *corev1.Pod) { sts.Status.UpdatedReplicas = 0 }},
		{"old pod", func(_ *appsv1.StatefulSet, p *corev1.Pod) { p.Labels[appsv1.ControllerRevisionHashLabelKey] = "old" }},
		{"registry rewrite", func(_ *appsv1.StatefulSet, p *corev1.Pod) { p.Spec.Containers[0].Image = "api:v1" }},
		{"missing container", func(_ *appsv1.StatefulSet, p *corev1.Pod) { p.Spec.Containers = nil }},
		{"not ready", func(_ *appsv1.StatefulSet, p *corev1.Pod) { p.Status.Conditions = nil }},
		{"different group", func(_ *appsv1.StatefulSet, p *corev1.Pod) { p.OwnerReferences[0].APIVersion = "test.example/v1" }},
		{"different UID", func(_ *appsv1.StatefulSet, p *corev1.Pod) { p.OwnerReferences[0].UID = "recreated" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sts, p := owner.DeepCopy(), pod.DeepCopy()
			tc.change(sts, p)
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sts)
			if err != nil {
				t.Fatal(err)
			}
			u := &unstructured.Unstructured{Object: m}
			scope, tmpl, sel, err := sizingTemplate("StatefulSet", u)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(scope)
			var projection struct {
				PodSpec map[string]json.RawMessage `json:"podSpec"`
			}
			if err := json.Unmarshal(wire, &projection); err != nil {
				t.Fatal(err)
			}
			if got := slices.Sorted(maps.Keys(projection.PodSpec)); !slices.Equal(got, []string{"containers"}) {
				t.Fatalf("unexpected PodSpec fields: %v", got)
			}
			var projected []map[string]json.RawMessage
			if err := json.Unmarshal(projection.PodSpec["containers"], &projected); err != nil {
				t.Fatal(err)
			}
			if len(projected) != 1 || !slices.Equal(slices.Sorted(maps.Keys(projected[0])), []string{"name", "resources"}) {
				t.Fatalf("unexpected container fields: %v", projected)
			}
			if err != nil || strings.Contains(string(wire), "sensitive-example") || len(scope.TemplateHash) != 64 {
				t.Fatalf("unsafe projection or missing hash")
			}
			terminal := p.DeepCopy()
			terminal.Name = "previous"
			terminal.UID = "previous"
			terminal.Status.Phase = corev1.PodFailed
			pods, err := verifiedSizingPods(t.Context(), fake.NewSimpleClientset(p, terminal), u, scope, tmpl, sel)
			if tc.name == "stable" || tc.name == "registry rewrite" || tc.name == "on-delete fully replaced" {
				if err != nil || len(pods) != 1 {
					t.Fatalf("got %v, %v", pods, err)
				}
			} else if tc.name == "missing container" {
				if !errors.Is(err, errSizingAdmission) {
					t.Fatalf("missing admission error: %v", err)
				}
			} else if !errors.Is(err, errSizingRollout) {
				t.Fatalf("want rollout conflict, got %v", err)
			}
		})
	}
}

func TestRightsizingScopeReadHPAAsCaller(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "scaler"}, Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}, Metrics: []autoscalingv2.MetricSpec{{Type: autoscalingv2.ContainerResourceMetricSourceType, ContainerResource: &autoscalingv2.ContainerResourceMetricSource{Name: corev1.ResourceCPU, Container: "api", Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr.To[int32](70)}}}}}}
	client := fake.NewSimpleClientset(hpa)
	scope := rightsizing.Scope{Kind: "Deployment", Namespace: "team", Name: "api"}
	readSizingHPA(t.Context(), client, &scope)
	if !scope.HPAEvidenceAvailable || !scope.HPAManaged.Controls("api", "cpu") || scope.HPAManaged.Controls("logger", "cpu") || scope.HPAManaged.Controls("api", "memory") {
		t.Fatalf("unexpected HPA guard: %#v", scope)
	}
	client.PrependReactor("list", "horizontalpodautoscalers", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(a.GetResource().GroupResource(), "", nil)
	})
	denied := rightsizing.Scope{Kind: "Deployment", Namespace: "team", Name: "api"}
	readSizingHPA(t.Context(), client, &denied)
	if denied.HPAEvidenceAvailable || len(denied.HPAManaged) > 0 {
		t.Fatalf("HPA facts leaked to denied caller")
	}
}

func TestRightsizingScopeDeploymentRejectsStaleReplicaSet(t *testing.T) {
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "api:v1"}}}}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{UID: "dep", Namespace: "team", Name: "api", Generation: 1, CreationTimestamp: metav1.Now()}, Spec: appsv1.DeploymentSpec{Replicas: ptr.To[int32](1), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}, Template: template}, Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, ObservedGeneration: 1}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{UID: "rs", Name: "api-123", Namespace: "team", Labels: map[string]string{"app": "api", appsv1.DefaultDeploymentUniqueLabelKey: "123"}, Annotations: map[string]string{"deployment.kubernetes.io/revision": "1"}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(dep, appsv1.SchemeGroupVersion.WithKind("Deployment"))}}, Spec: appsv1.ReplicaSetSpec{Template: *template.DeepCopy()}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod", Name: "api-123-one", Namespace: "team", Labels: rs.Labels, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(rs, appsv1.SchemeGroupVersion.WithKind("ReplicaSet"))}}, Spec: *template.Spec.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	for _, changed := range []bool{false, true} {
		if changed {
			dep.Spec.Template.Spec.Containers[0].Args = []string{"--more-workers"}
		}
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(dep)
		if err != nil {
			t.Fatal(err)
		}
		obj := &unstructured.Unstructured{Object: m}
		scope, tmpl, selector, err := sizingTemplate("Deployment", obj)
		if err != nil {
			t.Fatal(err)
		}
		client := fake.NewSimpleClientset(rs, pod)
		client.PrependReactor("list", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
			opts := a.(k8stesting.ListActionImpl).GetListOptions()
			if opts.Continue == "next" {
				return false, nil, nil
			}
			first := metav1.ListMeta{Continue: "next"}
			if a.Matches("list", "pods") {
				return true, &corev1.PodList{ListMeta: first}, nil
			}
			return true, &appsv1.ReplicaSetList{ListMeta: first}, nil
		})
		pods, err := verifiedSizingPods(t.Context(), client, obj, scope, tmpl, selector)
		if changed && !errors.Is(err, errSizingRollout) {
			t.Fatalf("stale ReplicaSet accepted: %#v %v", pods, err)
		}
		if !changed && (err != nil || len(pods) != 1) {
			t.Fatalf("current ReplicaSet rejected: %v", err)
		}
	}
}

func TestSizingInventoryInvalidNamespace(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "/api/agent/workload-rightsizing-inventory?namespace=bad.name", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "reader"}))
	w := httptest.NewRecorder()
	cloud.AuthenticatedTunnelHandler(http.HandlerFunc(s.handleWorkloadRightsizingInventory)).ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("%d: %s", w.Code, w.Body.String())
	}
}

func TestRightsizingScopeDaemonSetRevisionWithoutPodLabels(t *testing.T) {
	options := metav1.ObjectMeta{Namespace: "team", Name: "collector", UID: "ds", Generation: 1, CreationTimestamp: metav1.Now()}
	container := corev1.Container{Name: "agent", Image: "agent:v1"}
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "collector"}}
	ds := &appsv1.DaemonSet{ObjectMeta: options, Spec: appsv1.DaemonSetSpec{Selector: selector, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{container}}}}, Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 1, CurrentNumberScheduled: 1, UpdatedNumberScheduled: 1, NumberReady: 1, ObservedGeneration: 1}}
	ref := *metav1.NewControllerRef(ds, appsv1.SchemeGroupVersion.WithKind("DaemonSet"))
	revision := &appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "team", Labels: map[string]string{appsv1.ControllerRevisionHashLabelKey: "hash"}, OwnerReferences: []metav1.OwnerReference{ref}}, Revision: 7}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod", Name: "collector-0", Namespace: "team", OwnerReferences: []metav1.OwnerReference{ref}, Labels: map[string]string{"app": "collector", appsv1.ControllerRevisionHashLabelKey: "hash"}}, Spec: ds.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ds)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	s, tmpl, sel, err := sizingTemplate("DaemonSet", u)
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleClientset(revision, pod)
	pods, err := verifiedSizingPods(t.Context(), client, u, s, tmpl, sel)
	podLookup := client.Actions()[0].(k8stesting.ListAction)
	if !podLookup.Matches("list", "pods") || podLookup.GetListRestrictions().Fields.String() != "status.phase!=Failed,status.phase!=Succeeded" {
		t.Fatal("terminal candidates must be excluded before pagination", podLookup)
	}

	if err != nil || len(pods) != 1 {
		t.Fatalf("current DaemonSet: %v, %v", pods, err)
	}
}

func TestSizingInjectedSidecarWithSharedLimit(t *testing.T) {
	container := corev1.Container{Name: "api"}
	template := corev1.PodSpec{Containers: []corev1.Container{container}, Resources: &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}}
	actual := corev1.Pod{Spec: *template.DeepCopy()}
	actual.Spec.Containers = append(actual.Spec.Containers, corev1.Container{Name: "proxy"})
	if sizingPodMatches(&actual, template) {
		t.Fatal("injected container can change a shared Pod limit budget")
	}
	template.Resources = nil
	actual.Spec.Resources = nil
	if !sizingPodMatches(&actual, template) {
		t.Fatal("injected container unnecessarily blocked per-container sizing")
	}
}

func TestSizingInventoryExplicitNamespaceRechecksAccess(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"old"}})
	r := requestWithUser("GET", "/api/agent/workload-rightsizing-inventory?namespace=team", &auth.User{Username: "alice"})
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := dynamicfake.NewSimpleDynamicClient(scheme, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api"}})
	read := func() sizingInventory {
		return s.sizingInventoryForRequest(r, parseNamespaces(r.URL.Query()), client, fake.NewSimpleClientset())
	}
	got := read()
	if !got.read || len(got.Gaps) != 0 || len(got.Items) != 1 || got.Items[0].Namespace != "team" {
		t.Fatalf("new grant = %+v", got)
	}
	client.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(a.GetResource().GroupResource(), "", nil)
	})
	if got := read(); got.read || !slices.Contains(got.Gaps, readreason.UserForbidden) {
		t.Fatalf("revoked access = %+v", got)
	}
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("temporarily unavailable")
	})
	if got := read(); got.read || !slices.Contains(got.Gaps, readreason.ReadError) {
		t.Fatalf("unavailable = %+v", got)
	}
}

func TestSizingInventoryWithoutNamespaceDiscovery(t *testing.T) {
	previous := k8s.SetTestClient(nil)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"seed"}})
	r := requestWithUser("GET", "/api/agent/workload-rightsizing-inventory", &auth.User{Username: "alice"})
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := dynamicfake.NewSimpleDynamicClient(scheme,
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "store", Namespace: "outside-seeds"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "seed"}},
	)
	read := func() sizingInventory {
		return s.sizingInventoryForRequest(r, nil, client, fake.NewSimpleClientset())
	}
	if got := read(); !got.read || len(got.Gaps) != 0 || len(got.Items) != 2 {
		t.Fatalf("cluster-wide grants without namespace discovery = %+v", got)
	}

	client.ClearActions()
	client.PrependReactor("list", "replicasets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == metav1.NamespaceAll {
			return true, nil, apierrors.NewForbidden(a.GetResource().GroupResource(), "", nil)
		}
		return false, nil, nil
	})
	store := sizingCandidate{Kind: "StatefulSet", Namespace: "outside-seeds", Name: "store"}
	api := sizingCandidate{Kind: "Deployment", Namespace: "seed", Name: "api"}
	if got := read(); !slices.Contains(got.Items, store) || !slices.Contains(got.Items, api) || len(got.Items) != 2 ||
		!slices.Equal(got.Gaps, []readreason.Code{readreason.RadarNamespaceNotCollected}) {
		t.Fatalf("mixed grants and incomplete namespace discovery = %+v", got)
	}
	if slices.ContainsFunc(client.Actions(), func(a k8stesting.Action) bool {
		return a.Matches("list", "statefulsets") && a.GetNamespace() != metav1.NamespaceAll
	}) {
		t.Fatal("named fallback repeated an authorized cluster-wide read")
	}

	installed := k8s.RBACNamespaces()
	k8s.SetRBACNamespaces([]string{"seed"})
	t.Cleanup(func() { k8s.SetRBACNamespaces(installed) })
	client.ClearActions()
	if got := read(); !slices.Equal(got.Items, []sizingCandidate{api}) || len(got.Gaps) != 0 {
		t.Fatalf("namespace-scoped installation = %+v", got)
	}
	if slices.ContainsFunc(client.Actions(), func(a k8stesting.Action) bool { return a.GetNamespace() != "seed" }) {
		t.Fatal("queried outside the installation's declared namespace access")
	}
}

func TestSizingInventoryManyNamespaces(t *testing.T) {
	const total = 200
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var objects []runtime.Object
	var namespaces []string
	for i := range total {
		ns := fmt.Sprintf("team-%03d", i)
		namespaces = append(namespaces, ns)
		objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "api"}})
	}
	client := dynamicfake.NewSimpleDynamicClient(scheme, objects...)
	// client-go's root LIST fake retains selectors only, so drive the two
	// response pages in order.
	page := 0
	client.PrependReactor("list", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != metav1.NamespaceAll {
			return false, nil, nil
		}
		result := &unstructured.UnstructuredList{}
		result.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("DeploymentList"))
		start := page * 100
		page++
		if page == 1 {
			result.SetContinue("second")
		}
		if page > 2 {
			return true, nil, fmt.Errorf("unexpected extra page")
		}
		for _, ns := range namespaces[start : start+100] {
			var obj unstructured.Unstructured
			obj.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
			obj.SetNamespace(ns)
			obj.SetName("api")
			result.Items = append(result.Items, obj)
		}
		return true, result, nil
	})
	caller := fake.NewSimpleClientset()
	caller.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	for _, tc := range []struct {
		name  string
		ns    []string
		calls int
	}{
		{"namespaced", namespaces, 9 * total},
		{"cluster wide", []string{metav1.NamespaceAll}, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client.ClearActions()
			caller.ClearActions()
			got := readSizingInventory(t.Context(), client, caller, tc.ns)
			distinct := map[string]bool{}
			for _, obj := range got.Items {
				distinct[obj.Namespace] = true
			}
			calls := len(client.Actions()) + len(caller.Actions())
			if calls != tc.calls || len(distinct) != total || len(got.Items) != total || len(got.Gaps) != 0 {
				t.Fatalf("namespaces=%d candidates=%d gaps=%v API calls=%d", len(distinct), len(got.Items), got.Gaps, calls)
			}
			t.Logf("namespaces=%d candidates=%d caller API requests=%d", len(distinct), len(got.Items), calls)
		})
	}
}

func TestSizingPodMatchesDefaultedContainerRequest(t *testing.T) {
	template := corev1.PodSpec{Containers: []corev1.Container{{
		Name: "api", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
	}}}
	pod := corev1.Pod{Spec: *template.DeepCopy()}
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1000m")}
	if !sizingPodMatches(&pod, template) {
		t.Fatal("defaulted request did not match the workload's limit")
	}
	pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("500m")
	if sizingPodMatches(&pod, template) {
		t.Fatal("admission override must invalidate scope")
	}
}

func TestSizingPodMatchesDefaultedPodRequests(t *testing.T) {
	cpu := func(q string) corev1.ResourceList {
		return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(q)}
	}
	template := corev1.PodSpec{
		Resources: &corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("64Mi"),
		}},
		Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: cpu("100m")}}},
		InitContainers: []corev1.Container{
			{Name: "proxy", RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways), Resources: corev1.ResourceRequirements{Requests: cpu("50m")}},
			{Name: "setup", Resources: corev1.ResourceRequirements{Requests: cpu("200m")}},
		},
	}
	pod := corev1.Pod{Spec: *template.DeepCopy()}
	pod.Spec.Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("64Mi"),
	}
	if !sizingPodMatches(&pod, template) {
		t.Fatal("expected the larger init cohort for CPU and the Pod limit for memory")
	}
	if template.Resources.Requests != nil {
		t.Fatal("a derived request must not become a fixed template request")
	}
	t.Run("changed Pod request", func(t *testing.T) {
		changed := pod.DeepCopy()
		changed.Spec.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("300m")
		if sizingPodMatches(changed, template) {
			t.Fatal("accepted an admission override")
		}
	})
	t.Run("changed Pod limit", func(t *testing.T) {
		changed := pod.DeepCopy()
		changed.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("2")
		if sizingPodMatches(changed, template) {
			t.Fatal("accepted a different shared budget")
		}
	})
	t.Run("explicit Pod request", func(t *testing.T) {
		explicit := template.DeepCopy()
		explicit.Resources.Requests = cpu("0")
		if sizingPodMatches(&pod, *explicit) {
			t.Fatal("ignored the explicit zero in the template")
		}
	})
	t.Run("changed container consistent with changed Pod request", func(t *testing.T) {
		changed := pod.DeepCopy()
		changed.Spec.Containers[0].Resources.Requests = cpu("300m")
		changed.Spec.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("350m")
		if sizingPodMatches(changed, template) {
			t.Fatal("accepted a changed app container")
		}
	})
}

func TestSizingCandidateListBudgets(t *testing.T) {
	items := func(l *corev1.PodList) []corev1.Pod { return l.Items }
	t.Run("cap across pages", func(t *testing.T) {
		list := func(_ context.Context, opts metav1.ListOptions) (*corev1.PodList, error) {
			if opts.Continue == "" {
				return &corev1.PodList{Items: make([]corev1.Pod, 512), ListMeta: metav1.ListMeta{Continue: "next"}}, nil
			}
			if opts.Limit != 513 {
				t.Fatalf("initial query parameters changed for the final page: limit = %d", opts.Limit)
			}
			return &corev1.PodList{Items: []corev1.Pod{{}}}, nil
		}
		got, err := listSizingCandidates(t.Context(), metav1.ListOptions{}, list, items)
		if !errors.Is(err, errSizingLimit) || got != nil {
			t.Fatalf("oversized list returned items=%d, error=%v", len(got), err)
		}
	})
	t.Run("empty pages exhaust read budget", func(t *testing.T) {
		calls := 0
		list := func(context.Context, metav1.ListOptions) (*corev1.PodList, error) {
			calls++
			return &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "more"}}, nil
		}
		got, err := listSizingCandidates(t.Context(), metav1.ListOptions{}, list, items)
		if calls != 64 || got != nil || err == nil || errors.Is(err, errSizingLimit) {
			t.Fatalf("incomplete list returned items=%d, calls=%d, error=%v", len(got), calls, err)
		}
	})
}
