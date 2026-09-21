package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestLimitRangeResourceAccess(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "rules", Namespace: "default"}},
	))
	tests := []struct {
		name, path, verb string
		namespaces       []string
		grants           map[string]bool
		want             int
	}{
		{"list denied", "/api/resources/limitranges?namespace=default", "list", []string{"default"}, map[string]bool{"default": false}, 403},
		{"list allowed", "/api/resources/limitranges?namespace=default", "list", []string{"default"}, map[string]bool{"default": true}, 200},
		{"get denied", "/api/resources/limitranges/default/rules", "get", []string{"default"}, map[string]bool{"default": false}, 403},
		{"get allowed", "/api/resources/limitranges/default/rules", "get", []string{"default"}, map[string]bool{"default": true}, 200},
		{"namespace denied is not empty", "/api/resources/limitranges?namespace=default", "list", []string{"other"}, map[string]bool{"default": true}, 403},
		{"cluster pod visibility does not grant rules", "/api/resources/limitranges", "list", nil, map[string]bool{"": false}, 403},
		{"cluster grant", "/api/resources/limitranges", "list", nil, map[string]bool{"": true}, 200},
		{"mixed grants cannot claim completeness", "/api/resources/limitranges?namespaces=default,other", "list", []string{"default", "other"}, map[string]bool{"default": true, "other": false}, 403},
		{"table denial is explicit", "/api/resources/limitranges?namespace=default&table=1", "list", []string{"default"}, map[string]bool{"default": false}, 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newAuthTestServer(t)
			perms := &auth.UserPermissions{AllowedNamespaces: tt.namespaces}
			for ns, allowed := range tt.grants {
				perms.SetCanI(tt.verb, "", "limitranges", ns, allowed)
			}
			env.srv.permCache.Set("reader", nil, perms)
			resp := env.authGet(t, tt.path, "reader", "")
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if tt.want == http.StatusForbidden {
				var body map[string]string
				if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body["error"] == "" {
					t.Fatalf("expected explicit error, got %v (%v)", body, err)
				}
			} else if tt.verb == "list" {
				var items []corev1.LimitRange
				if err := json.NewDecoder(resp.Body).Decode(&items); err != nil || len(items) != 1 || items[0].Name != "rules" {
					t.Fatalf("expected rules, got %v (%v)", items, err)
				}
			}
		})
	}
}

func TestLimitRangeCacheCoverage(t *testing.T) {
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewClientset(),
		ResourceScopes: map[string]k8score.ResourceScope{
			k8score.LimitRanges: {Enabled: true, Namespace: "default"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetResourceCache()
	original := cache.ResourceCache
	cache.ResourceCache = core
	t.Cleanup(func() { cache.ResourceCache = original; core.Stop() })

	for _, tt := range []struct {
		path string
		want int
	}{
		{"/api/resources/limitranges?namespace=default", 200},
		{"/api/resources/limitranges?namespace=other", 403},
		{"/api/resources/limitranges?namespaces=default,other", 403},
		{"/api/resources/limitranges", 403},
		{"/api/resources/limitranges/other/unknown", 403},
		{"/api/resources/limitranges/default/unknown", 404},
	} {
		t.Run(tt.path, func(t *testing.T) {
			resp, err := http.Get(testServer.URL + tt.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

func TestLimitRangeCacheWarming(t *testing.T) {
	client := fake.NewClientset()
	release := make(chan struct{})
	client.PrependReactor("list", "limitranges", func(action k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return true, &corev1.LimitRangeList{}, nil
	})
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         client,
		ResourceScopes: map[string]k8score.ResourceScope{k8score.LimitRanges: {Enabled: true}},
		DeferredTypes:  map[string]bool{k8score.LimitRanges: true},
	})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	cache := k8s.GetResourceCache()
	original := cache.ResourceCache
	cache.ResourceCache = core
	t.Cleanup(func() { close(release); cache.ResourceCache = original; core.Stop() })
	for _, path := range []string{"/api/resources/limitranges?namespace=default", "/api/resources/limitranges/default/rules"} {
		resp, err := http.Get(testServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d, want 503", path, resp.StatusCode)
		}
	}
}
