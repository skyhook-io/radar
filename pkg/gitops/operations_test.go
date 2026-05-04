package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// argoAppForTest returns a minimal Argo Application as an unstructured object
// for use with the fake dynamic client. status fields default to absent;
// callers set them via the optional mutator.
func argoAppForTest(namespace, name string, mutate func(map[string]any)) *unstructured.Unstructured {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]any{
			"namespace": namespace,
			"name":      name,
		},
		"spec":   map[string]any{"project": "default"},
		"status": map[string]any{},
	}}
	if mutate != nil {
		mutate(app.Object)
	}
	return app
}

func newFakeArgo(objs ...runtime.Object) *fake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	// Register the Application list kind so List/Get/Patch on the GVR work.
	scheme.AddKnownTypeWithName(argoAppGVR.GroupVersion().WithKind("Application"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(argoAppGVR.GroupVersion().WithKind("ApplicationList"), &unstructured.UnstructuredList{})
	// Pull in the core scheme so non-Argo objects don't break the client init.
	_ = corev1.AddToScheme(scheme)
	return fake.NewSimpleDynamicClient(scheme, objs...)
}

// captureLastPatch returns the body of the most recent merge-patch action,
// decoded into a map. Fails the test if no patch was issued.
func captureLastPatch(t *testing.T, client *fake.FakeDynamicClient) map[string]any {
	t.Helper()
	for i := len(client.Actions()) - 1; i >= 0; i-- {
		if pa, ok := client.Actions()[i].(clienttesting.PatchAction); ok {
			var body map[string]any
			if err := json.Unmarshal(pa.GetPatch(), &body); err != nil {
				t.Fatalf("patch body not JSON: %v", err)
			}
			return body
		}
	}
	t.Fatalf("no patch action recorded; actions=%v", client.Actions())
	return nil
}

// nestedMap is a small helper that returns a sub-map by walking keys; missing
// keys return nil so test assertions stay declarative.
func nestedMap(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

// TestSyncArgoAppSyncStrategy pins the wire-format encoding of Force /
// ApplyOnly. The previous implementation wrote `syncStrategy.apply.force=true`
// for Force-without-ApplyOnly, which made Argo silently skip PreSync /
// PostSync / SyncFail hooks. The fix routes Force to `syncStrategy.hook.force`
// unless ApplyOnly is also requested. Without this test, that fix would be
// trivially reverted by anyone "DRYing" the syncStrategy block.
func TestSyncArgoAppSyncStrategy(t *testing.T) {
	tr := true
	fa := false
	cases := []struct {
		name         string
		opts         ArgoSyncOptions
		wantStrategy map[string]any // nil = no syncStrategy in patch
	}{
		{
			name:         "no flags → no syncStrategy",
			opts:         ArgoSyncOptions{},
			wantStrategy: nil,
		},
		{
			name:         "Force only → hook strategy with force",
			opts:         ArgoSyncOptions{Force: &tr},
			wantStrategy: map[string]any{"hook": map[string]any{"force": true}},
		},
		{
			name:         "ApplyOnly only → apply strategy without force",
			opts:         ArgoSyncOptions{ApplyOnly: &tr},
			wantStrategy: map[string]any{"apply": map[string]any{}},
		},
		{
			name:         "Force + ApplyOnly → apply strategy with force",
			opts:         ArgoSyncOptions{Force: &tr, ApplyOnly: &tr},
			wantStrategy: map[string]any{"apply": map[string]any{"force": true}},
		},
		{
			name:         "Force=&false (explicit off) → no syncStrategy",
			opts:         ArgoSyncOptions{Force: &fa},
			wantStrategy: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeArgo(argoAppForTest("argocd", "demo", nil))
			if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", tc.opts); err != nil {
				t.Fatalf("SyncArgoApp: %v", err)
			}
			body := captureLastPatch(t, client)
			sync := nestedMap(body, "operation", "sync")
			if sync == nil {
				t.Fatalf("patch missing operation.sync: %#v", body)
			}
			gotStrategy, _ := sync["syncStrategy"].(map[string]any)
			if tc.wantStrategy == nil {
				if gotStrategy != nil {
					t.Fatalf("expected no syncStrategy, got %#v", gotStrategy)
				}
				return
			}
			if !equalMap(gotStrategy, tc.wantStrategy) {
				t.Fatalf("syncStrategy = %#v, want %#v", gotStrategy, tc.wantStrategy)
			}
		})
	}
}

// equalMap is a shallow value comparison for map[string]any with map values —
// reflect.DeepEqual would also work but produces noisier failure output.
func equalMap(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		switch va := va.(type) {
		case map[string]any:
			vbMap, ok := vb.(map[string]any)
			if !ok || !equalMap(va, vbMap) {
				return false
			}
		default:
			if va != vb {
				return false
			}
		}
	}
	return true
}

func TestSyncArgoAppPruneAlwaysWrittenButRespectsExplicitOff(t *testing.T) {
	tr := true
	fa := false
	cases := []struct {
		name      string
		opts      ArgoSyncOptions
		wantPrune any
	}{
		{name: "nil prune defaults to true", opts: ArgoSyncOptions{}, wantPrune: true},
		{name: "explicit true", opts: ArgoSyncOptions{Prune: &tr}, wantPrune: true},
		// The doc-comment on ArgoSyncOptions.Prune calls out that explicit
		// false from the user (via the modal "untick Prune") must reach Argo
		// as `prune: false`, otherwise the user's choice is silently dropped.
		{name: "explicit false reaches the wire", opts: ArgoSyncOptions{Prune: &fa}, wantPrune: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeArgo(argoAppForTest("argocd", "demo", nil))
			if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", tc.opts); err != nil {
				t.Fatalf("SyncArgoApp: %v", err)
			}
			body := captureLastPatch(t, client)
			sync := nestedMap(body, "operation", "sync")
			if sync["prune"] != tc.wantPrune {
				t.Fatalf("prune = %#v, want %#v", sync["prune"], tc.wantPrune)
			}
		})
	}
}

// TestRollbackArgoAppHistoryIDVerification pins the int64-vs-float64
// type assertion in RollbackArgoApp. unstructured deserialization of JSON
// numbers produces float64, but the test must also cover the int64 path
// because Go's runtime deserialization may use either depending on the
// source. Without both branches, a refactor that drops one half silently
// breaks every real rollback ("Argo silently accepts and never executes",
// per the comment in the function).
func TestRollbackArgoAppHistoryIDVerification(t *testing.T) {
	makeAppWithHistory := func(historyID any) *unstructured.Unstructured {
		return argoAppForTest("argocd", "demo", func(obj map[string]any) {
			status, _ := obj["status"].(map[string]any)
			status["history"] = []any{
				map[string]any{"id": historyID, "revision": "abc123"},
			}
		})
	}

	t.Run("matches int64 history id", func(t *testing.T) {
		client := newFakeArgo(makeAppWithHistory(int64(7)))
		_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 7})
		if err != nil {
			t.Fatalf("expected success for int64 id=7, got %v", err)
		}
		body := captureLastPatch(t, client)
		rb := nestedMap(body, "operation", "rollback")
		// Patch encodes the id as int64; JSON marshal produces a number that
		// Unmarshal into map[string]any yields as float64. Assert via
		// numeric comparison rather than type-strict equality.
		if got, _ := rb["id"].(float64); got != 7 {
			t.Fatalf("rollback id in patch = %#v, want 7", rb["id"])
		}
	})

	t.Run("matches float64 history id (the realistic JSON case)", func(t *testing.T) {
		client := newFakeArgo(makeAppWithHistory(float64(42)))
		_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 42})
		if err != nil {
			t.Fatalf("expected success for float64 id=42, got %v", err)
		}
	})

	t.Run("missing id rejected with sentinel error", func(t *testing.T) {
		client := newFakeArgo(makeAppWithHistory(int64(7)))
		_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 999})
		if err == nil {
			t.Fatal("expected error for unknown history id, got nil")
		}
		if !errors.Is(err, ErrHistoryEntryNotFound) {
			t.Fatalf("expected ErrHistoryEntryNotFound, got %v", err)
		}
		// Verify no patch was issued — the whole point of the verify-first
		// design is that we don't touch the cluster on bad input.
		for _, action := range client.Actions() {
			if _, ok := action.(clienttesting.PatchAction); ok {
				t.Fatalf("rollback issued a patch despite invalid id; actions=%v", client.Actions())
			}
		}
	})

	t.Run("non-positive id rejected upfront", func(t *testing.T) {
		client := newFakeArgo(makeAppWithHistory(int64(7)))
		_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 0})
		if err == nil {
			t.Fatal("expected error for id=0")
		}
	})

	t.Run("running operation rejects rollback with sentinel error", func(t *testing.T) {
		app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
			status, _ := obj["status"].(map[string]any)
			status["operationState"] = map[string]any{"phase": "Running"}
			status["history"] = []any{map[string]any{"id": int64(1)}}
		})
		client := newFakeArgo(app)
		_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 1})
		if err == nil {
			t.Fatal("expected error during running operation")
		}
		if !errors.Is(err, ErrOperationInProgress) {
			t.Fatalf("expected ErrOperationInProgress, got %v", err)
		}
	})
}

// Sanity: the rollback-collision sentinel maps the same way in tests as it
// does in production — used to verify the handler-level HTTP mapping doesn't
// drift from the operation layer.
func TestSentinelErrorsAreDistinct(t *testing.T) {
	// Each sentinel must be uniquely identifiable so handler error mapping
	// doesn't accidentally collapse them to the same status code.
	if errors.Is(ErrOperationInProgress, ErrNoOperationInProgress) ||
		errors.Is(ErrNoOperationInProgress, ErrHistoryEntryNotFound) ||
		errors.Is(ErrOperationInProgress, ErrHistoryEntryNotFound) {
		t.Fatal("sentinel errors should not match each other under errors.Is")
	}
}

// TestOperationsRefuseTerminatingResource pins the contract that mutating
// operations refuse a resource with metadata.deletionTimestamp set, returning
// the typed sentinel ErrResourceTerminating. The HTTP and MCP layers map
// this sentinel to a tailored 409 / structured error so users see "this
// resource is being deleted" instead of either (a) a misleading success
// toast (the patch may technically land but the controller skips it) or
// (b) a confusing K8s downstream error like "namespaces X not found".
//
// Argo Refresh / Argo Terminate are intentionally NOT covered here:
//   - Refresh is read-only-from-cluster (just re-reads from Git)
//   - Terminate clears an in-flight op record, harmless on a Terminating
//     resource — and useful when the op is what's blocking deletion.
func TestOperationsRefuseTerminatingResource(t *testing.T) {
	ctx := context.Background()
	terminatingApp := func() *unstructured.Unstructured {
		return argoAppForTest("argocd", "demo", func(obj map[string]any) {
			md, _ := obj["metadata"].(map[string]any)
			md["deletionTimestamp"] = "2026-04-13T13:14:42Z"
			md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}
			// Add some history so RollbackArgoApp's history-id check
			// would otherwise pass — we want to confirm the terminating
			// guard fires *before* the history check.
			status, _ := obj["status"].(map[string]any)
			status["history"] = []any{map[string]any{"id": int64(1), "revision": "abc"}}
		})
	}
	cases := []struct {
		name string
		fn   func(client *fake.FakeDynamicClient) error
	}{
		{"SyncArgoApp", func(c *fake.FakeDynamicClient) error {
			_, err := SyncArgoApp(ctx, c, "argocd", "demo", ArgoSyncOptions{})
			return err
		}},
		{"SetArgoAutoSync(suspend)", func(c *fake.FakeDynamicClient) error {
			_, err := SetArgoAutoSync(ctx, c, "argocd", "demo", false)
			return err
		}},
		{"SetArgoAutoSync(resume)", func(c *fake.FakeDynamicClient) error {
			_, err := SetArgoAutoSync(ctx, c, "argocd", "demo", true)
			return err
		}},
		{"RollbackArgoApp", func(c *fake.FakeDynamicClient) error {
			_, err := RollbackArgoApp(ctx, c, "argocd", "demo", ArgoRollbackOptions{ID: 1})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeArgo(terminatingApp())
			err := tc.fn(client)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, ErrResourceTerminating) {
				t.Fatalf("expected ErrResourceTerminating, got %v", err)
			}
			// The error message must name the finalizer — otherwise the
			// user sees "resource is pending deletion" with no path
			// forward. Naming the finalizer points them at the owning
			// controller to investigate.
			if !strings.Contains(err.Error(), "resources-finalizer.argocd.argoproj.io") {
				t.Fatalf("expected error to name finalizer; got: %v", err)
			}
			// Verify no patch was issued — the entire point of this
			// guard is that we don't touch a resource being torn down.
			for _, action := range client.Actions() {
				if _, ok := action.(clienttesting.PatchAction); ok {
					t.Fatalf("operation issued a patch despite Terminating; actions=%v", client.Actions())
				}
			}
		})
	}
}

// TestOperationsAllowReadVerbsOnTerminatingResource asserts the carve-out
// for Refresh and Terminate. These verbs are useful on a Terminating
// resource (refresh re-reads Git; terminate clears a stuck op record)
// so they intentionally don't fire the assertNotTerminating guard.
func TestOperationsAllowReadVerbsOnTerminatingResource(t *testing.T) {
	ctx := context.Background()
	terminatingApp := argoAppForTest("argocd", "demo", func(obj map[string]any) {
		md, _ := obj["metadata"].(map[string]any)
		md["deletionTimestamp"] = "2026-04-13T13:14:42Z"
		md["finalizers"] = []any{"resources-finalizer.argocd.argoproj.io"}
		status, _ := obj["status"].(map[string]any)
		status["operationState"] = map[string]any{"phase": "Running"}
	})

	// Each subtest asserts the *guard* doesn't fire — the operation may
	// still error for unrelated reasons (the fake dynamic client's
	// JSON-Patch support is incomplete) but it must not be
	// ErrResourceTerminating. That's the contract: read-style verbs
	// don't gate on Terminating.
	t.Run("Refresh does not gate on Terminating", func(t *testing.T) {
		client := newFakeArgo(terminatingApp)
		_, err := RefreshArgoApp(ctx, client, "argocd", "demo", "normal")
		if errors.Is(err, ErrResourceTerminating) {
			t.Fatalf("Refresh should not return ErrResourceTerminating; got %v", err)
		}
	})
	t.Run("Terminate does not gate on Terminating", func(t *testing.T) {
		client := newFakeArgo(terminatingApp)
		_, err := TerminateArgoSync(ctx, client, "argocd", "demo")
		if errors.Is(err, ErrResourceTerminating) {
			t.Fatalf("Terminate should not return ErrResourceTerminating; got %v", err)
		}
	})
}

// TestOperationsPreserveNotFoundChain pins the error-wrapping contract that
// the HTTP layer relies on. Argo/Flux operation funcs wrap K8s NotFound
// errors with %w so writeGitOpsError's apierrors.IsNotFound check matches
// via errors.Is, mapping to 404. Stripping the wrap (returning a plain
// fmt.Errorf("...not found", ...)) silently downgrades 404 to 500 because
// the K8s status reason is gone — a real bug we shipped and reverted.
func TestOperationsPreserveNotFoundChain(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		fn   func(client *fake.FakeDynamicClient) error
	}{
		{"SyncArgoApp", func(c *fake.FakeDynamicClient) error {
			_, err := SyncArgoApp(ctx, c, "argocd", "missing", ArgoSyncOptions{})
			return err
		}},
		{"SetArgoAutoSync(suspend)", func(c *fake.FakeDynamicClient) error {
			_, err := SetArgoAutoSync(ctx, c, "argocd", "missing", false)
			return err
		}},
		{"RefreshArgoApp", func(c *fake.FakeDynamicClient) error {
			_, err := RefreshArgoApp(ctx, c, "argocd", "missing", "normal")
			return err
		}},
		{"TerminateArgoSync", func(c *fake.FakeDynamicClient) error {
			_, err := TerminateArgoSync(ctx, c, "argocd", "missing")
			return err
		}},
		{"RollbackArgoApp", func(c *fake.FakeDynamicClient) error {
			_, err := RollbackArgoApp(ctx, c, "argocd", "missing", ArgoRollbackOptions{ID: 1})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeArgo()
			err := tc.fn(client)
			if err == nil {
				t.Fatalf("expected error from missing Application, got nil")
			}
			if !apierrors.IsNotFound(err) {
				t.Fatalf("expected apierrors.IsNotFound to match, got %v", err)
			}
		})
	}
}

// Suppress the unused-metav1 lint when this file is compiled alone.
var _ = metav1.ObjectMeta{}
