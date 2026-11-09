package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestResourceSelectionUsesCapturedNamespaceCache(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "read-a"}}))
	started := readCache()
	useTestResourceCache(t, fake.NewClientset())
	request := httptest.NewRequest("GET", "/api/resources/services/read-a/api", nil)
	selection, code := testServerSrv.requestNamespaceSelection(request, started, []string{"read-a"})
	if code != "" || len(selection.Gaps) != 0 {
		t.Fatalf("selection borrowed replacement namespace facts: %+v, %s", selection, code)
	}
	failure := testServerSrv.preflightResourceGetFrom(request, started, "services", "read-a", "api", "")
	if failure == nil || failure.code != readreason.ConnectionChanged {
		t.Fatalf("stale target failure: %v", failure)
	}
	if k8s.GetResourceCache() == started {
		t.Fatal("fixture did not switch")
	}
}

func TestResourceScopeFailureAfterConnectionChange(t *testing.T) {
	for _, path := range []string{"/api/resources/services/read-a/api", "/api/resources/services?namespace=read-a", "/api/ai/resources/services/read-a/api?context=none", "/api/resource-counts?namespace=read-a"} {
		t.Run(path, func(t *testing.T) {
			useTestResourceCache(t, fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "read-a"}}))
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
			env := newAuthTestServer(t)
			response := make(chan *http.Response, 1)
			go func() { response <- env.authGet(t, path, "switching-reader", "") }()
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
			case resp := <-response:
				assertResourceReadError(t, resp, http.StatusConflict, "connection_changed")
			case <-time.After(5 * time.Second):
				t.Fatal("request did not finish")
			}
		})
	}
}
