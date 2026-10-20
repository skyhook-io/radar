package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestIssuesSecretPreviousValueRequiresPodRead(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "dummy-last-applied-secret"}}, Data: map[string][]byte{"password": []byte("dummy-secret-value")}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "shop"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "password"}}}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-10 * time.Minute))}}}}}}
	k8s.ResetResourceCache()
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(secret, pod)); err != nil {
		t.Fatal(err)
	}
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: prev.Context})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	timeline.ResetStore()
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatal(err)
		}
	})
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	timeline.SetObservationStartForTest(now.Add(-2 * time.Hour))
	old := secret.DeepCopy()
	old.Data["password"] = []byte("dummy-old-secret-value")
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{ID: "secret-write", Kind: "Secret", APIVersion: "v1", Name: "db", Namespace: "shop", Timestamp: now.Add(-time.Minute), ClusterContext: k8s.ActiveClusterContext(), Source: timeline.SourceInformer, EventType: timeline.EventTypeUpdate, Diff: k8s.ComputeDiff("Secret", old, secret)}); err != nil {
		t.Fatal(err)
	}
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"shop"}}
	perms.SetCanI("list", "", "secrets", "shop", true)
	s.permCache.Set("secret-reader", nil, perms)
	if viewer := s.recentChangeViewer(context.Background()); viewer.CanReadPods != nil || viewer.CanReadConsumer != nil {
		t.Fatal("auth-off viewer must be unrestricted")
	}
	for _, readable := range []bool{true, false} {
		perms.SetCanI("get", "", "pods", "shop", readable)
		r := requestWithUser(http.MethodGet, "/api/issues?namespace=shop", &auth.User{Username: "secret-reader"})
		w := httptest.NewRecorder()
		s.handleIssues(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		for _, sentinel := range []string{"dummy-secret-value", "dummy-old-secret-value", "dummy-last-applied-secret", "kubectl.kubernetes.io/last-applied-configuration"} {
			if strings.Contains(body, sentinel) {
				t.Fatal("response contains forbidden Secret material")
			}
		}
		var response issuesapi.Response
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.RecentChanges) != 1 || (response.RecentChanges[0].RunningPreviousValue != nil) != readable {
			t.Fatalf("pods readable=%v changes=%+v", readable, response.RecentChanges)
		}
		row := response.RecentChanges[0]
		// The caller may not list Pods, so the bare Pod is counted, never named.
		withheld := &issuesapi.Withheld{Count: 1, Reason: readreason.UserForbidden}
		if len(row.ConsumedBy) != 0 || !reflect.DeepEqual(row.ConsumedByWithheld, withheld) {
			t.Fatalf("pods readable=%v: consumed_by=%v withheld=%+v", readable, row.ConsumedBy, row.ConsumedByWithheld)
		}
		if readable && (len(row.RunningPreviousValue.Workloads) != 0 || !reflect.DeepEqual(row.RunningPreviousValue.Withheld, withheld) || row.RunningPreviousValue.Pods != 1) {
			t.Fatalf("stale reader not withheld: %+v", row.RunningPreviousValue)
		}
	}
}
