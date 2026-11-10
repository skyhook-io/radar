package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCronJobSchedulingEvidenceCoverageEnvelope(t *testing.T) {
	now := time.Now()
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "ci", UID: "current-cronjob", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Spec: batchv1.CronJobSpec{Schedule: "0 0 31 2 *"}}
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "warning", Namespace: "ci", UID: "warning-id"}, InvolvedObject: corev1.ObjectReference{Kind: "CronJob", APIVersion: "batch/v1", Namespace: "ci", Name: "nightly", UID: cj.UID}, Type: "Warning", Reason: "InvalidSchedule", Message: "original report", LastTimestamp: metav1.NewTime(now.Add(-time.Minute))}
	useTestResourceCache(t, fake.NewClientset(cj, event))
	s := newAuthServer(auth.Config{Mode: "proxy"})
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"ci"}}
	permissions.SetCanI("list", "", "events", "ci", true)
	s.permCache.Set("pg-user", nil, permissions)
	path := "/api/issues/resource/CronJob/ci/nightly?group=batch&coverage=1"
	w := resourceIssuesRequest(t, s, path)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	var response ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SchedulingReport == nil || response.SchedulingReport.UID != string(cj.UID) || response.SchedulingReport.Message != event.Message {
		t.Fatalf("missing retained evidence: %s", w.Body.String())
	}
	permissions.SetCanI("list", "", "events", "ci", false)
	w = resourceIssuesRequest(t, s, path)
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["scheduling_report"]; present {
		t.Fatal("Event evidence exposed to a caller denied Events")
	}
	// The original array-shaped endpoint is unchanged for existing clients.
	w = resourceIssuesRequest(t, s, "/api/issues/resource/CronJob/ci/nightly?group=batch")
	var original []json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &original); err != nil {
		t.Fatalf("legacy shape changed: %s", w.Body.String())
	}
}
