package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

func TestDeleteResourceDeadlineWithoutParentDeadline(t *testing.T) {
	for _, phase := range []string{"read", "observation", "observation-remaining-budget", "observation-denied", "delete", "preview-delete"} {
		t.Run(phase, func(t *testing.T) {
			setConnectedForTest(t)
			var apply atomic.Bool
			var deletes, stalled atomic.Int32
			release := make(chan struct{})
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "cfg", "namespace": "demo", "uid": "u", "resourceVersion": "1"}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method {
				case http.MethodGet:
					if apply.Load() && (phase == "read" || deletes.Load() > 0) {
						if phase == "observation-denied" {
							w.WriteHeader(http.StatusForbidden)
							_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: 403})
							return
						}
						stalled.Add(1)
						select {
						case <-r.Context().Done():
						case <-release:
						case <-time.After(time.Second):
						}
						return
					}
					_ = json.NewEncoder(w).Encode(obj)
				case http.MethodDelete:
					if apply.Load() {
						deletes.Add(1)
						if phase == "delete" || phase == "preview-delete" {
							stalled.Add(1)
							select {
							case <-r.Context().Done():
							case <-release:
							case <-time.After(time.Second):
							}
							return
						}

					}
					_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusSuccess})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(release) })
			dyn, err := dynamic.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if err := k8s.InitTestDynamicResourceCache(dyn, nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			if err := k8s.InitTestResourceCache(kubefake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "demo", UID: "u", ResourceVersion: "1"}})); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetResourceCache)
			addDeletePreviewKinds(t)
			input := deleteResourceInput{Kind: "ConfigMap", Namespace: "demo", Name: "cfg"}
			ctx := context.Background()
			preview, _, err := handleDeleteResource(ctx, nil, input)
			if err != nil {
				t.Fatal(err)
			}
			input.Confirm = decodeToolResult(t, preview)["confirm"].(string)
			input.DryRun = ptr.To(false)
			if phase == "preview-delete" {
				input.DryRun = ptr.To(true)
			}
			previousCall, previousObservation := deleteCallTimeout, deleteObservationTimeout
			deleteCallTimeout, deleteObservationTimeout = 120*time.Millisecond, 30*time.Millisecond
			if phase == "observation" {
				deleteCallTimeout = time.Second
			}
			if phase == "observation-remaining-budget" {
				deleteObservationTimeout = time.Second
			}
			t.Cleanup(func() { deleteCallTimeout, deleteObservationTimeout = previousCall, previousObservation })
			apply.Store(true)
			start := time.Now()
			result, _, err := handleDeleteResource(ctx, nil, input)
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("call exceeded its budget: %v", elapsed)
			}
			wantStalled := int32(1)
			if phase == "observation-denied" {
				wantStalled = 0
			}
			if stalled.Load() != wantStalled {
				t.Fatalf("stalled requests = %d, want %d", stalled.Load(), wantStalled)
			}
			switch phase {
			case "read":
				if !errors.Is(err, context.DeadlineExceeded) || result != nil {
					t.Fatalf("stalled read: result=%+v err=%v", result, err)
				}
				if phase == "read" && (deletes.Load() != 0 || !strings.Contains(err.Error(), "no deletion attempted")) {
					t.Fatalf("pre-delete timeout: deletes=%d err=%v", deletes.Load(), err)
				}
			case "delete", "preview-delete":
				want := "deletion outcome unknown"
				if phase == "preview-delete" {
					want = "deletion preview failed; no deletion was persisted"
				}
				if !errors.Is(err, context.DeadlineExceeded) || result != nil || deletes.Load() != 1 || !strings.Contains(err.Error(), want) {
					t.Fatalf("in-flight DELETE timeout: deletes=%d result=%+v err=%v", deletes.Load(), result, err)
				}
			default:
				if err != nil {
					t.Fatalf("accepted DELETE failed: %v", err)
				}
				got := decodeToolResult(t, result)
				observation := got["observation"].(map[string]any)
				message, _ := observation["observationError"].(string)
				if got["status"] != "ok" || deletes.Load() != 1 {
					t.Fatalf("accepted DELETE lost: deletes=%d result=%+v", deletes.Load(), got)
				}
				if phase == "observation-denied" {
					if !strings.Contains(message, "follow-up read failed") || strings.Contains(message, "budget") {
						t.Fatalf("observation denial mislabeled: %s", message)
					}
				} else if !strings.Contains(message, "cleanup was not observed") || !strings.Contains(message, "context deadline exceeded") {
					t.Fatalf("observation timeout lost: %s", message)
				}
			}
		})
	}
}
