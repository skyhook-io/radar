package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestResourceSelectionRejectsCapturedCacheAfterConnectionChange(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "read-a"}})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	started := currentReadCache()
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveReadTargetFrom(context.Background(), started, []string{"read-a"}); err == nil || !strings.HasPrefix(err.Error(), "connection_changed:") {
		t.Fatalf("stale namespace selection: %v", err)
	}
}

func TestResourceScopeFailureAfterConnectionChange(t *testing.T) {
	for _, verb := range []string{"get", "list"} {
		t.Run(verb, func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "read-a"}})); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(entered); <-release })
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(&authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}})
			}))
			defer api.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
			if err != nil {
				t.Fatal(err)
			}
			previous := k8s.SetTestClient(client)
			defer k8s.SetTestClient(previous)
			ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "switching-reader-" + verb})
			result := make(chan error, 1)
			go func() {
				var err error
				if verb == "get" {
					_, _, err = handleGetResource(ctx, nil, getResourceInput{Kind: "services", Namespace: "read-a", Name: "api", Context: "none"})
				} else {
					_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "services", Namespace: "read-a", Context: "none"})
				}
				result <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("authorization did not start")
			}
			k8s.ResetResourceCache()
			err = k8s.InitTestResourceCache(fake.NewClientset())
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err == nil || !strings.HasPrefix(err.Error(), "connection_changed:") {
					t.Fatalf("connection change: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("request did not finish")
			}
		})
	}
}
