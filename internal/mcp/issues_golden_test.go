package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
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
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// The issues tool's full response is pinned for a fixture that exercises every
// per-issue correlation path, so a change to how correlation is computed shows
// up as a diff of what agents receive. Regenerate with
// go test ./internal/mcp -run TestIssuesToolGolden -update-issues-golden.
var updateIssuesGolden = flag.Bool("update-issues-golden", false, "rewrite the issues tool goldens")

// goldenNow is the fake clock's start inside a synctest bubble.
var goldenNow = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

var goldenNamespaces = []string{"shop", "admit", "fresh", "crowd", "other"}

func goldenAgo(d time.Duration) time.Time { return goldenNow.Add(-d) }

// realClockTimestamp matches times from outside the fake clock: the informers
// start before the bubble, so when Radar began watching a kind is real time.
var realClockTimestamp = regexp.MustCompile(`\b(?:19[0-8]\d|199[0-8]|20(?:0[1-9]|[1-9]\d))-\d\d-\d\dT\d\d:\d\d:\d\dZ`)

func TestIssuesToolGolden(t *testing.T) {
	prevLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = prevLocal })

	initGoldenCluster(t)
	initGoldenHelm(t)
	setConnectedForTest(t)
	t.Cleanup(getPermCache().Invalidate)

	scenarios := []struct {
		name  string
		store func(t *testing.T)
		ctx   func(t *testing.T) context.Context
		input issuesInput
	}{
		{name: "shop", store: goldenStoreShop, ctx: goldenAdmin, input: issuesInput{Namespace: "shop"}},
		{name: "shop_restricted", store: goldenStoreShop, ctx: goldenRestricted, input: issuesInput{Namespace: "shop"}},
		{name: "shop_young_store", store: goldenStoreYoung, ctx: goldenAdmin, input: issuesInput{Namespace: "shop"}},
		{name: "shop_evicted_history", store: goldenStoreEvicted, ctx: goldenAdmin, input: issuesInput{Namespace: "shop"}},
		{name: "admit", store: goldenStoreShop, ctx: goldenAdmin, input: issuesInput{Namespace: "admit"}},
		{name: "admit_restricted", store: goldenStoreShop, ctx: goldenRestricted, input: issuesInput{Namespace: "admit"}},
		{name: "admit_young_store", store: goldenStoreYoung, ctx: goldenAdmin, input: issuesInput{Namespace: "admit"}},
		{name: "fresh", store: goldenStoreShop, ctx: goldenAdmin, input: issuesInput{Namespace: "fresh"}},
		{name: "crowd_over_cap", store: goldenStoreShop, ctx: goldenAdmin, input: issuesInput{Namespace: "crowd"}},
		{name: "all_namespaces", store: goldenStoreShop, ctx: goldenAdmin, input: issuesInput{}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			var got string
			synctest.Test(t, func(t *testing.T) {
				resetGoldenStore(t)
				sc.store(t)
				result, _, err := handleIssuesTool(sc.ctx(t), nil, sc.input)
				if err != nil {
					t.Fatalf("handleIssuesTool: %v", err)
				}
				got = extractText(t, result)
			})
			got = realClockTimestamp.ReplaceAllString(got, "<real clock>")
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, []byte(got), "", "  "); err != nil {
				t.Fatalf("indent: %v", err)
			}
			pretty.WriteByte('\n')
			path := filepath.Join("testdata", "issues_golden", sc.name+".json")
			if *updateIssuesGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s (regenerate with -update-issues-golden): %v", path, err)
			}
			if !bytes.Equal(want, pretty.Bytes()) {
				t.Fatalf("%s differs from the golden; diff it after regenerating with -update-issues-golden.\ngot:\n%s", path, pretty.String())
			}
		})
	}
}

func goldenAdmin(t *testing.T) context.Context {
	t.Helper()
	perms := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	for _, gr := range []string{
		"/configmaps", "/secrets", "/services", "/pods", "/resourcequotas", "/limitranges", "/events", "/namespaces", "/nodes",
		"apps/deployments", "apps/replicasets", "apps/statefulsets", "apps/daemonsets", "argoproj.io/rollouts",
		"admissionregistration.k8s.io/validatingwebhookconfigurations", "admissionregistration.k8s.io/mutatingwebhookconfigurations",
	} {
		group, resource, _ := strings.Cut(gr, "/")
		for _, verb := range []string{"list", "get"} {
			perms.SetCanI(verb, group, resource, "", true)
			for _, ns := range goldenNamespaces {
				perms.SetCanI(verb, group, resource, ns, true)
			}
		}
	}
	getPermCache().Set("golden-admin", nil, perms)
	return pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "golden-admin"})
}

// goldenRestricted can read the fixture namespaces' workloads and pods, and none
// of their Services, ConfigMaps, Secrets, ResourceQuotas or LimitRanges, nor any
// webhook configuration.
func goldenRestricted(t *testing.T) context.Context {
	t.Helper()
	perms := &pkgauth.UserPermissions{AllowedNamespaces: goldenNamespaces}
	allow := map[string]bool{
		"/pods": true, "/events": true, "apps/deployments": true, "apps/replicasets": true, "apps/statefulsets": true,
		"apps/daemonsets": true, "argoproj.io/rollouts": true,
	}
	for _, gr := range []string{
		"/configmaps", "/secrets", "/services", "/pods", "/resourcequotas", "/limitranges", "/events", "/namespaces", "/nodes",
		"apps/deployments", "apps/replicasets", "apps/statefulsets", "apps/daemonsets", "argoproj.io/rollouts",
		"admissionregistration.k8s.io/validatingwebhookconfigurations", "admissionregistration.k8s.io/mutatingwebhookconfigurations",
	} {
		group, resource, _ := strings.Cut(gr, "/")
		for _, verb := range []string{"list", "get"} {
			perms.SetCanI(verb, group, resource, "", false)
			for _, ns := range goldenNamespaces {
				perms.SetCanI(verb, group, resource, ns, allow[gr])
			}
		}
	}
	getPermCache().Set("golden-restricted", nil, perms)
	return pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "golden-restricted"})
}

func resetGoldenStore(t *testing.T) {
	t.Helper()
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
}

func initGoldenMemoryStore(t *testing.T, size int, observed time.Duration) timeline.EventStore {
	t.Helper()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: size}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	timeline.SetObservationStartForTest(time.Now().Add(-observed))
	return timeline.GetStore()
}

func goldenUpdate(kind, apiVersion, namespace, name string, at time.Time, path string, oldValue, newValue any) timeline.TimelineEvent {
	return timeline.TimelineEvent{
		ID: fmt.Sprintf("%s/%s/%s/%s/%d", kind, namespace, name, path, at.UnixNano()), Timestamp: at,
		Kind: kind, APIVersion: apiVersion, Namespace: namespace, Name: name, EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: path, OldValue: oldValue, NewValue: newValue}}, Summary: path + " changed"},
	}
}

func goldenAdd(kind, apiVersion, namespace, name string, at time.Time) timeline.TimelineEvent {
	return timeline.TimelineEvent{
		ID: fmt.Sprintf("%s/%s/%s/add/%d", kind, namespace, name, at.Unix()), Timestamp: at,
		Kind: kind, APIVersion: apiVersion, Namespace: namespace, Name: name, EventType: timeline.EventTypeAdd,
	}
}

// goldenShopChanges are the changes every shop scenario records.
func goldenShopChanges() []timeline.TimelineEvent {
	events := []timeline.TimelineEvent{
		goldenUpdate("Deployment", "apps/v1", "shop", "web", goldenAgo(4*time.Minute), "spec.template.spec.containers[web].image", "web:1", "web:2"),
		goldenUpdate("Deployment", "apps/v1", "shop", "quiet", goldenAgo(2*time.Minute), "status.readyReplicas", int32(1), int32(0)),
		goldenUpdate("ConfigMap", "v1", "shop", "flagd-config", goldenAgo(6*time.Minute), "data.flags", "off", "on"),
		goldenUpdate("Secret", "v1", "shop", "flagd-creds", goldenAgo(8*time.Minute), "data (modified keys)", nil, nil),
		goldenUpdate("Service", "v1", "shop", "cart", goldenAgo(3*time.Minute), "spec.selector.app", "cart", "cartt"),
		goldenUpdate("Deployment", "apps/v1", "admit", "search", goldenAgo(20*time.Minute), "spec.replicas", int64(2), int64(4)),
		goldenAdd("ResourceQuota", "v1", "admit", "compute", goldenAgo(10*time.Minute)),
		goldenUpdate("LimitRange", "v1", "admit", "limits", goldenAgo(12*time.Minute), "spec.limits[0].max.memory", "1Gi", "256Mi"),
		goldenAdd("ValidatingWebhookConfiguration", "admissionregistration.k8s.io/v1", "", "pod-policy", goldenAgo(5*time.Minute)),
		goldenAdd("Deployment", "apps/v1", "fresh", "fresh-api", goldenAgo(10*time.Minute+5*time.Second)),
		goldenAdd("ResourceQuota", "v1", "fresh", "fresh-quota", goldenAgo(10*time.Minute)),
		goldenUpdate("Deployment", "apps/v1", "crowd", "crowd-03", goldenAgo(7*time.Minute), "spec.replicas", int64(1), int64(2)),
	}
	for i := 0; i <= meaningfulchanges.NamedFetchBound; i++ {
		events = append(events, goldenUpdate("Deployment", "apps/v1", "shop", "churn", goldenAgo(time.Duration(i+1)*time.Second), "status.readyReplicas", int32(i%2), int32((i+1)%2)))
	}
	// Recorded after the status churn, so it is inside the fetch bound even
	// though the fetch saturates.
	events = append(events, goldenUpdate("Deployment", "apps/v1", "shop", "churn", goldenAgo(time.Duration(meaningfulchanges.NamedFetchBound/2)*time.Second+500*time.Millisecond), "spec.template.spec.containers[churn].env", "a", "b"))
	return events
}

func appendGolden(t *testing.T, store timeline.EventStore, events []timeline.TimelineEvent) {
	t.Helper()
	for _, e := range events {
		appendChange(t, store, e)
	}
}

func goldenStoreShop(t *testing.T) {
	store := initGoldenMemoryStore(t, 2000, 2*time.Hour)
	appendGolden(t, store, goldenShopChanges())
}

// goldenStoreYoung has watched for four minutes: changes it recorded are
// evidence, and no absence is claimed.
func goldenStoreYoung(t *testing.T) {
	store := initGoldenMemoryStore(t, 2000, 4*time.Minute)
	var recent []timeline.TimelineEvent
	for _, e := range goldenShopChanges() {
		if !e.Timestamp.Before(goldenAgo(4 * time.Minute)) {
			recent = append(recent, e)
		}
	}
	appendGolden(t, store, recent)
}

// goldenStoreEvicted's ring has dropped events from inside the window.
func goldenStoreEvicted(t *testing.T) {
	store := initGoldenMemoryStore(t, 40, 2*time.Hour)
	for i := range 40 {
		appendChange(t, store, goldenUpdate("Deployment", "apps/v1", "noise", "busy", goldenAgo(time.Duration(50*60-i)*time.Second), "spec.replicas", int64(i), int64(i+1)))
	}
	var kept []timeline.TimelineEvent
	for _, e := range goldenShopChanges() {
		if e.Name != "churn" {
			kept = append(kept, e)
		}
	}
	appendGolden(t, store, kept)
}

func initGoldenCluster(t *testing.T) {
	t.Helper()
	created := metav1.NewTime(goldenAgo(3 * time.Hour))
	controller := true
	replicas := func(n int32) *int32 { return &n }
	var objs []runtime.Object
	objs = append(objs,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", CreationTimestamp: created}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "admit", CreationTimestamp: created}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "crowd", CreationTimestamp: created}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fresh", CreationTimestamp: created}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "fresh-quota", Namespace: "fresh", CreationTimestamp: metav1.NewTime(goldenAgo(10 * time.Minute))}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other", CreationTimestamp: created}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "flagd-config", Namespace: "shop", CreationTimestamp: created}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "flagd-creds", Namespace: "shop", CreationTimestamp: created}, Data: map[string][]byte{"token": []byte("x")}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "compute", Namespace: "admit", CreationTimestamp: metav1.NewTime(goldenAgo(10 * time.Minute))}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "limits", Namespace: "admit", CreationTimestamp: created}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "shop", CreationTimestamp: created},
			Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "nothing"}, Ports: []corev1.ServicePort{{Port: 80}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cart", Namespace: "shop", CreationTimestamp: created},
			Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "cartt"}, Ports: []corev1.ServicePort{{Port: 80}}}},
	)
	crashloop := func(namespace, name string, podSpec corev1.PodSpec, owner metav1.OwnerReference) {
		if owner.Kind == "" {
			owner = metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: types.UID("deploy-uid-" + name), Controller: &controller}
			objs = append(objs, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, CreationTimestamp: created, UID: owner.UID},
				Spec: appsv1.DeploymentSpec{Replicas: replicas(1), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
					Template: corev1.PodTemplateSpec{Spec: podSpec}},
				Status: appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1},
			})
		}
		objs = append(objs,
			&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name + "-rs", Namespace: namespace, CreationTimestamp: created, UID: types.UID("rs-uid-" + name),
				OwnerReferences: []metav1.OwnerReference{owner}}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name + "-rs-1", Namespace: namespace, CreationTimestamp: created, UID: types.UID("pod-uid-" + name),
				Labels:          map[string]string{"app": name},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name + "-rs", UID: types.UID("rs-uid-" + name), Controller: &controller}}},
				Spec: podSpec,
				Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: podSpec.Containers[0].Name, RestartCount: 7,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off restarting failed container"}}}}}},
		)
	}
	plain := func(name string) corev1.PodSpec {
		return corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: name + ":2"}}}
	}
	crashloop("shop", "web", plain("web"), metav1.OwnerReference{})
	crashloop("shop", "quiet", plain("quiet"), metav1.OwnerReference{})
	crashloop("shop", "churn", plain("churn"), metav1.OwnerReference{})
	crashloop("shop", "flagd", corev1.PodSpec{
		Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "flagd-config"}}}}},
		Containers: []corev1.Container{{Name: "flagd", Image: "flagd:1", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "flagd-creds"}, Key: "token"}}}}}},
	}, metav1.OwnerReference{})
	// A Rollout that isn't in the cache: its pod template can't be read.
	crashloop("shop", "canary", plain("canary"), metav1.OwnerReference{APIVersion: "argoproj.io/v1alpha1", Kind: "Rollout", Name: "canary", UID: "rollout-uid-canary", Controller: &controller})
	for i := range 12 {
		name := fmt.Sprintf("crowd-%02d", i)
		crashloop("crowd", name, plain(name), metav1.OwnerReference{})
	}
	crashloop("other", "elsewhere", plain("elsewhere"), metav1.OwnerReference{})

	admissionFailure := func(namespace, name string, createdAt metav1.Time, failedAt time.Time, message string) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, CreationTimestamp: createdAt, UID: types.UID("deploy-uid-admission-" + name)},
			Spec:       appsv1.DeploymentSpec{Replicas: replicas(4), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}},
			Status: appsv1.DeploymentStatus{Replicas: 2, UpdatedReplicas: 2, Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate", Message: message,
				LastTransitionTime: metav1.NewTime(failedAt),
			}}},
		}
	}
	objs = append(objs,
		admissionFailure("admit", "search", created, goldenAgo(9*time.Minute), `pods "search-7d996-j2wkp" is forbidden: exceeded quota: compute, requested: pods=1, used: pods=2, limited: pods=2`),
		admissionFailure("admit", "hooked", created, goldenAgo(9*time.Minute), `admission webhook "deny-root.policy.example.com" denied the request: runAsNonRoot is required`),
		// Created moments before the quota that rejects it: its at-creation
		// timing is withdrawn, which changes the recent_changes decision.
		admissionFailure("fresh", "fresh-api", metav1.NewTime(goldenAgo(10*time.Minute+5*time.Second)), goldenAgo(10*time.Minute-time.Second),
			`pods "fresh-api-6d4f-x" is forbidden: exceeded quota: fresh-quota, requested: pods=1, used: pods=0, limited: pods=0`),
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "batch-rs", Namespace: "admit", CreationTimestamp: created, UID: "rs-uid-batch"},
			Spec:       appsv1.ReplicaSetSpec{Replicas: replicas(2)},
			Status: appsv1.ReplicaSetStatus{Replicas: 0, Conditions: []appsv1.ReplicaSetCondition{{
				Type: appsv1.ReplicaSetReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate",
				Message:            `pods "batch-rs-x" is forbidden: maximum memory usage per Container is 256Mi, but limit is 1Gi`,
				LastTransitionTime: metav1.NewTime(goldenAgo(11 * time.Minute)),
			}}},
		},
	)
	if err := k8s.InitTestResourceCache(k8sfake.NewSimpleClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)

	vwcGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"}
	mwcGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"}
	config := &admissionv1.ValidatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{Kind: "ValidatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "pod-policy", CreationTimestamp: metav1.NewTime(goldenAgo(5 * time.Minute))},
		Webhooks:   []admissionv1.ValidatingWebhook{{Name: "deny-root.policy.example.com"}},
	}
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(config)
	if err != nil {
		t.Fatal(err)
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{vwcGVR: "ValidatingWebhookConfigurationList", mwcGVR: "MutatingWebhookConfigurationList"},
		&unstructured.Unstructured{Object: data})
	if err := k8s.InitTestDynamicResourceCache(dynamicClient, []k8s.APIResource{
		{Group: vwcGVR.Group, Version: "v1", Kind: "ValidatingWebhookConfiguration", Name: vwcGVR.Resource, Verbs: []string{"list", "watch"}},
		{Group: mwcGVR.Group, Version: "v1", Kind: "MutatingWebhookConfiguration", Name: mwcGVR.Resource, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{vwcGVR, mwcGVR} {
		if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("watch %s: %v", gvr, err)
		}
	}
}

// initGoldenHelm serves one native Helm release in admit whose upgrade to
// revision 2 failed three minutes ago, to callers who may read its Secrets.
func initGoldenHelm(t *testing.T) {
	t.Helper()
	revision := func(version int, status release.Status, description string, at time.Time) corev1.Secret {
		rel := &release.Release{
			Name: "payments", Namespace: "admit", Version: version,
			Info: &release.Info{Status: status, Description: description,
				FirstDeployed: helmtime.Time{Time: goldenAgo(3 * time.Hour)}, LastDeployed: helmtime.Time{Time: at}},
			Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "payments", Version: "1.0." + strconv.Itoa(version), AppVersion: "1.0.0"}},
		}
		payload, err := json.Marshal(rel)
		if err != nil {
			t.Fatal(err)
		}
		return corev1.Secret{
			TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.payments.v" + strconv.Itoa(version), Namespace: "admit", Labels: map[string]string{
				"owner": "helm", "name": "payments", "status": status.String(), "version": strconv.Itoa(version),
			}},
			Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(payload))},
		}
	}
	secrets := []corev1.Secret{
		revision(1, release.StatusDeployed, "Install complete", goldenAgo(3*time.Hour)),
		revision(2, release.StatusFailed, "Upgrade \"payments\" failed: context deadline exceeded", goldenAgo(3*time.Minute)),
	}
	secretList, err := json.Marshal(corev1.SecretList{TypeMeta: metav1.TypeMeta{Kind: "SecretList", APIVersion: "v1"}, Items: secrets})
	if err != nil {
		t.Fatal(err)
	}
	metaList := metav1.PartialObjectMetadataList{TypeMeta: metav1.TypeMeta{Kind: "PartialObjectMetadataList", APIVersion: "meta.k8s.io/v1"}}
	for _, s := range secrets {
		metaList.Items = append(metaList.Items, metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{Kind: "PartialObjectMetadata", APIVersion: "meta.k8s.io/v1"}, ObjectMeta: s.ObjectMeta})
	}
	metaPayload, err := json.Marshal(metaList)
	if err != nil {
		t.Fatal(err)
	}
	empty := []byte(`{"apiVersion":"v1","kind":"SecretList","items":[]}`)
	emptyMeta := []byte(`{"apiVersion":"meta.k8s.io/v1","kind":"PartialObjectMetadataList","items":[]}`)
	storage := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"major":"1","minor":"30","gitVersion":"v1.30.0"}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/secrets") {
			http.NotFound(w, r)
			return
		}
		// Reads arrive impersonating the caller; the restricted caller may not
		// list Secrets, so the API server refuses its release storage reads.
		if r.Header.Get("Impersonate-User") == "golden-restricted" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"secrets is forbidden: User \"golden-restricted\" cannot list resource \"secrets\""}`))
			return
		}
		inStorage := strings.Contains(r.URL.Path, "/namespaces/admit/") || !strings.Contains(r.URL.Path, "/namespaces/")
		if strings.Contains(r.Header.Get("Accept"), "PartialObjectMetadataList") {
			if inStorage {
				_, _ = w.Write(metaPayload)
			} else {
				_, _ = w.Write(emptyMeta)
			}
			return
		}
		if inStorage {
			_, _ = w.Write(secretList)
		} else {
			_, _ = w.Write(empty)
		}
	}))
	// Connections close after each response, so no client goroutine outlives
	// a synctest bubble.
	storage.Config.SetKeepAlivesEnabled(false)
	storage.Start()
	t.Cleanup(storage.Close)
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(&rest.Config{Host: storage.URL}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(helm.ResetClient)
}
