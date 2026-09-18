package k8score

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCreateNodeDebugPodIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces/default/pods" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var pod corev1.Pod
		if err := json.NewDecoder(r.Body).Decode(&pod); err != nil {
			t.Error(err)
		}
		if pod.Spec.NodeName != "node-a" || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 3600 {
			t.Errorf("unexpected pod spec: %+v", pod.Spec)
		}
		pod.UID = types.UID("created-uid")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pod)
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := CreateNodeDebugPod(context.Background(), client, "node-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.UID != "created-uid" || result.Namespace != "default" || result.PodName == "" || result.ContainerName != "debug" {
		t.Fatalf("unexpected result: %+v", result)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["uid"] != "created-uid" {
		t.Fatalf("UID missing from JSON: %s", data)
	}
}

func TestDeleteNodeDebugPod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reason metav1.StatusReason
	}{
		{"success", http.StatusOK, ""},
		{"already gone", http.StatusNotFound, metav1.StatusReasonNotFound},
		{"UID conflict", http.StatusConflict, metav1.StatusReasonConflict},
		{"forbidden", http.StatusForbidden, metav1.StatusReasonForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/namespaces/default/pods/session-a" || r.URL.RawQuery != "" {
					t.Errorf("must delete exact pod, not a collection: %s %s", r.Method, r.URL)
				}
				var options metav1.DeleteOptions
				if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
					t.Error(err)
				}
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "fdde0bca-d4df-4263-bf04-88ce73cc90c1" {
					t.Errorf("missing UID precondition: %+v", options)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				json.NewEncoder(w).Encode(metav1.Status{Status: "Failure", Reason: tc.reason, Code: int32(tc.status), Message: "test response"})
			}))
			defer server.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
			if err != nil {
				t.Fatal(err)
			}
			err = DeleteNodeDebugPod(context.Background(), client, "default", "session-a", "fdde0bca-d4df-4263-bf04-88ce73cc90c1")
			switch tc.status {
			case http.StatusOK, http.StatusNotFound:
				if err != nil {
					t.Fatal(err)
				}
			case http.StatusConflict:
				if !apierrors.IsConflict(err) {
					t.Fatalf("expected conflict, got %v", err)
				}
			case http.StatusForbidden:
				if !apierrors.IsForbidden(err) {
					t.Fatalf("expected forbidden, got %v", err)
				}
			}
			if requests != 1 {
				t.Fatalf("expected exactly one guarded request, got %d", requests)
			}
		})
	}
}

func TestDeleteNodeDebugPodRequiresIdentity(t *testing.T) {
	for i, identity := range [][3]string{{"", "pod", "uid"}, {"default", "", "uid"}, {"default", "pod", ""}} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			// A nil client ensures validation cannot issue a delete or fall back to a collection.
			if err := DeleteNodeDebugPod(context.Background(), nil, identity[0], identity[1], types.UID(identity[2])); err == nil || err.Error() != "debug pod namespace, name and UID are required" {
				t.Fatalf("expected identity validation, got %v", err)
			}
		})
	}
}

func TestDeleteNodeDebugPodRejectsMalformedIdentity(t *testing.T) {
	const uid = "fdde0bca-d4df-4263-bf04-88ce73cc90c1"
	for _, tc := range []struct{ name, namespace, podName, uid string }{
		{"namespace whitespace", " default", "pod", uid},
		{"namespace uppercase", "Default", "pod", uid},
		{"namespace dotted", "team.a", "pod", uid},
		{"namespace too long", strings.Repeat("a", 64), "pod", uid},
		{"pod slash", "default", "a/b", uid},
		{"pod percent", "default", "pod%2Fa", uid},
		{"pod uppercase", "default", "Pod", uid},
		{"pod too long", "default", strings.Repeat("a", 254), uid},
		{"UID whitespace", "default", "pod", " " + uid},
		{"UID malformed", "default", "pod", "not-a-uuid"},
		{"UID invalid hex", "default", "pod", "zzzzzzzz-d4df-4263-bf04-88ce73cc90c1"},
		{"UID URN", "default", "pod", "urn:uuid:" + uid},
		{"UID braces", "default", "pod", "{" + uid + "}"},
		{"UID no hyphens", "default", "pod", strings.ReplaceAll(uid, "-", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(http.StatusOK) }))
			defer server.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			err = DeleteNodeDebugPod(context.Background(), client, tc.namespace, tc.podName, types.UID(tc.uid))
			if err == nil {
				t.Fatal("expected invalid identity to be rejected")
			}
			if requests != 0 {
				t.Fatalf("invalid identity issued %d Kubernetes requests", requests)
			}
		})
	}
}

func TestValidateNodeDebugPodIdentityAcceptsKubernetesNames(t *testing.T) {
	for _, tc := range []struct{ namespace, podName string }{
		{"default", "radar-node-debug-worker-1-123"},
		{"team-a", "debug.worker-1"},
		{strings.Repeat("a", 63), strings.Repeat("a", 253)},
	} {
		if err := ValidateNodeDebugPodIdentity(tc.namespace, tc.podName, "fdde0bca-d4df-4263-bf04-88ce73cc90c1"); err != nil {
			t.Fatalf("valid namespace/name rejected: %v", err)
		}
	}
}
