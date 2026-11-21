package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestWorkloadMetricsScopeRequiresAuthenticatedTunnel(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/agent/workload-metrics-scope/Deployment/team/web", nil)
	req.Header.Set("X-Forwarded-User", "u_example")
	handler := http.HandlerFunc(s.handleWorkloadMetricsScope)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("direct request: got %d, want 404", w.Code)
	}
	w = httptest.NewRecorder()
	cloud.AuthenticatedTunnelHandler(handler).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "workload_metrics_identity_required") {
		t.Fatalf("tunnel without caller identity: got %d %s", w.Code, w.Body)
	}
}

func TestWorkloadMetricsScopeChecksExactOwnerAndPodList(t *testing.T) {
	createdAt := time.Unix(1720000000, 0).UTC()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	owner := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Namespace: "team", Name: "web", CreationTimestamp: metav1.NewTime(createdAt),
	}}
	for _, name := range []string{"allowed", "workload denied", "pods denied", "different namespace"} {
		t.Run(name, func(t *testing.T) {
			client := dynamicfake.NewSimpleDynamicClient(scheme, owner)
			namespace := "team"
			if name == "different namespace" {
				namespace = "other"
			}
			if name == "workload denied" {
				client.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "web", nil)
				})
			}
			if name == "pods denied" {
				client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "", nil)
				})
			}
			got, err := workloadMetricsCreationTime(t.Context(), client, "deployments", namespace, "web")
			switch name {
			case "allowed":
				if err != nil || !got.Equal(createdAt) {
					t.Fatalf("got (%s, %v)", got, err)
				}
			case "different namespace":
				if !apierrors.IsNotFound(err) || !got.IsZero() {
					t.Fatalf("got (%s, %v), want scoped not found", got, err)
				}
			default:
				if !apierrors.IsForbidden(err) || !got.IsZero() {
					t.Fatalf("got (%s, %v), want denial", got, err)
				}
			}
			actions := client.Actions()
			if actions[0].GetNamespace() != namespace || actions[0].GetResource().Resource != "deployments" {
				t.Fatalf("unscoped workload read: %v", actions)
			}
			if name == "allowed" && (len(actions) != 2 || actions[1].GetNamespace() != namespace || actions[1].GetResource().Resource != "pods") {
				t.Fatalf("unscoped pod list: %v", actions)
			}
		})
	}
}
