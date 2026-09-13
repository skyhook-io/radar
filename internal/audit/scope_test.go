package audit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReadScopeSeparatesSubjectsAndSecretGrants(t *testing.T) {
	scope := resolveReadScope([]string{"b", "a"}, []string{"b"}, nil, nil)
	for _, tc := range []struct{ selected, want []string }{
		{nil, []string{"a", "b"}}, {[]string{}, []string{"a", "b"}}, {[]string{"b", "outside"}, []string{"b"}}, {[]string{"outside"}, []string{}},
	} {
		if got := scope.subjectNamespaces(tc.selected); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("selected %v: %v, want %v", tc.selected, got, tc.want)
		}
	}
	secrets := schema.GroupVersionResource{Resource: "secrets"}
	if scope.allows(secrets, "a") || !scope.allows(secrets, "b") || scope.allows(secrets, "outside") {
		t.Fatal("secret scope widened")
	}
	if scope.hasSecretSubjects([]string{"a"}) || !scope.hasSecretSubjects(nil) {
		t.Fatal("secret subject intersection incorrect")
	}
	if !scope.allows(schema.GroupVersionResource{Resource: "pods"}, "a") {
		t.Fatal("namespace policy changed")
	}
	all := resolveReadScope(nil, nil, nil, nil)
	none := resolveReadScope([]string{}, []string{}, nil, nil)
	a, _ := json.Marshal(all)
	n, _ := json.Marshal(none)
	if string(a) == string(n) || !all.allows(secrets, "any") || none.allows(secrets, "any") {
		t.Fatal("nil and empty scopes conflated")
	}
}

func TestReadScopeWatchedGrantsWithoutDiscovery(t *testing.T) {
	var calls atomic.Int64
	gvr := schema.GroupVersionResource{Group: "database.example.com", Version: "v1", Resource: "databases"}
	other := gvr
	other.Version = "v2"
	scope := resolveReadScope([]string{"app"}, nil, []schema.GroupVersionResource{gvr, other, {Group: "pkg.crossplane.io", Resource: "providers"}}, func(group, resource, ns string) bool {
		calls.Add(1)
		if group != gvr.Group || resource != gvr.Resource || ns != "" {
			t.Errorf("unexpected SAR: %s/%s/%s", group, resource, ns)
		}
		return true
	})
	if calls.Load() != 1 || !scope.allows(gvr, "") {
		t.Fatal("watched grant lost or duplicated without discovery")
	}
	empty := resolveReadScope([]string{}, nil, []schema.GroupVersionResource{gvr}, func(string, string, string) bool {
		t.Error("no namespace access should not resolve grants")
		return true
	})
	if empty.Namespaces == nil {
		t.Fatal("empty scope widened")
	}
	denied := resolveReadScope(nil, nil, []schema.GroupVersionResource{gvr}, func(string, string, string) bool { return false })
	if denied.allows(gvr, "") {
		t.Fatal("denied cluster subject allowed")
	}
}

func BenchmarkResolveReadScopeWatchedKinds(b *testing.B) {
	watched := make([]schema.GroupVersionResource, 1000)
	for i := range watched {
		watched[i] = schema.GroupVersionResource{Group: "example.com", Resource: fmt.Sprintf("kind-%d", i)}
	}
	for b.Loop() {
		resolveReadScope(nil, nil, watched, func(string, string, string) bool { return true })
	}
}

func TestRunScopeFiltersSubjectsWithoutChangingConsumerEvidence(t *testing.T) {
	now := metav1.NewTime(time.Now().Add(-time.Hour))
	objects := []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "used"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "b", Name: "private", DeletionTimestamp: &now, Finalizers: []string{"test/hold"}}},
	}
	client := fake.NewClientset(objects[0], objects[1],
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "settings"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "consumer"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app:1", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "used"}}}}}}}},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	cache := k8s.GetResourceCache()
	full := RunFromCache(cache, nil, nil)
	emptyView := RunFromCache(cache, []string{}, nil)
	if !reflect.DeepEqual(full.Findings, emptyView.Findings) || !reflect.DeepEqual(full.CheckCounts, emptyView.CheckCounts) {
		t.Fatal("local empty namespace selection stopped meaning all")
	}
	fullyAuthorized := RunFromCache(cache, []string{}, &RunOptions{Scope: &ReadScope{}})
	if !reflect.DeepEqual(full.Findings, fullyAuthorized.Findings) || !reflect.DeepEqual(full.CheckCounts, fullyAuthorized.CheckCounts) {
		t.Fatal("full access changed")
	}
	restricted := RunFromCache(cache, nil, &RunOptions{Scope: &ReadScope{Namespaces: []string{"a", "b"}, SecretNamespaces: []string{"a"}}})
	visible := func(result *bp.ScanResults) map[string]bool {
		out := map[string]bool{}
		for _, f := range result.Findings {
			if f.CheckID == "orphanConfigMapSecret" && !(f.Kind == "Secret" && f.Namespace == "b") {
				out[f.Kind+"/"+f.Namespace+"/"+f.Name] = true
			}
		}
		return out
	}
	if !reflect.DeepEqual(visible(full), visible(restricted)) {
		t.Fatal("subject filtering changed retained orphan findings")
	}
	for _, f := range restricted.Findings {
		if f.Kind == "Secret" && f.Namespace == "b" {
			t.Fatalf("hidden Secret: %+v", f)
		}
	}
	if full.CheckCounts["orphanConfigMapSecret"].Evaluated-restricted.CheckCounts["orphanConfigMapSecret"].Evaluated != 1 {
		t.Fatal("hidden Secret remains counted")
	}
	denied := RunFromCache(cache, []string{"a"}, &RunOptions{Scope: &ReadScope{Namespaces: []string{"a", "b"}, SecretNamespaces: []string{"b"}}})
	if !slices.Contains(denied.MissingInputs, "secrets") {
		t.Fatal("total subject-scope denial not missing")
	}
	emptyAllowed := RunFromCache(cache, []string{"empty"}, &RunOptions{Scope: &ReadScope{SecretNamespaces: []string{"empty"}}})
	if slices.Contains(emptyAllowed.MissingInputs, "secrets") {
		t.Fatal("authorized empty inventory treated as denied")
	}
	noAccess := RunFromCache(cache, []string{"outside"}, &RunOptions{Scope: &ReadScope{Namespaces: []string{"a"}}})
	if len(noAccess.Findings) > 0 || len(noAccess.MissingInputs) > 0 {
		t.Fatal("no access should return empty transport shape")
	}
}
