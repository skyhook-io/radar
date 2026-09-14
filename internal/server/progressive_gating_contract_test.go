package server

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// The per-kind gating contract the frontend depends on. Three error_code
// strings drive keep-polling vs terminal UX in web/src/api/client.ts — this
// test pins them at the HTTP layer across the whole progressive arc:
// a still-syncing kind answers kind_sync_pending, an already-synced kind
// serves mid-connecting, an untyped kind answers cluster_connecting, a kind
// whose deadline fires answers kind_sync_failed, and a kind whose LIST
// completes after that deadline serves anyway.
func TestProgressiveGatingContract(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc-1", Namespace: "default"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "dep-1", Namespace: "default"}},
	)

	prevStatus := k8s.GetConnectionStatus()
	t.Cleanup(func() {
		k8s.SetConnectionStatus(prevStatus)
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	k8s.ResetResourceCache()
	// Pods' informer start is delayed past Phase 1, so it is promoted at the
	// patience gate; the deferred deadline (shorter than the delay) then marks
	// it terminally failed; when the delay elapses the informer syncs late.
	if err := k8s.InitTestPromotedSyncingCache(client, 5*time.Second, 700*time.Millisecond,
		map[string]time.Duration{"pods": 2 * time.Second}); err != nil {
		t.Fatalf("InitTestPromotedSyncingCache: %v", err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnecting, Context: "contract-test"})

	// 1. Still-syncing typed kind: retryable, machine-readable pending.
	assertGateResponse(t, "/api/resources/pods", http.StatusServiceUnavailable, "kind_sync_pending")

	// 2. Synced typed kind serves while the connection is still 'connecting'.
	// Polled: under -race the services informer can still be completing when
	// this line runs; what the contract pins is that it serves BEFORE pods
	// does (pods stays blocked) and before any 'connected' flip.
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, errCode := gateProbe(t, "/api/resources/services")
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("services never served mid-sync (last: %d %q)", code, errCode)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// 3. Kind outside the typed informer set cannot serve before full connect,
	//    but must say "connecting", not "unavailable".
	assertGateResponse(t, "/api/resources/leases", http.StatusServiceUnavailable, "cluster_connecting")

	// 4. The deferred deadline fires with pods still blocked: terminal for now.
	deadline = time.Now().Add(5 * time.Second)
	for {
		code, errCode := gateProbe(t, "/api/resources/pods")
		if code == http.StatusServiceUnavailable && errCode == "kind_sync_failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pods never reached kind_sync_failed (last: %d %q)", code, errCode)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// 5. The delayed informer starts, its LIST completes after the deadline:
	//    the store is real, so serve it (gate and lister must agree — this
	//    used to 403).
	deadline = time.Now().Add(5 * time.Second)
	for {
		code, errCode := gateProbe(t, "/api/resources/pods")
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pods never served after late sync (last: %d %q)", code, errCode)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func assertGateResponse(t *testing.T, path string, wantStatus int, wantCode string) {
	t.Helper()
	code, errCode := gateProbe(t, path)
	if code != wantStatus || errCode != wantCode {
		t.Fatalf("GET %s: got %d %q, want %d %q", path, code, errCode, wantStatus, wantCode)
	}
}

func gateProbe(t *testing.T, path string) (status int, errorCode string) {
	t.Helper()
	resp := get(t, path)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var payload struct {
		ErrorCode string `json:"error_code"`
	}
	_ = json.Unmarshal(body, &payload)
	return resp.StatusCode, payload.ErrorCode
}
