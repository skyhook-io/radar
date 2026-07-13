package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// TestSyncArgoAppSyncStrategy pins the wire format: Force without ApplyOnly
// must encode as syncStrategy.hook.force, not syncStrategy.apply.force,
// otherwise Argo silently skips PreSync / PostSync / SyncFail hooks.
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

// TestRollbackArgoAppHistoryIDVerification pins both type-assertion paths
// for status.history[].id (int64 from structured deep-copy, float64 from
// JSON unmarshal). Dropping either half silently breaks rollback for the
// other source.
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

// TestOperationsRefuseTerminatingResource pins that mutating ops refuse a
// resource with metadata.deletionTimestamp set, returning ErrResourceTerminating.
// Refresh and Terminate are intentionally excluded because both remain useful
// when an in-flight operation is blocking deletion.
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

// TestSyncArgoAppSelectiveResources covers the selective-sync wire shape.
func TestSyncArgoAppSelectiveResources(t *testing.T) {
	cases := []struct {
		name      string
		resources []ArgoSyncResource
		want      []map[string]any // nil = sync.resources field absent
	}{
		{
			name:      "empty slice → no resources field",
			resources: nil,
			want:      nil,
		},
		{
			name: "single valid entry survives",
			resources: []ArgoSyncResource{
				{Group: "apps", Kind: "Deployment", Namespace: "demo", Name: "web"},
			},
			want: []map[string]any{
				{"group": "apps", "kind": "Deployment", "namespace": "demo", "name": "web"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeArgo(argoAppForTest("argocd", "demo", nil))
			if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{Resources: tc.resources}); err != nil {
				t.Fatalf("SyncArgoApp: %v", err)
			}
			body := captureLastPatch(t, client)
			sync := nestedMap(body, "operation", "sync")
			got, _ := sync["resources"].([]any)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("expected no resources field, got %#v", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("resources length = %d, want %d (%#v)", len(got), len(tc.want), got)
			}
			for i, raw := range got {
				m, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("resources[%d] not a map: %T", i, raw)
				}
				if !equalMap(m, tc.want[i]) {
					t.Fatalf("resources[%d] = %#v, want %#v", i, m, tc.want[i])
				}
			}
		})
	}
}

func TestSyncArgoAppRejectsIncompleteSelectiveResource(t *testing.T) {
	resources := []ArgoSyncResource{
		{Group: "apps", Kind: "Deployment", Namespace: "demo", Name: "web"},
		{Kind: "Service"},
	}
	client := newFakeArgo(argoAppForTest("argocd", "demo", nil))
	_, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{Resources: resources})
	if !errors.Is(err, ErrInvalidResourceSelection) {
		t.Fatalf("SyncArgoApp error = %v, want ErrInvalidResourceSelection", err)
	}
	for _, action := range client.Actions() {
		if _, ok := action.(clienttesting.PatchAction); ok {
			t.Fatalf("invalid selective sync issued a patch; actions=%v", client.Actions())
		}
	}
}

func TestSyncArgoAppDryRunDoesNotRequestHardRefresh(t *testing.T) {
	dryRun := true
	client := newFakeArgo(argoAppForTest("argocd", "demo", nil))
	if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{DryRun: &dryRun}); err != nil {
		t.Fatalf("SyncArgoApp: %v", err)
	}
	body := captureLastPatch(t, client)
	if annotations := nestedMap(body, "metadata", "annotations"); annotations != nil {
		t.Fatalf("dry-run patch must not request a refresh, got annotations %#v", annotations)
	}
}

func TestArgoOperationPatchesUseResourceVersion(t *testing.T) {
	app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
		metadata, _ := obj["metadata"].(map[string]any)
		metadata["resourceVersion"] = "17"
		status, _ := obj["status"].(map[string]any)
		status["history"] = []any{map[string]any{"id": int64(1)}}
	})

	t.Run("sync", func(t *testing.T) {
		client := newFakeArgo(app.DeepCopy())
		if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{}); err != nil {
			t.Fatalf("SyncArgoApp: %v", err)
		}
		metadata := nestedMap(captureLastPatch(t, client), "metadata")
		if metadata["resourceVersion"] != "17" {
			t.Fatalf("sync resourceVersion = %#v, want 17", metadata["resourceVersion"])
		}
	})

	t.Run("rollback", func(t *testing.T) {
		client := newFakeArgo(app.DeepCopy())
		if _, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 1}); err != nil {
			t.Fatalf("RollbackArgoApp: %v", err)
		}
		metadata := nestedMap(captureLastPatch(t, client), "metadata")
		if metadata["resourceVersion"] != "17" {
			t.Fatalf("rollback resourceVersion = %#v, want 17", metadata["resourceVersion"])
		}
	})
}

func TestArgoOperationPatchConflictMapsToOperationInProgress(t *testing.T) {
	app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
		status, _ := obj["status"].(map[string]any)
		status["history"] = []any{map[string]any{"id": int64(1)}}
	})
	cases := []struct {
		name string
		run  func(*fake.FakeDynamicClient) error
	}{
		{name: "sync", run: func(client *fake.FakeDynamicClient) error {
			_, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{})
			return err
		}},
		{name: "rollback", run: func(client *fake.FakeDynamicClient) error {
			_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 1})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeArgo(app.DeepCopy())
			client.PrependReactor("patch", "applications", func(action clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewConflict(argoAppGVR.GroupResource(), "demo", errors.New("changed"))
			})
			if err := tc.run(client); !errors.Is(err, ErrOperationInProgress) {
				t.Fatalf("error = %v, want ErrOperationInProgress", err)
			}
		})
	}
}

func TestValidateArgoResourceRejectsQueuedOperation(t *testing.T) {
	app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
		obj["operation"] = map[string]any{"sync": map[string]any{}}
	})
	client := newFakeArgo(app)
	_, err := ValidateArgoResource(context.Background(), client, "argocd", "demo", ArgoSyncResource{Kind: "Service", Name: "api"}, ArgoSyncOptions{})
	if !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("ValidateArgoResource error = %v, want ErrOperationInProgress", err)
	}
	for _, action := range client.Actions() {
		if _, ok := action.(clienttesting.PatchAction); ok {
			t.Fatalf("validation overwrote a queued operation; actions=%v", client.Actions())
		}
	}
}

func TestValidateArgoResourceStartsSafeSelectiveDryRun(t *testing.T) {
	client := newFakeArgo(argoAppForTest("argocd", "demo", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	target := ArgoSyncResource{Group: "apps", Kind: "Deployment", Namespace: "demo", Name: "api"}
	_, err := ValidateArgoResource(ctx, client, "argocd", "demo", target, ArgoSyncOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ValidateArgoResource error = %v, want context deadline", err)
	}
	body := captureLastPatch(t, client)
	if annotations := nestedMap(body, "metadata", "annotations"); annotations != nil {
		t.Fatalf("validation patch must not request a refresh, got annotations %#v", annotations)
	}
	sync := nestedMap(body, "operation", "sync")
	if sync["dryRun"] != true || sync["prune"] != false {
		t.Fatalf("validation flags = dryRun:%#v prune:%#v", sync["dryRun"], sync["prune"])
	}
	resources, ok := sync["resources"].([]any)
	if !ok || len(resources) != 1 {
		t.Fatalf("validation resources = %#v, want one exact selector", sync["resources"])
	}
	info, ok := nestedMap(body, "operation")["info"].([]any)
	if !ok || len(info) != 1 {
		t.Fatalf("validation info = %#v, want one correlation entry", nestedMap(body, "operation")["info"])
	}
}

func TestArgoResourceValidationResultCorrelatesAndReturnsTarget(t *testing.T) {
	target := ArgoSyncResource{Group: "karpenter.sh", Kind: "NodePool", Namespace: "default", Name: "default"}
	makeApp := func(id, phase string, resources []any) *unstructured.Unstructured {
		return argoAppForTest("argocd", "demo", func(obj map[string]any) {
			obj["status"] = map[string]any{
				"operationState": map[string]any{
					"phase": phase,
					"operation": map[string]any{
						"info": []any{map[string]any{"name": "radar-validation-id", "value": id}},
					},
					"syncResult": map[string]any{"resources": resources},
				},
			}
		})
	}
	matchingResource := map[string]any{
		"group": "karpenter.sh", "kind": "NodePool", "namespace": "default", "name": "default",
		"status": "Synced", "message": "nodepool.karpenter.sh/default configured (dry run)",
	}

	if _, done := argoResourceValidationResult(makeApp("old", "Succeeded", []any{matchingResource}), "current", target); done {
		t.Fatal("a terminal result from a previous request must not complete the current validation")
	}
	if _, done := argoResourceValidationResult(makeApp("current", "Running", []any{matchingResource}), "current", target); done {
		t.Fatal("a matching operation that is still running must keep waiting")
	}
	result, done := argoResourceValidationResult(makeApp("current", "Succeeded", []any{matchingResource}), "current", target)
	if !done || result.Outcome != "succeeded" || result.Resource == nil {
		t.Fatalf("result = %#v, done=%v; want succeeded target result", result, done)
	}
	if !strings.Contains(result.Message, "API admission can still reject") {
		t.Fatalf("success message = %q, want dry-run limitation", result.Message)
	}
	if result.Resource.Status != "Synced" || !strings.Contains(result.Resource.Message, "dry run") {
		t.Fatalf("resource result = %#v, want exact Argo status and message", result.Resource)
	}
	targetWithoutNamespace := target
	targetWithoutNamespace.Namespace = ""
	result, done = argoResourceValidationResult(makeApp("current", "Succeeded", []any{matchingResource}), "current", targetWithoutNamespace)
	if !done || result.Outcome != "succeeded" {
		t.Fatalf("namespace-omitted selector result = %#v, done=%v; want succeeded", result, done)
	}
	targetInAnotherNamespace := target
	targetInAnotherNamespace.Namespace = "other"
	if argoResourceResultMatches(matchingResource, targetInAnotherNamespace) {
		t.Fatal("an explicit selector namespace must match the result namespace")
	}
	result, done = argoResourceValidationResult(makeApp("current", "Succeeded", nil), "current", target)
	if !done || result.Outcome != "inconclusive" {
		t.Fatalf("missing target result = %#v, done=%v; want inconclusive", result, done)
	}
}

// TestSetArgoAutoSyncResumeRestoresSettings pins the legacy / current
// annotation lookup on resume. Older Radar builds wrote skyhook.io/* keys;
// the current writer uses radarhq.io/*. Resume must read either, prefer
// the current key when both are present, and fall back to defaults when
// neither is — and clear all four keys regardless.
func TestSetArgoAutoSyncResumeRestoresSettings(t *testing.T) {
	cases := []struct {
		name         string
		annotations  map[string]any
		wantPrune    bool
		wantSelfHeal bool
	}{
		{
			name: "current keys only",
			annotations: map[string]any{
				ArgoSuspendedPruneAnnotation:    "false",
				ArgoSuspendedSelfHealAnnotation: "true",
			},
			wantPrune:    false,
			wantSelfHeal: true,
		},
		{
			name: "legacy keys only",
			annotations: map[string]any{
				legacyArgoSuspendedPruneAnnotation:    "true",
				legacyArgoSuspendedSelfHealAnnotation: "false",
			},
			wantPrune:    true,
			wantSelfHeal: false,
		},
		{
			name: "current and legacy both present → current wins",
			annotations: map[string]any{
				ArgoSuspendedPruneAnnotation:          "true",
				legacyArgoSuspendedPruneAnnotation:    "false",
				ArgoSuspendedSelfHealAnnotation:       "false",
				legacyArgoSuspendedSelfHealAnnotation: "true",
			},
			wantPrune:    true,
			wantSelfHeal: false,
		},
		{
			name:         "no suspension annotations → defaults to true/true",
			annotations:  map[string]any{},
			wantPrune:    true,
			wantSelfHeal: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
				md, _ := obj["metadata"].(map[string]any)
				md["annotations"] = tc.annotations
			})
			client := newFakeArgo(app)
			if _, err := SetArgoAutoSync(context.Background(), client, "argocd", "demo", true); err != nil {
				t.Fatalf("SetArgoAutoSync(resume): %v", err)
			}
			body := captureLastPatch(t, client)
			automated := nestedMap(body, "spec", "syncPolicy", "automated")
			if automated == nil {
				t.Fatalf("patch missing spec.syncPolicy.automated: %#v", body)
			}
			if got, _ := automated["prune"].(bool); got != tc.wantPrune {
				t.Fatalf("prune = %v, want %v", got, tc.wantPrune)
			}
			if got, _ := automated["selfHeal"].(bool); got != tc.wantSelfHeal {
				t.Fatalf("selfHeal = %v, want %v", got, tc.wantSelfHeal)
			}
			// All four annotation keys must be cleared (set to nil) so a
			// future suspend cycle starts clean. The patch encodes "delete"
			// as a nil-valued annotations map entry.
			ann := nestedMap(body, "metadata", "annotations")
			if ann == nil {
				t.Fatalf("patch missing metadata.annotations: %#v", body)
			}
			for _, key := range []string{
				ArgoSuspendedPruneAnnotation,
				ArgoSuspendedSelfHealAnnotation,
				legacyArgoSuspendedPruneAnnotation,
				legacyArgoSuspendedSelfHealAnnotation,
			} {
				v, present := ann[key]
				if !present {
					t.Fatalf("annotation %q not present in patch (must be set to nil to clear)", key)
				}
				if v != nil {
					t.Fatalf("annotation %q = %v, want nil (delete)", key, v)
				}
			}
		})
	}
}

// TestFluxOperationsRefuseTerminatingResource extends the Argo coverage in
// TestOperationsRefuseTerminatingResource to the Flux operation surface,
// so a refactor that drops assertNotTerminating from any Flux verb is
// caught at the unit level, not when it hits prod.
func TestFluxOperationsRefuseTerminatingResource(t *testing.T) {
	ctx := context.Background()
	scheme := newFluxScheme(t)
	terminatingKustomization := func() *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata": map[string]any{
				"namespace":         "flux-system",
				"name":              "demo",
				"deletionTimestamp": "2026-04-13T13:14:42Z",
				"finalizers":        []any{"finalizers.fluxcd.io"},
			},
			"spec": map[string]any{
				"sourceRef": map[string]any{
					"kind":      "GitRepository",
					"name":      "source",
					"namespace": "flux-system",
				},
			},
		}}
	}
	kustomizationEntry, err := ResolveFluxKind("Kustomization")
	if err != nil {
		t.Fatalf("ResolveFluxKind: %v", err)
	}
	cases := []struct {
		name string
		fn   func(client *fake.FakeDynamicClient) error
	}{
		{"ReconcileFlux", func(c *fake.FakeDynamicClient) error {
			_, err := ReconcileFlux(ctx, c, kustomizationEntry, "flux-system", "demo")
			return err
		}},
		{"SetFluxSuspend(suspend)", func(c *fake.FakeDynamicClient) error {
			_, err := SetFluxSuspend(ctx, c, kustomizationEntry, "flux-system", "demo", true)
			return err
		}},
		{"SetFluxSuspend(resume)", func(c *fake.FakeDynamicClient) error {
			_, err := SetFluxSuspend(ctx, c, kustomizationEntry, "flux-system", "demo", false)
			return err
		}},
		{"SyncFluxWithSource", func(c *fake.FakeDynamicClient) error {
			_, err := SyncFluxWithSource(ctx, c, "Kustomization", "flux-system", "demo")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleDynamicClient(scheme, terminatingKustomization())
			err := tc.fn(client)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, ErrResourceTerminating) {
				t.Fatalf("expected ErrResourceTerminating, got %v", err)
			}
			for _, action := range client.Actions() {
				if _, ok := action.(clienttesting.PatchAction); ok {
					t.Fatalf("operation issued a patch despite Terminating; actions=%v", client.Actions())
				}
			}
		})
	}
}

// TestSyncFluxWithSourceCases covers happy-path + the three bad-input shapes
// the real implementation has to navigate: missing sourceRef, unsupported
// outer kind, and the zombie-namespace 404 wrapping.
func TestSyncFluxWithSourceCases(t *testing.T) {
	ctx := context.Background()
	scheme := newFluxScheme(t)

	t.Run("Kustomization happy path patches source then target", func(t *testing.T) {
		client := fake.NewSimpleDynamicClient(scheme,
			fluxGitRepository("flux-system", "source"),
			fluxKustomization("flux-system", "demo", map[string]any{
				"kind": "GitRepository", "name": "source", "namespace": "flux-system",
			}),
		)
		res, err := SyncFluxWithSource(ctx, client, "Kustomization", "flux-system", "demo")
		if err != nil {
			t.Fatalf("SyncFluxWithSource: %v", err)
		}
		if res.Source == nil || res.Source.Kind != "GitRepository" || res.Source.Name != "source" {
			t.Fatalf("unexpected Source ref: %#v", res.Source)
		}
		patches := patchActionsByResource(client)
		if patches["gitrepositories"] != 1 {
			t.Fatalf("expected 1 patch on gitrepositories, got %d", patches["gitrepositories"])
		}
		if patches["kustomizations"] != 1 {
			t.Fatalf("expected 1 patch on kustomizations, got %d", patches["kustomizations"])
		}
	})

	t.Run("HelmRelease happy path resolves nested chart sourceRef", func(t *testing.T) {
		hr := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata":   map[string]any{"namespace": "flux-system", "name": "demo"},
			"spec": map[string]any{
				"chart": map[string]any{
					"spec": map[string]any{
						"sourceRef": map[string]any{
							"kind": "HelmRepository", "name": "repo", "namespace": "flux-system",
						},
					},
				},
			},
		}}
		repo := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "source.toolkit.fluxcd.io/v1",
			"kind":       "HelmRepository",
			"metadata":   map[string]any{"namespace": "flux-system", "name": "repo"},
		}}
		client := fake.NewSimpleDynamicClient(scheme, hr, repo)
		res, err := SyncFluxWithSource(ctx, client, "HelmRelease", "flux-system", "demo")
		if err != nil {
			t.Fatalf("SyncFluxWithSource: %v", err)
		}
		if res.Source == nil || res.Source.Kind != "HelmRepository" {
			t.Fatalf("unexpected Source ref: %#v", res.Source)
		}
	})

	t.Run("missing sourceRef returns an explicit error before any patch", func(t *testing.T) {
		client := fake.NewSimpleDynamicClient(scheme, fluxKustomization("flux-system", "demo", nil))
		_, err := SyncFluxWithSource(ctx, client, "Kustomization", "flux-system", "demo")
		if err == nil {
			t.Fatal("expected error for missing sourceRef")
		}
		if !strings.Contains(err.Error(), "no source reference") {
			t.Fatalf("error should name the missing source ref; got %v", err)
		}
		for _, action := range client.Actions() {
			if _, ok := action.(clienttesting.PatchAction); ok {
				t.Fatal("no patch should fire when sourceRef is missing")
			}
		}
	})

	t.Run("unsupported kind rejected without API call", func(t *testing.T) {
		// GitRepository is a valid Flux kind but sync-with-source isn't
		// defined for it (it's already a source). Use a non-empty spec so
		// the spec-shape guard passes and we exercise the kind-switch default.
		repo := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "source.toolkit.fluxcd.io/v1",
			"kind":       "GitRepository",
			"metadata":   map[string]any{"namespace": "flux-system", "name": "source"},
			"spec":       map[string]any{"interval": "1m", "url": "https://example.com"},
		}}
		client := fake.NewSimpleDynamicClient(scheme, repo)
		_, err := SyncFluxWithSource(ctx, client, "GitRepository", "flux-system", "source")
		if err == nil {
			t.Fatal("expected error for unsupported kind")
		}
		if !strings.Contains(err.Error(), "only supported for Kustomization and HelmRelease") {
			t.Fatalf("error should name the supported kinds; got %v", err)
		}
	})

	t.Run("source-not-found wraps with finalizer-zombie context and preserves NotFound chain", func(t *testing.T) {
		// Kustomization references a source that doesn't exist in the
		// fake client — patch returns NotFound. The wrapped error must
		// (a) mention the zombie scenario for operator clarity and
		// (b) errors.Is-match apierrors.IsNotFound so HTTP layer maps to 404.
		client := fake.NewSimpleDynamicClient(scheme,
			fluxKustomization("flux-system", "demo", map[string]any{
				"kind": "GitRepository", "name": "ghost", "namespace": "flux-system",
			}),
		)
		_, err := SyncFluxWithSource(ctx, client, "Kustomization", "flux-system", "demo")
		if err == nil {
			t.Fatal("expected NotFound error from missing source")
		}
		if !apierrors.IsNotFound(err) {
			t.Fatalf("expected apierrors.IsNotFound to match (HTTP layer relies on this); got %v", err)
		}
		if !strings.Contains(err.Error(), "finalizer-stuck zombie") {
			t.Fatalf("error should include the zombie hint to disambiguate cause; got %v", err)
		}
	})
}

// --- Flux test helpers ---

func newFluxScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for kind, gv := range map[string]schema.GroupVersion{
		"Kustomization":  {Group: "kustomize.toolkit.fluxcd.io", Version: "v1"},
		"HelmRelease":    {Group: "helm.toolkit.fluxcd.io", Version: "v2"},
		"GitRepository":  {Group: "source.toolkit.fluxcd.io", Version: "v1"},
		"HelmRepository": {Group: "source.toolkit.fluxcd.io", Version: "v1"},
		"OCIRepository":  {Group: "source.toolkit.fluxcd.io", Version: "v1"},
	} {
		scheme.AddKnownTypeWithName(gv.WithKind(kind), &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gv.WithKind(kind+"List"), &unstructured.UnstructuredList{})
	}
	_ = corev1.AddToScheme(scheme)
	return scheme
}

func fluxKustomization(namespace, name string, sourceRef map[string]any) *unstructured.Unstructured {
	spec := map[string]any{}
	if sourceRef != nil {
		spec["sourceRef"] = sourceRef
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]any{"namespace": namespace, "name": name},
		"spec":       spec,
	}}
}

func fluxGitRepository(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata":   map[string]any{"namespace": namespace, "name": name},
	}}
}

func patchActionsByResource(client *fake.FakeDynamicClient) map[string]int {
	out := map[string]int{}
	for _, action := range client.Actions() {
		if pa, ok := action.(clienttesting.PatchAction); ok {
			out[pa.GetResource().Resource]++
		}
	}
	return out
}

func TestTerminateArgoSyncJSONPatchRaceMapsToSentinel(t *testing.T) {
	app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
		status, _ := obj["status"].(map[string]any)
		status["operationState"] = map[string]any{"phase": "Running"}
	})
	client := newFakeArgo(app)
	client.PrependReactor("patch", "applications", func(action clienttesting.Action) (bool, runtime.Object, error) {
		// Mimic the K8s API server's JSON-Patch error when /operation is
		// missing: an Invalid StatusError. apierrors.IsInvalid must match.
		return true, nil, apierrors.NewInvalid(
			schema.GroupKind{Group: "argoproj.io", Kind: "Application"},
			"demo",
			nil,
		)
	})
	_, err := TerminateArgoSync(context.Background(), client, "argocd", "demo")
	if err == nil {
		t.Fatal("expected error from racing terminate")
	}
	if !errors.Is(err, ErrNoOperationInProgress) {
		t.Fatalf("expected ErrNoOperationInProgress, got %v", err)
	}
}

func TestTerminateArgoSyncRequestsControllerTermination(t *testing.T) {
	app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
		metadata, _ := obj["metadata"].(map[string]any)
		metadata["resourceVersion"] = "23"
		obj["operation"] = map[string]any{"sync": map[string]any{"revision": "abc123"}}
		status, _ := obj["status"].(map[string]any)
		status["operationState"] = map[string]any{"phase": "Running"}
	})
	client := newFakeArgo(app)

	result, err := TerminateArgoSync(context.Background(), client, "argocd", "demo")
	if err != nil {
		t.Fatalf("TerminateArgoSync: %v", err)
	}
	if result.Message != "Termination requested" {
		t.Fatalf("message = %q, want termination request", result.Message)
	}
	updated, err := client.Resource(argoAppGVR).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get updated Application: %v", err)
	}
	phase, _, _ := unstructured.NestedString(updated.Object, "status", "operationState", "phase")
	if phase != "Terminating" {
		t.Fatalf("operation phase = %q, want Terminating", phase)
	}
	if operation, found, _ := unstructured.NestedFieldNoCopy(updated.Object, "operation"); !found || operation == nil {
		t.Fatal("terminate removed spec.operation instead of leaving it for the Argo controller")
	}
	for _, action := range client.Actions() {
		patchAction, ok := action.(clienttesting.PatchAction)
		if !ok {
			continue
		}
		var operations []map[string]any
		if err := json.Unmarshal(patchAction.GetPatch(), &operations); err != nil {
			t.Fatalf("decode terminate patch: %v", err)
		}
		for _, operation := range operations {
			if operation["path"] == "/metadata/resourceVersion" {
				t.Fatal("terminate must not reject a live operation because unrelated status updates changed resourceVersion")
			}
		}
	}
}

func TestTerminateArgoSyncRepairsStaleRunningStatus(t *testing.T) {
	app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
		status, _ := obj["status"].(map[string]any)
		status["operationState"] = map[string]any{"phase": "Running", "message": "waiting for hook"}
	})
	client := newFakeArgo(app)

	result, err := TerminateArgoSync(context.Background(), client, "argocd", "demo")
	if err != nil {
		t.Fatalf("TerminateArgoSync: %v", err)
	}
	if result.Message != "Stale operation status cleared" {
		t.Fatalf("message = %q, want stale-status recovery", result.Message)
	}
	updated, err := client.Resource(argoAppGVR).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get updated Application: %v", err)
	}
	phase, _, _ := unstructured.NestedString(updated.Object, "status", "operationState", "phase")
	if phase != "Error" {
		t.Fatalf("operation phase = %q, want Error", phase)
	}
	finishedAt, _, _ := unstructured.NestedString(updated.Object, "status", "operationState", "finishedAt")
	if _, err := time.Parse(time.RFC3339Nano, finishedAt); err != nil {
		t.Fatalf("finishedAt = %q, want RFC3339 timestamp: %v", finishedAt, err)
	}
}

// Suppress the unused-metav1 lint when this file is compiled alone.
var _ = metav1.ObjectMeta{}
