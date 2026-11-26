package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// correlationFixture is a shop namespace with a crashlooping Deployment that
// references a ConfigMap and a Secret, a Service selecting no pods, quota and
// webhook admission rejections, and a crashlooping workload whose Deployment
// Radar doesn't hold.
func correlationFixture(t *testing.T) timeline.EventStore {
	t.Helper()
	created := metav1.NewTime(time.Now().Add(-24 * time.Hour))
	controller := true
	one := int32(1)
	four := int32(4)
	var objs []runtime.Object
	objs = append(objs,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", CreationTimestamp: created}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other", CreationTimestamp: created}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "web-config", Namespace: "shop", CreationTimestamp: created}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "web-creds", Namespace: "shop", CreationTimestamp: created}, Data: map[string][]byte{"token": []byte("x")}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "compute", Namespace: "shop", CreationTimestamp: created}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop", CreationTimestamp: created},
			Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "nothing"}, Ports: []corev1.ServicePort{{Port: 80}}}},
	)
	webSpec := corev1.PodSpec{
		Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"}}}}},
		Containers: []corev1.Container{{Name: "web", Image: "web:2", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "web-creds"}, Key: "token"}}}}}},
	}
	crashloop := func(name string, cached bool) {
		if cached {
			objs = append(objs, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", CreationTimestamp: created, UID: types.UID("deploy-" + name)},
				Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{Spec: webSpec}},
				Status: appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1},
			})
		}
		objs = append(objs,
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name + "-rs", Namespace: "shop", CreationTimestamp: created, UID: types.UID("rs-" + name),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: types.UID("deploy-" + name), Controller: &controller}}}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name + "-rs-1", Namespace: "shop", CreationTimestamp: created, Labels: map[string]string{"app": name},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name + "-rs", UID: types.UID("rs-" + name), Controller: &controller}}},
				Spec: webSpec,
				Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", RestartCount: 7,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"}}}}}},
		)
	}
	crashloop("web", true)
	crashloop("ghost", false)
	admission := func(name, message string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", CreationTimestamp: created, UID: types.UID("deploy-" + name)},
			Spec:       appsv1.DeploymentSpec{Replicas: &four, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}},
			Status: appsv1.DeploymentStatus{Replicas: 2, UpdatedReplicas: 2, Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate", Message: message,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-9 * time.Minute)),
			}}},
		}
	}
	objs = append(objs,
		// A bare ReplicaSet: its own changes aren't tracked.
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "batch", Namespace: "shop", CreationTimestamp: created, UID: "rs-batch"},
			Spec:       appsv1.ReplicaSetSpec{Replicas: &four},
			Status: appsv1.ReplicaSetStatus{Replicas: 0, Conditions: []appsv1.ReplicaSetCondition{{
				Type: appsv1.ReplicaSetReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate",
				Message:            `pods "batch-x" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=2, limited: pods=2`,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-9 * time.Minute)),
			}}},
		},
		admission("vague", `pods "vague-x" is forbidden: exceeded quota`),
		admission("orphaned", `pods "orphaned-x" is forbidden: exceeded quota: gone, requested: pods=1, used: pods=2, limited: pods=2`),
		admission("search", `pods "search-x" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=2, limited: pods=2`),
		admission("hooked", `admission webhook "deny-root.policy.example.com" denied the request: runAsNonRoot is required`),
	)
	// The informers appear to have been watching as long as the timeline
	// below has been recording.
	restoreClock := k8s.SetTestWatchClock(func() time.Time { return time.Now().Add(-26 * time.Hour) })
	useTestResourceCache(t, fake.NewClientset(objs...))
	restoreClock()

	vwcGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	mwcGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&admissionv1.ValidatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{Kind: "ValidatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "pod-policy", CreationTimestamp: created},
		Webhooks:   []admissionv1.ValidatingWebhook{{Name: "deny-root.policy.example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vwcGVR: "ValidatingWebhookConfigurationList", mwcGVR: "MutatingWebhookConfigurationList"},
		&unstructured.Unstructured{Object: data})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: vwcGVR.Group, Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: vwcGVR.Resource, Verbs: []string{"list", "watch"}},
		{Group: mwcGVR.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: mwcGVR.Resource, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, gvr := range []schema.GroupVersionResource{vwcGVR, mwcGVR} {
		if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil || !k8s.GetDynamicResourceCache().WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("watch %s: %v", gvr, err)
		}
	}
	store := useMemoryTimeline(t)
	// History covers the 24 hours an issue without a first-seen time looks
	// back.
	timeline.SetObservationStartForTest(time.Now().Add(-25 * time.Hour))
	t.Cleanup(func() { timeline.SetObservationStartForTest(time.Time{}) })
	return store
}

// The changes every fixture records unless a test leaves one out.
// correlationChangesAt is the one time every fixture's changes are dated
// from, so two fixtures built a moment apart record identical rows.
var correlationChangesAt = sync.OnceValue(time.Now)

func correlationChanges() map[string]timeline.TimelineEvent {
	now := correlationChangesAt()
	update := func(kind, apiVersion, namespace, name, path string, ago time.Duration) timeline.TimelineEvent {
		return timeline.TimelineEvent{
			ID: kind + "/" + name, Timestamp: now.Add(-ago), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: kind, APIVersion: apiVersion, Namespace: namespace, Name: name, EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: path, OldValue: "a", NewValue: "b"}}},
		}
	}
	return map[string]timeline.TimelineEvent{
		"web":        update("Deployment", "apps/v1", "shop", "web", "spec.template.spec.containers[web].image", 4*time.Minute),
		"web-config": update("ConfigMap", "v1", "shop", "web-config", "data.flags", 6*time.Minute),
		"cart":       update("Service", "v1", "shop", "cart", "spec.selector.app", 3*time.Minute),
		"compute":    update("ResourceQuota", "v1", "shop", "compute", "spec.hard.pods", 10*time.Minute),
		"pod-policy": update("ValidatingWebhookConfiguration", "admissionregistration.k8s.io/v1", "", "pod-policy", "webhooks[0].rules", 12*time.Minute),
	}
}

func recordChanges(t *testing.T, store timeline.EventStore, except ...string) {
	t.Helper()
	for key, e := range correlationChanges() {
		if slices.Contains(except, key) {
			continue
		}
		appendInformerEvent(t, store, e)
	}
}

func appendInformerEvent(t *testing.T, store timeline.EventStore, e timeline.TimelineEvent) {
	t.Helper()
	if err := store.Append(t.Context(), e); err != nil {
		t.Fatalf("append %s: %v", e.ID, err)
	}
}

var correlationResources = []string{
	"/pods", "/configmaps", "/secrets", "/services", "/resourcequotas", "/limitranges", "/events", "/namespaces",
	"apps/deployments", "apps/replicasets", "argoproj.io/rollouts",
	"admissionregistration.k8s.io/validatingwebhookconfigurations", "admissionregistration.k8s.io/mutatingwebhookconfigurations",
}

// correlationUser can list and get every fixture resource except those
// denied, in shop (and cluster-wide for cluster-scoped kinds). A nil
// namespace list reads every namespace.
func correlationUser(env *authTestEnv, name string, namespaces []string, denied ...string) {
	perms := &auth.UserPermissions{AllowedNamespaces: namespaces}
	for _, gr := range correlationResources {
		group, resource, _ := strings.Cut(gr, "/")
		allowed := !slices.Contains(denied, resource)
		for _, verb := range []string{"list", "get"} {
			perms.SetCanI(verb, group, resource, "", allowed)
			perms.SetCanI(verb, group, resource, "shop", allowed)
		}
	}
	env.srv.permCache.Set(name, nil, perms)
}

func getResourceIssues(t *testing.T, env *authTestEnv, user, path string) (int, ResourceIssuesResponse) {
	t.Helper()
	response := env.authGet(t, path, user, "")
	defer response.Body.Close()
	var body ResourceIssuesResponse
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return response.StatusCode, body
}

// correlated reads the subject's issues, then correlates the one with
// category, and returns it.
func correlated(t *testing.T, env *authTestEnv, user, kind, name, group string, category issuesapi.Category) issuesapi.Issue {
	t.Helper()
	base := "/api/issues/resource/" + kind + "/shop/" + name + "?group=" + group
	status, listed := getResourceIssues(t, env, user, base+"&coverage=1")
	if status != http.StatusOK {
		t.Fatalf("%s: status %d", base, status)
	}
	id := ""
	for _, iss := range listed.Issues {
		if iss.Category == category {
			id = iss.ID
		}
	}
	if id == "" {
		t.Fatalf("%s: no %s issue in %+v", base, category, listed.Issues)
	}
	status, body := getResourceIssues(t, env, user, base+"&correlate="+id)
	if status != http.StatusOK || body.CorrelationStatus != "" {
		t.Fatalf("%s correlate: status %d, correlation_status %q", base, status, body.CorrelationStatus)
	}
	for _, iss := range body.Issues {
		if iss.ID == id {
			if iss.Correlation == nil {
				t.Fatalf("%s: correlated issue has no correlation: %+v", base, iss)
			}
			return iss
		}
		if iss.Correlation != nil || len(iss.CorrelatedChanges) > 0 {
			t.Fatalf("%s: an issue not named was correlated: %+v", base, iss)
		}
	}
	t.Fatalf("%s: correlated issue missing", base)
	return issuesapi.Issue{}
}

func changeKinds(iss issuesapi.Issue) []string {
	var out []string
	for _, c := range iss.CorrelatedChanges {
		out = append(out, c.Kind)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func unknownKinds(iss issuesapi.Issue) []string {
	if iss.Correlation.Unknown == nil {
		return nil
	}
	var out []string
	for _, k := range iss.Correlation.Unknown.Kinds {
		out = append(out, k.Kind)
	}
	return out
}

func TestProxyAuth_IssueCorrelationPerCaller(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	correlationUser(env, "deployments-only", []string{"shop"}, "configmaps", "secrets", "services", "resourcequotas", "limitranges",
		"validatingwebhookconfigurations", "mutatingwebhookconfigurations")
	correlationUser(env, "no-config", []string{"shop"}, "configmaps", "secrets")
	correlationUser(env, "no-service", []string{"shop"}, "services")
	correlationUser(env, "no-quota", []string{"shop"}, "resourcequotas")
	correlationUser(env, "no-webhook-list", []string{"shop"}, "validatingwebhookconfigurations", "mutatingwebhookconfigurations")
	correlationUser(env, "other-namespace", []string{"other"})

	webhookKinds := []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"}
	for _, tc := range []struct {
		user, kind, name, group string
		category                issuesapi.Category
		changes, unknown        []string
		reason                  issuesapi.CorrelationUnknownReason
	}{
		{"full", "Deployment", "web", "apps", issuesapi.CategoryCrashLoop, []string{"ConfigMap", "Deployment"}, nil, ""},
		{"deployments-only", "Deployment", "web", "apps", issuesapi.CategoryCrashLoop, []string{"Deployment"}, []string{"ConfigMap", "Secret"}, issuesapi.CorrelationUnknownUserForbidden},
		{"no-config", "Deployment", "web", "apps", issuesapi.CategoryCrashLoop, []string{"Deployment"}, []string{"ConfigMap", "Secret"}, issuesapi.CorrelationUnknownUserForbidden},
		{"full", "Service", "cart", "", issuesapi.CategoryServiceNoEndpoints, []string{"Service"}, nil, ""},
		{"no-service", "Service", "cart", "", issuesapi.CategoryServiceNoEndpoints, nil, []string{"Service"}, issuesapi.CorrelationUnknownUserForbidden},
		{"full", "Deployment", "search", "apps", issuesapi.CategoryQuotaExceeded, []string{"ResourceQuota"}, nil, ""},
		{"no-quota", "Deployment", "search", "apps", issuesapi.CategoryQuotaExceeded, nil, []string{"ResourceQuota"}, issuesapi.CorrelationUnknownUserForbidden},
		{"full", "Deployment", "hooked", "apps", issuesapi.CategoryAdmissionWebhookBlocking, []string{"ValidatingWebhookConfiguration"}, nil, ""},
		{"no-webhook-list", "Deployment", "hooked", "apps", issuesapi.CategoryAdmissionWebhookBlocking, nil, webhookKinds, issuesapi.CorrelationUnknownUserForbidden},
		{"deployments-only", "Deployment", "hooked", "apps", issuesapi.CategoryAdmissionWebhookBlocking, nil, webhookKinds, issuesapi.CorrelationUnknownUserForbidden},
	} {
		t.Run(tc.user+"/"+tc.name, func(t *testing.T) {
			iss := correlated(t, env, tc.user, tc.kind, tc.name, tc.group, tc.category)
			if got := changeKinds(iss); !slices.Equal(got, tc.changes) {
				t.Fatalf("changes from %v, want %v: %+v", got, tc.changes, iss.CorrelatedChanges)
			}
			if got := unknownKinds(iss); !slices.Equal(got, tc.unknown) {
				t.Fatalf("unknown kinds %v, want %v", got, tc.unknown)
			}
			if tc.reason != "" && iss.Correlation.Unknown.Reason != tc.reason {
				t.Fatalf("reason %q, want %q", iss.Correlation.Unknown.Reason, tc.reason)
			}
			if iss.NoRecentChanges != nil {
				t.Fatalf("absence claimed: %+v", iss)
			}
		})
	}

	t.Run("other namespace", func(t *testing.T) {
		if status, _ := getResourceIssues(t, env, "other-namespace", "/api/issues/resource/Deployment/shop/web?group=apps&correlate=x"); status != http.StatusForbidden {
			t.Fatalf("status %d, want 403", status)
		}
	})

	t.Run("no changes is claimed only when complete", func(t *testing.T) {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 1000}); err != nil {
			t.Fatal(err)
		}
		timeline.SetObservationStartForTest(time.Now().Add(-25 * time.Hour))
		iss := correlated(t, env, "full", "Service", "cart", "", issuesapi.CategoryServiceNoEndpoints)
		if iss.NoRecentChanges == nil || iss.Correlation.Unknown != nil {
			t.Fatalf("quiet readable Service: %+v", iss)
		}
		if iss.NoRecentChanges.WindowSeconds != int(iss.Correlation.WindowTo.Sub(iss.Correlation.WindowFrom).Seconds()) {
			t.Fatalf("window_seconds %d doesn't match the window %+v", iss.NoRecentChanges.WindowSeconds, iss.Correlation)
		}
	})
}

// What Radar says about a source the caller can't list is the same whether
// or not that source changed.
func TestProxyAuth_IssueCorrelationSameAnswerWhetherHiddenSourceChanged(t *testing.T) {
	for _, tc := range []struct {
		hidden, user, kind, name, group string
		category                        issuesapi.Category
		denied                          []string
	}{
		{"web-config", "no-config", "Deployment", "web", "apps", issuesapi.CategoryCrashLoop, []string{"configmaps", "secrets"}},
		{"cart", "no-service", "Service", "cart", "", issuesapi.CategoryServiceNoEndpoints, []string{"services"}},
		{"pod-policy", "no-webhook-list", "Deployment", "hooked", "apps", issuesapi.CategoryAdmissionWebhookBlocking,
			[]string{"validatingwebhookconfigurations", "mutatingwebhookconfigurations"}},
	} {
		t.Run(tc.hidden, func(t *testing.T) {
			answer := func(changed bool) string {
				store := correlationFixture(t)
				if changed {
					recordChanges(t, store)
				} else {
					recordChanges(t, store, tc.hidden)
				}
				env := newAuthTestServer(t)
				correlationUser(env, tc.user, []string{"shop"}, tc.denied...)
				iss := correlated(t, env, tc.user, tc.kind, tc.name, tc.group, tc.category)
				// Times that follow the clock the fixture is built from, not the data.
				iss.LastSeen, iss.FirstSeen, iss.ResourceCreatedAt = time.Time{}, time.Time{}, time.Time{}
				iss.Correlation.WindowFrom, iss.Correlation.WindowTo, iss.Correlation.ObservedSince = time.Time{}, time.Time{}, time.Time{}
				data, err := json.Marshal(iss)
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			if changed, quiet := answer(true), answer(false); changed != quiet {
				t.Fatalf("answer depends on the hidden change:\nchanged: %s\nquiet:   %s", changed, quiet)
			}
		})
	}
}

func TestProxyAuth_IssueCorrelationMissingWorkloadIsUnresolved(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	iss := correlated(t, env, "full", "Deployment", "ghost", "apps", issuesapi.CategoryCrashLoop)
	if iss.NoRecentChanges != nil || iss.Correlation.Unknown == nil || iss.Correlation.Unknown.Reason != issuesapi.CorrelationUnknownSourcesUnresolved {
		t.Fatalf("missing workload: %+v", iss.Correlation)
	}
}

// A Rollout that takes its template from a Deployment names its sources
// through that Deployment, so a caller who can't list Deployments learns
// nothing from the template.
func TestProxyAuth_IssueCorrelationRolloutWorkloadRefTarget(t *testing.T) {
	created := metav1.NewTime(time.Now().Add(-24 * time.Hour))
	controller := true
	zero := int32(0)
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:1", Env: []corev1.EnvVar{{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "password"}}}}}}}
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", CreationTimestamp: created}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", CreationTimestamp: created}, Data: map[string][]byte{"password": []byte("x")}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop", CreationTimestamp: created},
			Spec: appsv1.DeploymentSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}, Template: corev1.PodTemplateSpec{Spec: spec}}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "roll-rs", Namespace: "shop", CreationTimestamp: created, UID: "rs-roll",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "Rollout", Name: "roll", UID: "rollout-roll", Controller: &controller}}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "roll-rs-1", Namespace: "shop", CreationTimestamp: created,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "roll-rs", UID: "rs-roll", Controller: &controller}}},
			Spec: spec,
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", RestartCount: 7,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"}}}}}},
	))
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rollout := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
		"metadata": map[string]any{"name": "roll", "namespace": "shop", "uid": "rollout-roll"},
		"spec":     map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"}}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RolloutList"}, rollout)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Rollout", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil || !k8s.GetDynamicResourceCache().WaitForSync(gvr, 5*time.Second) {
		t.Fatalf("watch rollouts: %v", err)
	}
	store := useMemoryTimeline(t)
	timeline.SetObservationStartForTest(time.Now().Add(-25 * time.Hour))
	t.Cleanup(func() { timeline.SetObservationStartForTest(time.Time{}) })
	appendInformerEvent(t, store, timeline.TimelineEvent{
		ID: "secret-write", Timestamp: time.Now().Add(-5 * time.Minute), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Secret", APIVersion: "v1", Namespace: "shop", Name: "db", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", OldValue: []string{"password"}, NewValue: []string{"password"}}}},
	})
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	correlationUser(env, "no-deployments", []string{"shop"}, "deployments")

	if iss := correlated(t, env, "full", "Rollout", "roll", "argoproj.io", issuesapi.CategoryCrashLoop); !slices.Equal(changeKinds(iss), []string{"Secret"}) {
		t.Fatalf("full: %+v", iss.CorrelatedChanges)
	}
	iss := correlated(t, env, "no-deployments", "Rollout", "roll", "argoproj.io", issuesapi.CategoryCrashLoop)
	if len(iss.CorrelatedChanges) != 0 || iss.NoRecentChanges != nil || iss.Correlation.Unknown == nil ||
		iss.Correlation.Unknown.Reason != issuesapi.CorrelationUnknownUserForbidden || !slices.Equal(unknownKinds(iss), []string{"Deployment"}) {
		t.Fatalf("no-deployments: changes %+v, correlation %+v", iss.CorrelatedChanges, iss.Correlation.Unknown)
	}
}

// A caller who can list only one webhook configuration kind gets what a
// caller who can list neither gets: Radar doesn't look up which configuration
// declares the webhook for them, so nothing names one. Only the kinds they
// can't list differ.
func TestProxyAuth_IssueCorrelationOneWebhookKindReadable(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	env := newAuthTestServer(t)
	correlationUser(env, "validating-only", []string{"shop"}, "mutatingwebhookconfigurations")
	correlationUser(env, "no-webhook-list", []string{"shop"}, "validatingwebhookconfigurations", "mutatingwebhookconfigurations")
	answer := func(user string) (string, []string) {
		iss := correlated(t, env, user, "Deployment", "hooked", "apps", issuesapi.CategoryAdmissionWebhookBlocking)
		kinds := unknownKinds(iss)
		if iss.Correlation.Unknown == nil || iss.Correlation.Unknown.Reason != issuesapi.CorrelationUnknownUserForbidden {
			t.Fatalf("%s: unknown %+v", user, iss.Correlation.Unknown)
		}
		iss.Correlation.Unknown.Kinds = nil
		iss.LastSeen, iss.Correlation.WindowFrom, iss.Correlation.WindowTo = time.Time{}, time.Time{}, time.Time{}
		data, err := json.Marshal(iss)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "pod-policy") {
			t.Fatalf("%s: a configuration is named: %s", user, data)
		}
		return string(data), kinds
	}
	one, oneKinds := answer("validating-only")
	neither, neitherKinds := answer("no-webhook-list")
	if one != neither {
		t.Fatalf("answers differ:\none kind:  %s\nneither:   %s", one, neither)
	}
	if !slices.Equal(oneKinds, []string{"MutatingWebhookConfiguration"}) || !slices.Equal(neitherKinds, []string{"MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"}) {
		t.Fatalf("unknown kinds: one %v, neither %v", oneKinds, neitherKinds)
	}
}

// A quota rejection on a bare ReplicaSet, whose own changes Radar doesn't
// track, says whether the quota changed: when it didn't, the marker is scoped
// to the policy.
func TestProxyAuth_IssueCorrelationQuietPolicyOnUntrackedSubject(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store, "compute")
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	iss := correlated(t, env, "full", "ReplicaSet", "batch", "apps", issuesapi.CategoryQuotaExceeded)
	if iss.NoRecentChanges == nil || iss.NoRecentChanges.Scope != issuesapi.NoRecentChangesScopeAdmissionPolicy || iss.Correlation.Unknown != nil {
		t.Fatalf("quiet quota: no_recent_changes %+v, unknown %+v", iss.NoRecentChanges, iss.Correlation.Unknown)
	}

	appendInformerEvent(t, store, correlationChanges()["compute"])
	iss = correlated(t, env, "full", "ReplicaSet", "batch", "apps", issuesapi.CategoryQuotaExceeded)
	if iss.NoRecentChanges != nil || !slices.Equal(changeKinds(iss), []string{"ResourceQuota"}) {
		t.Fatalf("changed quota: no_recent_changes %+v, changes %+v", iss.NoRecentChanges, iss.CorrelatedChanges)
	}
}

// An admission issue whose rejecting policy Radar can't identify is unknown,
// never "no changes": the message names none, or names a quota that's gone.
func TestProxyAuth_IssueCorrelationUnresolvedAdmissionBlocker(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	for _, name := range []string{"vague", "orphaned"} {
		iss := correlated(t, env, "full", "Deployment", name, "apps", issuesapi.CategoryQuotaExceeded)
		if iss.NoRecentChanges != nil || iss.Correlation.Unknown == nil || iss.Correlation.Unknown.Reason != issuesapi.CorrelationUnknownSourcesUnresolved {
			t.Fatalf("%s: no_recent_changes %+v, unknown %+v", name, iss.NoRecentChanges, iss.Correlation.Unknown)
		}
	}
}

// A read that hit its bound and found nothing says so on the wire, so the
// empty list doesn't read as complete.
func TestProxyAuth_IssueCorrelationSaturatedEmptyRead(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store, "web", "web-config")
	for i := range 101 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("status-%d", i), Timestamp: time.Now().Add(-time.Duration(i+1) * time.Second), Source: timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(), Kind: "Deployment", APIVersion: "apps/v1", Namespace: "shop", Name: "web",
			EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "status.readyReplicas", OldValue: int32(i % 2), NewValue: int32((i + 1) % 2)}}},
		})
	}
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	iss := correlated(t, env, "full", "Deployment", "web", "apps", issuesapi.CategoryCrashLoop)
	if len(iss.CorrelatedChanges) != 0 || iss.NoRecentChanges != nil || !iss.CorrelatedChangesIncomplete {
		t.Fatalf("saturated empty read: changes %d, no_recent_changes %+v, incomplete %v", len(iss.CorrelatedChanges), iss.NoRecentChanges, iss.CorrelatedChangesIncomplete)
	}
}

// A boundary from Radar's own late start dates no one's activity, so a caller
// limited to some namespaces gets it too.
func TestProxyAuth_IssueCorrelationLateStartBoundaryForEveryCaller(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	timeline.SetObservationStartForTest(time.Now().Add(-20 * time.Minute))
	env := newAuthTestServer(t)
	correlationUser(env, "everywhere", nil)
	correlationUser(env, "shop-only", []string{"shop"})
	for _, tc := range []struct {
		user string
		want bool
	}{{"everywhere", true}, {"shop-only", true}} {
		iss := correlated(t, env, tc.user, "Deployment", "web", "apps", issuesapi.CategoryCrashLoop)
		history := iss.Correlation.History
		if history == nil || !history.Incomplete || !slices.Contains(history.Reasons, issuesapi.HistoryObservationStartedLate) {
			t.Fatalf("%s: history %+v", tc.user, history)
		}
		if got := !history.CompleteSince.IsZero(); got != tc.want {
			t.Fatalf("%s: complete_since sent = %v, want %v", tc.user, got, tc.want)
		}
		if iss.Correlation.Unknown == nil || iss.Correlation.Unknown.Reason != issuesapi.CorrelationUnknownHistoryIncomplete || iss.NoRecentChanges != nil {
			t.Fatalf("%s: incomplete history must block absence: %+v", tc.user, iss.Correlation.Unknown)
		}
		if len(iss.CorrelatedChanges) == 0 {
			t.Fatalf("%s: recorded changes must still list", tc.user)
		}
	}
}

func TestProxyAuth_IssueCorrelationNotFound(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	env := newAuthTestServer(t)
	correlationUser(env, "full", []string{"shop"})
	status, body := getResourceIssues(t, env, "full", "/api/issues/resource/Deployment/shop/web?group=apps&correlate=no-such-issue")
	if status != http.StatusOK || body.CorrelationStatus != correlationStatusNotFound || len(body.Issues) == 0 {
		t.Fatalf("status %d, body %+v", status, body)
	}
	for _, iss := range body.Issues {
		if iss.Correlation != nil || len(iss.CorrelatedChanges) > 0 {
			t.Fatalf("an issue was correlated: %+v", iss)
		}
	}
}

// helmStorage serves one release in shop whose upgrade failed three minutes
// ago, refusing or failing the read for the users named.
func helmStorage(t *testing.T, refused, broken string) {
	t.Helper()
	revision := func(version int, status release.Status, at time.Time) corev1.Secret {
		payload, err := json.Marshal(&release.Release{
			Name: "payments", Namespace: "shop", Version: version,
			Info:  &release.Info{Status: status, Description: "Upgrade \"payments\" failed: timed out", LastDeployed: helmtime.Time{Time: at}},
			Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "payments", Version: "1.0." + strconv.Itoa(version)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.payments.v" + strconv.Itoa(version), Namespace: "shop",
				Labels: map[string]string{"owner": "helm", "name": "payments", "status": status.String(), "version": strconv.Itoa(version)}},
			Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(payload))},
		}
	}
	secrets := []corev1.Secret{
		revision(1, release.StatusSuperseded, time.Now().Add(-48*time.Hour)),
		revision(2, release.StatusFailed, time.Now().Add(-3*time.Minute)),
	}
	full, err := json.Marshal(corev1.SecretList{TypeMeta: metav1.TypeMeta{Kind: "SecretList", APIVersion: "v1"}, Items: secrets})
	if err != nil {
		t.Fatal(err)
	}
	labels := metav1.PartialObjectMetadataList{TypeMeta: metav1.TypeMeta{Kind: "PartialObjectMetadataList", APIVersion: "meta.k8s.io/v1"}}
	for _, s := range secrets {
		labels.Items = append(labels.Items, metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "PartialObjectMetadata", APIVersion: "meta.k8s.io/v1"}, ObjectMeta: s.ObjectMeta})
	}
	labelPayload, err := json.Marshal(labels)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch user := r.Header.Get("Impersonate-User"); {
		case user != "" && user == refused:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"secrets is forbidden"}`))
			return
		case user != "" && user == broken:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"InternalError","code":500,"message":"etcd unavailable"}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/namespaces/shop/secrets") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "PartialObjectMetadataList") {
			_, _ = w.Write(labelPayload)
			return
		}
		_, _ = w.Write(full)
	}))
	t.Cleanup(server.Close)
	helm.ResetTestClient()
	if err := helm.InitializeWithRESTConfig(&rest.Config{Host: server.URL}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(helm.ResetTestClient)
}

// A native Helm release is composed and correlated from its storage, read as
// the caller.
func TestProxyAuth_IssueCorrelationHelmReadAsCaller(t *testing.T) {
	correlationFixture(t)
	helmStorage(t, "denied", "broken")
	env := newAuthTestServer(t)
	for _, user := range []string{"reader", "denied", "broken"} {
		correlationUser(env, user, []string{"shop"})
	}
	base := "/api/issues/resource/HelmRelease/shop/payments?group=helm.sh"

	status, listed := getResourceIssues(t, env, "reader", base+"&coverage=1")
	if status != http.StatusOK || len(listed.Issues) != 1 || listed.Issues[0].Kind != "HelmRelease" {
		t.Fatalf("reader: status %d, issues %+v", status, listed.Issues)
	}
	id := listed.Issues[0].ID
	status, body := getResourceIssues(t, env, "reader", base+"&correlate="+id)
	if status != http.StatusOK || body.CorrelationStatus != "" {
		t.Fatalf("reader correlate: %d %+v", status, body)
	}
	iss := body.Issues[0]
	if iss.Correlation == nil || iss.Correlation.Unknown != nil || len(iss.CorrelatedChanges) == 0 || iss.CorrelatedChanges[0].Kind != "HelmRelease" {
		t.Fatalf("reader: %+v", iss)
	}

	// The plural route name, in any case, reaches the same release.
	for _, kind := range []string{"helmreleases", "HelmReleases"} {
		code, body := getResourceIssues(t, env, "reader", "/api/issues/resource/"+kind+"/shop/payments?group=helm.sh&correlate="+id)
		if code != http.StatusOK || body.CorrelationStatus != "" || len(body.Issues) != 1 || body.Issues[0].Correlation == nil || len(body.Issues[0].CorrelatedChanges) == 0 {
			t.Fatalf("%s: %d %+v", kind, code, body)
		}
	}

	for _, tc := range []struct {
		user   string
		status string
	}{{"denied", string(readreason.UserForbidden)}, {"broken", string(readreason.ReadError)}} {
		code, body := getResourceIssues(t, env, tc.user, base+"&correlate="+id)
		if code != http.StatusOK || body.CorrelationStatus != tc.status || len(body.Issues) != 0 || len(body.Unchecked) != 1 {
			t.Fatalf("%s: %d %+v", tc.user, code, body)
		}
	}
}

// A switch of the timeline store or cluster during the read fails it.
func TestCorrelationTimelineIdentityCheck(t *testing.T) {
	useMemoryTimeline(t)
	if err := withTimelineIdentity(func() {}); err != nil {
		t.Fatalf("unchanged identity: %v", err)
	}
	err := withTimelineIdentity(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
			t.Fatal(err)
		}
	})
	if err != errReadHandlesChanged {
		t.Fatalf("store switch: %v, want errReadHandlesChanged", err)
	}
	if failure := collectionFailure("issues", err); failure.code != readreason.ConnectionChanged {
		t.Fatalf("code %q, want connection_changed", failure.code)
	}

	// Same store, another cluster.
	previous := k8s.SetTestContextName("before-switch")
	t.Cleanup(func() { k8s.SetTestContextName(previous) })
	if err := withTimelineIdentity(func() { k8s.SetTestContextName("after-switch") }); err != errReadHandlesChanged {
		t.Fatalf("cluster switch: %v, want errReadHandlesChanged", err)
	}
}

// Shared without authentication: anyone who reaches Radar gets the answer, a
// page in their browser on another site gets nothing, and the Hub tunnel
// still gets it.
func TestIssueCorrelationWithoutAuth(t *testing.T) {
	store := correlationFixture(t)
	recordChanges(t, store)
	srv := New(Config{DevMode: true})
	handler := srv.Handler()
	ts := httptest.NewServer(handler)
	tunnel := httptest.NewServer(cloud.AuthenticatedTunnelHandler(handler))
	t.Cleanup(func() { ts.Close(); tunnel.Close(); srv.Stop() })

	get := func(base string, crossSite bool, path string) (int, ResourceIssuesResponse) {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if crossSite {
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("Origin", "https://evil.example")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body ResourceIssuesResponse
		if resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(resp.Body).Decode(&body)
		}
		return resp.StatusCode, body
	}
	path := "/api/issues/resource/Service/shop/cart?coverage=1"
	_, listed := get(ts.URL, false, path)
	if len(listed.Issues) == 0 {
		t.Fatal("no issue to correlate")
	}
	path = fmt.Sprintf("/api/issues/resource/Service/shop/cart?correlate=%s", listed.Issues[0].ID)
	if status, body := get(ts.URL, false, path); status != http.StatusOK || body.Issues[0].Correlation == nil || len(body.Issues[0].CorrelatedChanges) == 0 {
		t.Fatalf("direct read: %d %+v", status, body)
	}
	if status, _ := get(ts.URL, true, path); status != http.StatusForbidden {
		t.Fatalf("cross-site browser read: %d, want 403", status)
	}
	if status, body := get(tunnel.URL, true, path); status != http.StatusOK || body.Issues[0].Correlation == nil {
		t.Fatalf("over the Hub tunnel: %d %+v", status, body)
	}
}
