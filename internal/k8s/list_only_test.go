package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// newListOnlyCache builds a cache over Pods, Services, Secrets and ConfigMaps
// whose informers for listOnly may list but not watch, and waits until the
// cache has noticed.
func newListOnlyCache(t *testing.T, listOnly []string, objects ...runtime.Object) *ResourceCache {
	t.Helper()
	return newListOnlyCacheStaleAfter(t, 0, listOnly, objects...)
}

func newListOnlyCacheStaleAfter(t *testing.T, staleAfter time.Duration, listOnly []string, objects ...runtime.Object) *ResourceCache {
	t.Helper()
	client := fake.NewClientset(objects...)
	for _, resource := range listOnly {
		gr := schema.GroupResource{Resource: resource}
		client.PrependWatchReactor(resource, func(k8stesting.Action) (bool, watch.Interface, error) {
			return true, nil, apierrors.NewForbidden(gr, "", errors.New("watch is not allowed"))
		})
	}
	scopes := map[string]k8score.ResourceScope{}
	types := map[string]bool{}
	for _, key := range []string{k8score.Pods, k8score.Services, k8score.Secrets, k8score.ConfigMaps} {
		scopes[key] = k8score.ResourceScope{Enabled: true}
		types[key] = true
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         client,
		ResourceTypes:  types,
		DeferredTypes:  map[string]bool{},
		ResourceScopes: scopes,

		ListOnlyStaleAfter: staleAfter,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	for _, resource := range listOnly {
		deadline := time.Now().Add(10 * time.Second)
		for !core.KindListOnly(resource, "shop") {
			if time.Now().After(deadline) {
				t.Fatalf("%s informer never reported list-only", resource)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	cacheMu.Lock()
	gen := cacheGeneration
	cacheMu.Unlock()
	return &ResourceCache{ResourceCache: core, generation: gen}
}

func newListOnlySecretsCache(t *testing.T, listOnly bool, objects ...runtime.Object) *ResourceCache {
	t.Helper()
	if listOnly {
		return newListOnlyCache(t, []string{k8score.Secrets}, objects...)
	}
	return newListOnlyCache(t, nil, objects...)
}

func secretRefPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: "db", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "db-conn"}}}},
			Containers: []corev1.Container{{
				Name: "app",
				Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "app"}, Key: "token"},
				}}},
			}},
		},
	}
}

func secretDetections(dets []Detection) []string {
	var out []string
	for _, d := range dets {
		if strings.Contains(d.Message, "Secret") {
			out = append(out, d.Reason+": "+d.Message)
		}
	}
	return out
}

// A list-only Secret cache is a periodic snapshot: an object (or key) missing
// from it may have been created since the last LIST, or the LIST may have
// stalled hours ago. Neither is reported as missing.
func TestDetectMissingRefs_ListOnlySecretsAreNotEvidence(t *testing.T) {
	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "shop"}, Data: map[string][]byte{"old": []byte("x")}}

	watched := newListOnlySecretsCache(t, false, secretRefPod(), existing)
	if got := secretDetections(DetectMissingRefs(watched, "shop")); len(got) != 2 {
		t.Fatalf("watched Secrets cache: want Missing Secret + missing key, got %q", got)
	}

	listOnly := newListOnlySecretsCache(t, true, secretRefPod(), existing)
	if got := secretDetections(DetectMissingRefs(listOnly, "shop")); len(got) != 0 {
		t.Fatalf("list-only Secrets cache must not report missing Secrets or keys, got %q", got)
	}
}

func TestFetchResourceConfirmed_ListOnlyMiss(t *testing.T) {
	defer ResetTestDynamicState()
	cache := newListOnlySecretsCache(t, true)

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	created := &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: "created-after-list", Namespace: "shop", ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl"}}},
		Data:       map[string][]byte{"password": []byte("hunter2")},
	}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, created)
	if err := InitTestDynamicResourceCache(dyn, nil); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	ctx := context.Background()

	if _, err := FetchResource(cache, "secrets", "shop", "created-after-list"); !apierrors.IsNotFound(err) {
		t.Fatalf("precondition: the cache misses the Secret, got %v", err)
	}
	obj, note, err := FetchResourceConfirmed(ctx, cache, "secret", "shop", "created-after-list")
	if err != nil || note != "" {
		t.Fatalf("direct read should find the Secret: obj=%v note=%q err=%v", obj, note, err)
	}
	secret, ok := obj.(*corev1.Secret)
	if !ok || secret.Name != "created-after-list" || len(secret.ManagedFields) != 0 {
		t.Fatalf("want a typed Secret stripped like the cache, got %#v", obj)
	}

	if _, _, err := FetchResourceConfirmed(ctx, cache, "secrets", "shop", "absent"); !apierrors.IsNotFound(err) {
		t.Fatalf("absent on the server too: want NotFound, got %v", err)
	}

	dyn.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "absent", errors.New("get is not allowed"))
	})
	_, _, err = FetchResourceConfirmed(ctx, cache, "secrets", "shop", "absent")
	var unconfirmed *CacheUnconfirmedError
	if !errors.As(err, &unconfirmed) || apierrors.IsNotFound(err) {
		t.Fatalf("a failed confirmation must not read as not-found, got %v", err)
	}
	if !strings.Contains(err.Error(), "periodic list") {
		t.Fatalf("error should say why the cache could not answer: %v", err)
	}
}

// After a context switch the dynamic cache belongs to another cluster; a
// confirmation read through it must not answer for the old cache.
func TestFetchResourceConfirmed_RefusesAfterContextSwitch(t *testing.T) {
	defer ResetTestDynamicState()
	cache := newListOnlySecretsCache(t, true)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	other := &corev1.Secret{TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{Name: "only-on-b", Namespace: "shop"}}
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(scheme, other), nil); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	cacheMu.Lock()
	cacheGeneration++
	cacheMu.Unlock()

	_, _, err := FetchResourceConfirmed(context.Background(), cache, "secrets", "shop", "only-on-b")
	var unconfirmed *CacheUnconfirmedError
	if !errors.As(err, &unconfirmed) {
		t.Fatalf("want an unconfirmed error after the generation moved, got %v", err)
	}
}

// Kinds with a live watch keep the plain cache read: no API call on a miss.
func TestFetchResourceConfirmed_WatchedKindUsesCache(t *testing.T) {
	cache := newListOnlySecretsCache(t, false)
	if _, _, err := FetchResourceConfirmed(context.Background(), cache, "secrets", "shop", "absent"); !apierrors.IsNotFound(err) {
		t.Fatalf("want the cache's NotFound, got %v", err)
	}
}

// A Service whose selector matches nothing in a list-only Pod snapshot is not
// reported: the pods may have been created since the last LIST.
func TestServiceSelectorMatchesNoPods_ListOnlyPods(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	now := time.Now()
	watched := newListOnlyCache(t, nil, svc)
	if got := detectServiceBackendProblems(watched, svc, nil, now); len(got) != 1 || got[0].Reason != SelectorMatchesNoPodsReason {
		t.Fatalf("watched Pods: want %q, got %+v", SelectorMatchesNoPodsReason, got)
	}
	listOnly := newListOnlyCache(t, []string{k8score.Pods}, svc)
	if got := detectServiceBackendProblems(listOnly, svc, nil, now); len(got) != 0 {
		t.Fatalf("list-only Pods: want no finding, got %+v", got)
	}
}

// A stale cache hit is re-read from the API server; when that read fails the
// cached copy is still served, with a warning that it may be out of date. A
// confirmed 404 wins over the stale copy.
func TestFetchResourceConfirmed_StaleHit(t *testing.T) {
	defer ResetTestDynamicState()
	cached := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}}
	cache := newListOnlyCacheStaleAfter(t, time.Nanosecond, []string{k8score.Secrets}, cached)
	deadline := time.Now().Add(5 * time.Second)
	for !cache.KindStale(k8score.Secrets, "shop") {
		if time.Now().After(deadline) {
			t.Fatal("Secrets never reported stale")
		}
		time.Sleep(10 * time.Millisecond)
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	dyn := dynamicfake.NewSimpleDynamicClient(scheme)
	if err := InitTestDynamicResourceCache(dyn, nil); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	ctx := context.Background()

	if _, _, err := FetchResourceConfirmed(ctx, cache, "secrets", "shop", "db"); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted on the server: want NotFound over the stale copy, got %v", err)
	}

	dyn.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("apiserver unavailable")
	})
	obj, note, err := FetchResourceConfirmed(ctx, cache, "secrets", "shop", "db")
	if err != nil || obj == nil || !strings.Contains(note, "may be out of date") {
		t.Fatalf("want the cached copy with a staleness note, got obj=%v note=%q err=%v", obj, note, err)
	}

	// After a context switch neither the read nor the stale copy may answer.
	cacheMu.Lock()
	cacheGeneration++
	cacheMu.Unlock()
	var unconfirmed *CacheUnconfirmedError
	if obj, _, err := FetchResourceConfirmed(ctx, cache, "secrets", "shop", "db"); obj != nil || !errors.As(err, &unconfirmed) {
		t.Fatalf("after a context switch: want an unconfirmed error and no object, got obj=%v err=%v", obj, err)
	}

	// Mid-switch the dynamic cache is already gone; still no stale fallback.
	ResetTestDynamicState()
	if obj, _, err := FetchResourceConfirmed(ctx, cache, "secrets", "shop", "db"); obj != nil || !errors.As(err, &unconfirmed) {
		t.Fatalf("after teardown: want an unconfirmed error and no object, got obj=%v err=%v", obj, err)
	}
}
