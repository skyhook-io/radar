package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
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

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
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

// Prune is always written and is off unless the caller asks for it, as in
// Argo CD's UI and CLI: a sync that omits it must not delete resources that
// were removed from Git.
func TestSyncArgoAppPruneAlwaysWrittenAndDefaultsOff(t *testing.T) {
	tr := true
	fa := false
	cases := []struct {
		name      string
		opts      ArgoSyncOptions
		wantPrune any
	}{
		{name: "nil prune defaults to false", opts: ArgoSyncOptions{}, wantPrune: false},
		{name: "explicit true", opts: ArgoSyncOptions{Prune: &tr}, wantPrune: true},
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
				map[string]any{"id": historyID, "revision": "abc123", "source": map[string]any{"repoURL": "https://example.com/repo", "path": "app"}},
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
		if got := nestedMap(body, "operation", "sync")["revision"]; got != "abc123" {
			t.Fatalf("operation.sync.revision = %#v, want abc123", got)
		}
	})

	t.Run("matches float64 history id (the realistic JSON case)", func(t *testing.T) {
		client := newFakeArgo(makeAppWithHistory(float64(42)))
		_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 42})
		if err != nil {
			t.Fatalf("expected success for float64 id=42, got %v", err)
		}
	})

	// Argo's controller numbers history from 0, so the first deploy is id 0.
	t.Run("id 0 is the first history entry", func(t *testing.T) {
		client := newFakeArgo(makeAppWithHistory(int64(0)))
		if _, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 0}); err != nil {
			t.Fatalf("expected success for id=0, got %v", err)
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
		assertNoPatch(t, client)
	})

	t.Run("negative id rejected upfront", func(t *testing.T) {
		client := newFakeArgo(makeAppWithHistory(int64(7)))
		_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: -1})
		if err == nil {
			t.Fatal("expected error for id=-1")
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

	t.Run("pending operation rejects rollback with sentinel error", func(t *testing.T) {
		client := newFakeArgo(argoAppForTest("argocd", "demo", func(obj map[string]any) {
			obj["operation"] = map[string]any{"sync": map[string]any{"revision": "abc123"}}
			status, _ := obj["status"].(map[string]any)
			status["history"] = []any{map[string]any{"id": int64(1), "revision": "abc123", "source": map[string]any{"repoURL": "https://example.com/repo"}}}
		}))
		_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 1})
		if !errors.Is(err, ErrOperationInProgress) {
			t.Fatalf("expected ErrOperationInProgress, got %v", err)
		}
		assertNoPatch(t, client)
	})
}

func assertNoPatch(t *testing.T, client *fake.FakeDynamicClient) {
	t.Helper()
	for _, action := range client.Actions() {
		if _, ok := action.(clienttesting.PatchAction); ok {
			t.Fatalf("expected no patch; actions=%v", client.Actions())
		}
	}
}

// The Application CRD has no operation.rollback: the API server prunes it and
// Argo's controller fails the empty operation. Rollback must be written the
// way argocd-server writes it — a sync pinned to the history entry.
func TestRollbackArgoAppWritesSyncOperation(t *testing.T) {
	ctx := context.Background()
	trueValue := true

	t.Run("single source", func(t *testing.T) {
		client := newFakeArgo(argoAppForTest("argocd", "demo", func(obj map[string]any) {
			obj["spec"] = map[string]any{
				"project":    "default",
				"source":     map[string]any{"repoURL": "https://example.com/repo", "path": "current", "targetRevision": "main"},
				"syncPolicy": map[string]any{"syncOptions": []any{"CreateNamespace=true"}},
			}
			status, _ := obj["status"].(map[string]any)
			status["history"] = []any{
				map[string]any{"id": int64(0), "revision": "rev-old", "source": map[string]any{"repoURL": "https://example.com/repo", "path": "old", "targetRevision": "main"}},
				map[string]any{"id": int64(1), "revision": "rev-new", "source": map[string]any{"repoURL": "https://example.com/repo", "path": "current", "targetRevision": "main"}},
			}
		}))
		if _, err := RollbackArgoApp(ctx, client, "argocd", "demo", ArgoRollbackOptions{ID: 0}); err != nil {
			t.Fatalf("RollbackArgoApp: %v", err)
		}
		body := captureLastPatch(t, client)
		operation := nestedMap(body, "operation")
		if _, ok := operation["rollback"]; ok {
			t.Fatalf("patch still writes operation.rollback: %v", operation)
		}
		if got := nestedMap(operation, "initiatedBy")["username"]; got != "radar" {
			t.Fatalf("initiatedBy.username = %#v, want radar", got)
		}
		if !argoOperationHasInfo(operation, argoRollbackInfoName, "true") {
			t.Fatalf("rollback operation lacks its marker: %#v", operation)
		}
		want := map[string]any{
			"revision":     "rev-old",
			"source":       map[string]any{"repoURL": "https://example.com/repo", "path": "old", "targetRevision": "main"},
			"prune":        false,
			"syncStrategy": map[string]any{"apply": map[string]any{}},
			"syncOptions":  []any{"CreateNamespace=true"},
		}
		if got := nestedMap(operation, "sync"); !reflect.DeepEqual(got, want) {
			t.Fatalf("operation.sync = %#v\nwant %#v", got, want)
		}
	})

	t.Run("multi source", func(t *testing.T) {
		sources := []any{
			map[string]any{"repoURL": "https://example.com/a", "path": "app", "targetRevision": "main"},
			map[string]any{"repoURL": "https://example.com/b", "path": "values", "targetRevision": "v2", "ref": "values"},
		}
		client := newFakeArgo(argoAppForTest("argocd", "demo", func(obj map[string]any) {
			status, _ := obj["status"].(map[string]any)
			status["history"] = []any{map[string]any{
				"id":        int64(3),
				"revisions": []any{"rev-a", "rev-b"},
				"sources":   sources,
				// Argo serializes the unused single source of a multi-source
				// entry as an all-empty object.
				"source": map[string]any{"repoURL": ""},
			}}
		}))
		if _, err := RollbackArgoApp(ctx, client, "argocd", "demo", ArgoRollbackOptions{ID: 3, Prune: &trueValue, DryRun: &trueValue}); err != nil {
			t.Fatalf("RollbackArgoApp: %v", err)
		}
		want := map[string]any{
			"revisions":    []any{"rev-a", "rev-b"},
			"sources":      sources,
			"prune":        true,
			"dryRun":       true,
			"syncStrategy": map[string]any{"apply": map[string]any{}},
		}
		if got := nestedMap(captureLastPatch(t, client), "operation", "sync"); !reflect.DeepEqual(got, want) {
			t.Fatalf("operation.sync = %#v\nwant %#v", got, want)
		}
	})

	t.Run("entry without a source is refused", func(t *testing.T) {
		client := newFakeArgo(argoAppForTest("argocd", "demo", func(obj map[string]any) {
			status, _ := obj["status"].(map[string]any)
			status["history"] = []any{map[string]any{"id": int64(1), "revision": "abc123"}}
		}))
		_, err := RollbackArgoApp(ctx, client, "argocd", "demo", ArgoRollbackOptions{ID: 1})
		if !errors.Is(err, ErrHistoryEntryNotRollbackable) {
			t.Fatalf("expected ErrHistoryEntryNotRollbackable, got %v", err)
		}
		assertNoPatch(t, client)
	})
}

// Argo CD refuses a rollback while automated sync is on; Argo CD 3.x can keep
// the automated block with enabled: false, which is off.
func TestRollbackArgoAppAutoSync(t *testing.T) {
	cases := []struct {
		name      string
		automated map[string]any
		wantErr   bool
	}{
		{name: "automated block present", automated: map[string]any{"prune": true, "selfHeal": true}, wantErr: true},
		{name: "empty automated block", automated: map[string]any{}, wantErr: true},
		{name: "enabled: true", automated: map[string]any{"enabled": true}, wantErr: true},
		{name: "enabled: false", automated: map[string]any{"enabled": false, "prune": true}, wantErr: false},
		{name: "no automated block", automated: nil, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeArgo(argoAppForTest("argocd", "demo", func(obj map[string]any) {
				spec, _ := obj["spec"].(map[string]any)
				if tc.automated != nil {
					spec["syncPolicy"] = map[string]any{"automated": tc.automated}
				}
				status, _ := obj["status"].(map[string]any)
				status["history"] = []any{map[string]any{"id": int64(0), "revision": "abc123", "source": map[string]any{"repoURL": "https://example.com/repo"}}}
			}))
			_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 0})
			if tc.wantErr {
				if !errors.Is(err, ErrAutoSyncEnabled) {
					t.Fatalf("expected ErrAutoSyncEnabled, got %v", err)
				}
				assertNoPatch(t, client)
				return
			}
			if err != nil {
				t.Fatalf("expected rollback to start, got %v", err)
			}
		})
	}
}

func TestArgoHistoryEntryRollbackable(t *testing.T) {
	cases := []struct {
		name  string
		entry map[string]any
		want  bool
	}{
		{name: "single source", entry: map[string]any{"id": int64(0), "source": map[string]any{"repoURL": "https://example.com/repo"}}, want: true},
		{name: "multi source", entry: map[string]any{"id": float64(2), "sources": []any{map[string]any{"repoURL": "https://example.com/repo"}}, "source": map[string]any{"repoURL": ""}}, want: true},
		{name: "empty source object", entry: map[string]any{"id": int64(1), "revision": "abc", "source": map[string]any{"repoURL": ""}}, want: false},
		{name: "no source at all", entry: map[string]any{"id": int64(1), "revision": "abc"}, want: false},
		{name: "no id", entry: map[string]any{"source": map[string]any{"repoURL": "https://example.com/repo"}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ArgoHistoryEntryRollbackable(tc.entry); got != tc.want {
				t.Fatalf("ArgoHistoryEntryRollbackable = %v, want %v", got, tc.want)
			}
		})
	}
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
		status["history"] = []any{map[string]any{"id": int64(1), "source": map[string]any{"repoURL": "https://example.com/repo"}}}
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
		status["history"] = []any{map[string]any{"id": int64(1), "source": map[string]any{"repoURL": "https://example.com/repo"}}}
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
	if !ok || len(info) != 2 {
		t.Fatalf("validation info = %#v, want the provenance and one correlation entry", nestedMap(body, "operation")["info"])
	}
	if !argoOperationHasInfo(nestedMap(body, "operation"), ArgoInitiatedViaInfo, ArgoInitiatedViaRadar) {
		t.Fatalf("validation info lacks the Radar provenance: %#v", info)
	}
	if _, ok := nestedMap(body, "operation")["retry"]; ok {
		t.Fatalf("validation dry-run must not carry a retry strategy: %#v", nestedMap(body, "operation"))
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
// neither is (prune off, selfHeal on) — and clear all the keys regardless.
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
			name:         "never suspended → prune off, as in Argo CD's UI; selfHeal on",
			annotations:  map[string]any{},
			wantPrune:    false,
			wantSelfHeal: true,
		},
		{
			name: "only the legacy selfHeal key → prune off",
			annotations: map[string]any{
				legacyArgoSuspendedSelfHealAnnotation: "false",
			},
			wantPrune:    false,
			wantSelfHeal: false,
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
				ArgoSuspendedAutomatedAnnotation,
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

// Argo CD 3.x can switch automation off in place with automated.enabled:
// false. Resume must clear that flag, or Argo stays manual while Radar reports
// auto-sync resumed, and it keeps the block's own prune/selfHeal settings.
func TestSetArgoAutoSyncResumeKeepsTheBlocksSettings(t *testing.T) {
	for name, automated := range map[string]map[string]any{
		"switched off in place": {"enabled": false, "prune": false, "selfHeal": true},
		"already on":            {"prune": false, "selfHeal": true},
	} {
		t.Run(name, func(t *testing.T) {
			client := newFakeArgo(argoAppForTest("argocd", "demo", func(obj map[string]any) {
				spec, _ := obj["spec"].(map[string]any)
				spec["syncPolicy"] = map[string]any{"automated": automated}
			}))
			// A retried or duplicated resume must not fall back to the defaults.
			for call := 1; call <= 2; call++ {
				if _, err := SetArgoAutoSync(context.Background(), client, "argocd", "demo", true); err != nil {
					t.Fatalf("SetArgoAutoSync(resume) #%d: %v", call, err)
				}
				app, err := client.Resource(argoAppGVR).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
				if err != nil {
					t.Fatalf("get app: %v", err)
				}
				if !ArgoAutoSyncEnabled(app) {
					t.Fatalf("auto-sync off after resume #%d: %#v", call, app.Object["spec"])
				}
				got, _, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
				if got["prune"] != false || got["selfHeal"] != true {
					t.Fatalf("resume #%d changed the block's settings: %#v", call, got)
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
			_, err := SyncFluxWithSource(ctx, c, "Kustomization", "flux-system", "demo", nil)
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
		res, err := SyncFluxWithSource(ctx, client, "Kustomization", "flux-system", "demo", nil)
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

	source := func(kind, namespace, name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "source.toolkit.fluxcd.io/v1",
			"kind":       kind,
			"metadata":   map[string]any{"namespace": namespace, "name": name},
		}}
	}
	release := func(spec, status map[string]any) *unstructured.Unstructured {
		obj := map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata":   map[string]any{"namespace": "team", "name": "demo"},
			"spec":       runtime.DeepCopyJSON(spec),
		}
		if status != nil {
			obj["status"] = status
		}
		return &unstructured.Unstructured{Object: obj}
	}
	inline := map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo", "namespace": "flux-system"}}}}

	chart := func(namespace, name string, sourceRef map[string]any) *unstructured.Unstructured {
		obj := source("HelmChart", namespace, name)
		obj.Object["spec"] = map[string]any{"sourceRef": sourceRef}
		return obj
	}
	repoRef := map[string]any{"kind": "HelmRepository", "name": "repo"}

	// Matches `flux reconcile hr --with-source`: the chart is reconciled with
	// its own source first, so a new chart version in the repository resolves.
	for _, tc := range []struct {
		name    string
		hr      *unstructured.Unstructured
		objects []runtime.Object
		source  string
		want    []string
	}{
		{"inline release reconciles repository, generated chart, release", release(inline, nil),
			[]runtime.Object{source("HelmRepository", "flux-system", "repo"), chart("flux-system", "team-demo", repoRef)},
			"HelmChart flux-system/team-demo", []string{"helmrepositories flux-system/repo", "helmcharts flux-system/team-demo", "helmreleases team/demo"}},
		{"inline release follows the current spec, not a stale recorded chart", release(inline, map[string]any{"helmChart": "old-charts/team-demo"}),
			[]runtime.Object{source("HelmRepository", "flux-system", "repo"), chart("flux-system", "team-demo", repoRef), chart("old-charts", "team-demo", repoRef)},
			"HelmChart flux-system/team-demo", []string{"helmrepositories flux-system/repo", "helmcharts flux-system/team-demo", "helmreleases team/demo"}},
		{"chartRef to an OCIRepository", release(map[string]any{"chartRef": map[string]any{"kind": "OCIRepository", "name": "chart"}}, nil),
			[]runtime.Object{source("OCIRepository", "team", "chart")},
			"OCIRepository team/chart", []string{"ocirepositories team/chart", "helmreleases team/demo"}},
		{"chartRef to a HelmChart in another namespace reconciles the chart's source first", release(map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "shared", "namespace": "charts"}}, nil),
			[]runtime.Object{source("GitRepository", "charts", "git"), chart("charts", "shared", map[string]any{"kind": "GitRepository", "name": "git"})},
			"HelmChart charts/shared", []string{"gitrepositories charts/git", "helmcharts charts/shared", "helmreleases team/demo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleDynamicClient(scheme, append(tc.objects, tc.hr)...)
			res, err := SyncFluxWithSource(ctx, client, "HelmRelease", "team", "demo", nil)
			if err != nil {
				t.Fatalf("SyncFluxWithSource: %v", err)
			}
			if res.Source == nil || res.Source.Kind+" "+res.Source.Namespace+"/"+res.Source.Name != tc.source {
				t.Fatalf("source = %#v, want %s", res.Source, tc.source)
			}
			if got := patchSequence(client); !slices.Equal(got, tc.want) {
				t.Fatalf("patches = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("a generated chart that doesn't exist yet fails before any patch", func(t *testing.T) {
		client := fake.NewSimpleDynamicClient(scheme, source("HelmRepository", "flux-system", "repo"), release(inline, nil))
		_, err := SyncFluxWithSource(ctx, client, "HelmRelease", "team", "demo", nil)
		if !apierrors.IsNotFound(err) {
			t.Fatalf("want NotFound, got %v", err)
		}
		if got := patchSequence(client); len(got) != 0 {
			t.Fatalf("patches = %v", got)
		}
	})

	suspended := func(obj *unstructured.Unstructured) *unstructured.Unstructured {
		spec, _ := obj.Object["spec"].(map[string]any)
		if spec == nil {
			spec = map[string]any{}
		}
		spec["suspend"] = true
		obj.Object["spec"] = spec
		return obj
	}
	for _, tc := range []struct {
		name    string
		objects []runtime.Object
	}{
		{"suspended release", []runtime.Object{source("HelmRepository", "flux-system", "repo"), chart("flux-system", "team-demo", repoRef), suspended(release(inline, nil))}},
		{"suspended generated chart", []runtime.Object{source("HelmRepository", "flux-system", "repo"), suspended(chart("flux-system", "team-demo", repoRef)), release(inline, nil)}},
		{"suspended repository behind the chart", []runtime.Object{suspended(source("HelmRepository", "flux-system", "repo")), chart("flux-system", "team-demo", repoRef), release(inline, nil)}},
	} {
		t.Run(tc.name+" is refused before any patch", func(t *testing.T) {
			client := fake.NewSimpleDynamicClient(scheme, tc.objects...)
			_, err := SyncFluxWithSource(ctx, client, "HelmRelease", "team", "demo", nil)
			if !errors.Is(err, ErrResourceSuspended) {
				t.Fatalf("want ErrResourceSuspended, got %v", err)
			}
			if got := patchSequence(client); len(got) != 0 {
				t.Fatalf("patches = %v", got)
			}
		})
	}

	for _, tc := range []struct {
		name, resource string
		err            error
	}{
		{"an unreadable generated chart", "helmcharts", apierrors.NewForbidden(schema.GroupResource{Group: "source.toolkit.fluxcd.io", Resource: "helmcharts"}, "team-demo", errors.New("denied"))},
		{"an unreadable repository behind the chart", "helmrepositories", apierrors.NewForbidden(schema.GroupResource{Group: "source.toolkit.fluxcd.io", Resource: "helmrepositories"}, "repo", errors.New("denied"))},
		{"a source read that times out", "helmrepositories", apierrors.NewTimeoutError("slow", 1)},
	} {
		t.Run(tc.name+" fails before any patch", func(t *testing.T) {
			client := fake.NewSimpleDynamicClient(scheme, suspended(source("HelmRepository", "flux-system", "repo")), chart("flux-system", "team-demo", repoRef), release(inline, nil))
			client.PrependReactor("get", tc.resource, func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, tc.err })
			_, err := SyncFluxWithSource(ctx, client, "HelmRelease", "team", "demo", nil)
			if err == nil || errors.Is(err, ErrResourceSuspended) || apierrors.ReasonForError(err) != apierrors.ReasonForError(tc.err) {
				t.Fatalf("want the read error %v, got %v", tc.err, err)
			}
			if got := patchSequence(client); len(got) != 0 {
				t.Fatalf("patches = %v", got)
			}
		})
	}

	t.Run("callers without a resolver keep the static versions", func(t *testing.T) {
		client := fake.NewSimpleDynamicClient(scheme, fluxGitRepository("flux-system", "source"),
			fluxKustomization("flux-system", "demo", map[string]any{"kind": "GitRepository", "name": "source"}))
		if _, err := SyncFluxWithSource(ctx, client, "Kustomization", "flux-system", "demo"); err != nil {
			t.Fatalf("SyncFluxWithSource: %v", err)
		}
	})

	t.Run("sources are patched at the version the cluster serves", func(t *testing.T) {
		older := source("OCIRepository", "team", "chart")
		older.SetAPIVersion("source.toolkit.fluxcd.io/v1beta2")
		hr := release(map[string]any{"chartRef": map[string]any{"kind": "OCIRepository", "name": "chart"}}, nil)
		served := func(group, kind string) (schema.GroupVersionResource, bool) {
			if kind == "OCIRepository" {
				return schema.GroupVersionResource{Group: group, Version: "v1beta2", Resource: "ocirepositories"}, true
			}
			return schema.GroupVersionResource{}, false
		}
		if _, err := SyncFluxWithSource(ctx, fake.NewSimpleDynamicClient(scheme, older, hr.DeepCopy()), "HelmRelease", "team", "demo", nil); !apierrors.IsNotFound(err) {
			t.Fatalf("static v1 against a v1beta2-only cluster: want NotFound, got %v", err)
		}
		client := fake.NewSimpleDynamicClient(scheme, older, hr)
		if _, err := SyncFluxWithSource(ctx, client, "HelmRelease", "team", "demo", served); err != nil {
			t.Fatalf("SyncFluxWithSource: %v", err)
		}
		if got := patchSequence(client); !slices.Equal(got, []string{"ocirepositories team/chart", "helmreleases team/demo"}) {
			t.Fatalf("patches = %v", got)
		}
	})

	t.Run("chartRef to an ExternalArtifact reconciles only the release", func(t *testing.T) {
		client := fake.NewSimpleDynamicClient(scheme, release(map[string]any{"chartRef": map[string]any{"kind": "ExternalArtifact", "name": "generated"}}, nil))
		res, err := SyncFluxWithSource(ctx, client, "HelmRelease", "team", "demo", nil)
		if err != nil {
			t.Fatalf("SyncFluxWithSource: %v", err)
		}
		if res.Source != nil || !strings.Contains(res.Message, "produced by another controller") {
			t.Fatalf("result = %#v", res)
		}
		if patches := patchActionsByResource(client); len(patches) != 1 || patches["helmreleases"] != 1 {
			t.Fatalf("patches = %v", patches)
		}
	})

	t.Run("Kustomization from a Bucket", func(t *testing.T) {
		client := fake.NewSimpleDynamicClient(scheme, source("Bucket", "flux-system", "manifests"),
			fluxKustomization("flux-system", "demo", map[string]any{"kind": "Bucket", "name": "manifests"}))
		res, err := SyncFluxWithSource(ctx, client, "Kustomization", "flux-system", "demo", nil)
		if err != nil || res.Source == nil || res.Source.Kind != "Bucket" {
			t.Fatalf("result = %#v err = %v", res, err)
		}
	})

	t.Run("missing sourceRef returns an explicit error before any patch", func(t *testing.T) {
		client := fake.NewSimpleDynamicClient(scheme, fluxKustomization("flux-system", "demo", nil))
		_, err := SyncFluxWithSource(ctx, client, "Kustomization", "flux-system", "demo", nil)
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
		_, err := SyncFluxWithSource(ctx, client, "GitRepository", "flux-system", "source", nil)
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
		_, err := SyncFluxWithSource(ctx, client, "Kustomization", "flux-system", "demo", nil)
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
		"OCIRepository.": {Group: "source.toolkit.fluxcd.io", Version: "v1beta2"},
		"HelmChart":      {Group: "source.toolkit.fluxcd.io", Version: "v1"},
		"Bucket":         {Group: "source.toolkit.fluxcd.io", Version: "v1"},
	} {
		kind = strings.TrimSuffix(kind, ".")
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

// patchSequence lists patches in order as "<resource> <namespace>/<name>".
func patchSequence(client *fake.FakeDynamicClient) []string {
	var out []string
	for _, action := range client.Actions() {
		if pa, ok := action.(clienttesting.PatchAction); ok {
			out = append(out, pa.GetResource().Resource+" "+pa.GetNamespace()+"/"+pa.GetName())
		}
	}
	return out
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

func TestTerminateArgoSyncReportsAlreadyTerminatingAsNoChange(t *testing.T) {
	app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
		obj["operation"] = map[string]any{"sync": map[string]any{"revision": "abc123"}}
		status, _ := obj["status"].(map[string]any)
		status["operationState"] = map[string]any{"phase": "Terminating"}
	})
	client := newFakeArgo(app)

	result, err := TerminateArgoSync(context.Background(), client, "argocd", "demo")
	if err != nil {
		t.Fatalf("TerminateArgoSync: %v", err)
	}
	if !result.NoChange || result.Message != "Termination already in progress" {
		t.Fatalf("result = %+v, want explicit no-change termination", result)
	}
	for _, action := range client.Actions() {
		if _, ok := action.(clienttesting.PatchAction); ok {
			t.Fatalf("already-terminating application was patched: %+v", client.Actions())
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

// completedAutomatedState is what Argo CD leaves in status.operationState after
// an automated sync that needed retries.
func completedAutomatedState() map[string]any {
	return map[string]any{
		"phase":      "Succeeded",
		"message":    "successfully synced (all tasks run)",
		"retryCount": int64(2),
		"startedAt":  "2026-10-10T10:00:00Z",
		"finishedAt": "2026-10-10T10:00:05Z",
		"operation": map[string]any{
			"initiatedBy": map[string]any{"automated": true},
			"retry":       map[string]any{"limit": int64(5)},
			"sync":        map[string]any{"revision": "old", "prune": true},
		},
		"syncResult": map[string]any{"revision": "old", "resources": []any{}},
	}
}

func appWithHistoryAndState(state map[string]any) *unstructured.Unstructured {
	return argoAppForTest("argocd", "demo", func(obj map[string]any) {
		status, _ := obj["status"].(map[string]any)
		status["history"] = []any{map[string]any{"id": int64(1), "revision": "abc", "source": map[string]any{"repoURL": "https://example.com/repo"}}}
		if state != nil {
			status["operationState"] = state
		}
	})
}

// A new operation replaces a completed status.operationState in the same
// write, as Argo CD's SetAppOperation does. Otherwise the controller's merge
// patch keeps the old state's initiatedBy.automated, retryCount and
// syncResult, and its sync-window check reads the old automated state.
func TestArgoOperationsClearCompletedOperationState(t *testing.T) {
	run := map[string]func(*fake.FakeDynamicClient) error{
		"sync": func(client *fake.FakeDynamicClient) error {
			_, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{})
			return err
		},
		"rollback": func(client *fake.FakeDynamicClient) error {
			_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 1})
			return err
		},
	}
	for name, start := range run {
		for stateName, completedState := range map[string]map[string]any{
			"automated": completedAutomatedState(),
			"rollback": {
				"phase":      "Succeeded",
				"operation":  map[string]any{"info": []any{map[string]any{"name": argoRollbackInfoName, "value": "true"}}},
				"syncResult": map[string]any{"revision": "abc123"},
			},
		} {
			t.Run(name+" clears a completed "+stateName+" state", func(t *testing.T) {
				client := newFakeArgo(appWithHistoryAndState(completedState))
				if err := start(client); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				body := captureLastPatch(t, client)
				status, ok := body["status"].(map[string]any)
				if !ok {
					t.Fatalf("patch does not touch status: %#v", body)
				}
				if state, present := status["operationState"]; !present || state != nil {
					t.Fatalf("status.operationState = %#v (present %v), want null", state, present)
				}
				if nestedMap(body, "operation", "sync") == nil {
					t.Fatalf("patch does not install the operation: %#v", body)
				}
				app, err := client.Resource(argoAppGVR).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
				if err != nil {
					t.Fatalf("get app: %v", err)
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(app.Object, "status", "operationState"); found {
					t.Fatalf("stored Application still has the old operationState: %#v", app.Object["status"])
				}
				if _, found, _ := unstructured.NestedSlice(app.Object, "status", "history"); !found {
					t.Fatalf("clearing the operation state dropped status.history: %#v", app.Object["status"])
				}
			})
		}
		t.Run(name+" leaves status alone when there is no state", func(t *testing.T) {
			client := newFakeArgo(appWithHistoryAndState(nil))
			if err := start(client); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if _, ok := captureLastPatch(t, client)["status"]; ok {
				t.Fatalf("patch touches status with no operationState to clear: %#v", captureLastPatch(t, client))
			}
		})
	}
}

// An omitted syncOptions inherits the Application's own, as argocd-server
// does; an explicit list, even an empty one, replaces them. A sync also
// carries the Application's retry strategy, while a rollback never does
// (argocd-server's Rollback doesn't) and still inherits the sync options.
func TestArgoOperationsInheritSyncOptionsAndRetry(t *testing.T) {
	appSyncOptions := []any{"CreateNamespace=true", "ServerSideApply=true"}
	appRetry := map[string]any{"limit": float64(3), "backoff": map[string]any{"duration": "5s"}}
	withPolicy := func() *unstructured.Unstructured {
		app := appWithHistoryAndState(nil)
		spec, _ := app.Object["spec"].(map[string]any)
		spec["syncPolicy"] = map[string]any{"syncOptions": appSyncOptions, "retry": appRetry}
		return app
	}
	syncWith := func(t *testing.T, app *unstructured.Unstructured, opts ArgoSyncOptions) map[string]any {
		t.Helper()
		client := newFakeArgo(app)
		if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", opts); err != nil {
			t.Fatalf("SyncArgoApp: %v", err)
		}
		return nestedMap(captureLastPatch(t, client), "operation")
	}

	t.Run("omitted options inherit the Application's options and retry", func(t *testing.T) {
		operation := syncWith(t, withPolicy(), ArgoSyncOptions{})
		if got := nestedMap(operation, "sync")["syncOptions"]; !reflect.DeepEqual(got, appSyncOptions) {
			t.Fatalf("syncOptions = %#v, want %#v", got, appSyncOptions)
		}
		if got := nestedMap(operation, "retry"); !reflect.DeepEqual(got, appRetry) {
			t.Fatalf("retry = %#v, want %#v", got, appRetry)
		}
	})
	t.Run("explicit options replace the Application's", func(t *testing.T) {
		operation := syncWith(t, withPolicy(), ArgoSyncOptions{SyncOptions: []string{"Replace=true"}})
		if got := nestedMap(operation, "sync")["syncOptions"]; !reflect.DeepEqual(got, []any{"Replace=true"}) {
			t.Fatalf("syncOptions = %#v, want only Replace=true", got)
		}
	})
	t.Run("an explicit empty list syncs with no options", func(t *testing.T) {
		operation := syncWith(t, withPolicy(), ArgoSyncOptions{SyncOptions: []string{}})
		if got, ok := nestedMap(operation, "sync")["syncOptions"]; ok {
			t.Fatalf("syncOptions = %#v, want none", got)
		}
	})
	t.Run("an Application without a policy adds neither", func(t *testing.T) {
		operation := syncWith(t, appWithHistoryAndState(nil), ArgoSyncOptions{})
		if got, ok := nestedMap(operation, "sync")["syncOptions"]; ok {
			t.Fatalf("syncOptions = %#v, want none", got)
		}
		if got, ok := operation["retry"]; ok {
			t.Fatalf("retry = %#v, want none", got)
		}
	})
	t.Run("rollback inherits options but not retry", func(t *testing.T) {
		client := newFakeArgo(withPolicy())
		if _, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 1}); err != nil {
			t.Fatalf("RollbackArgoApp: %v", err)
		}
		operation := nestedMap(captureLastPatch(t, client), "operation")
		if got := nestedMap(operation, "sync")["syncOptions"]; !reflect.DeepEqual(got, appSyncOptions) {
			t.Fatalf("rollback syncOptions = %#v, want %#v", got, appSyncOptions)
		}
		if got, ok := operation["retry"]; ok {
			t.Fatalf("rollback retry = %#v, want none", got)
		}
	})
}

func TestSyncArgoAppRevision(t *testing.T) {
	for _, sourceCount := range []int{0, 1, 2} {
		for _, revision := range []string{"", "release-1"} {
			for _, dryRun := range []bool{false, true} {
				t.Run(fmt.Sprintf("sources=%d/revision=%s/dryRun=%t", sourceCount, revision, dryRun), func(t *testing.T) {
					app := argoAppForTest("argocd", "demo", func(obj map[string]any) {
						sources := make([]any, sourceCount)
						for i := range sources {
							sources[i] = map[string]any{"repoURL": "https://example.com/repo", "targetRevision": "main"}
						}
						obj["spec"].(map[string]any)["sources"] = sources
					})
					client := newFakeArgo(app)
					_, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{Revision: revision, DryRun: &dryRun})
					if sourceCount > 0 && revision != "" {
						if !errors.Is(err, ErrInvalidRevision) || !strings.Contains(err.Error(), "multi-source") {
							t.Fatalf("error = %v, want invalid revision for a multi-source Application", err)
						}
						for _, action := range client.Actions() {
							if action.GetVerb() != "get" {
								t.Fatalf("rejected sync wrote to the Application: %v", action)
							}
						}
						return
					}
					if err != nil {
						t.Fatalf("SyncArgoApp: %v", err)
					}
					if got := nestedMap(captureLastPatch(t, client), "operation", "sync")["revision"]; got != revision {
						t.Fatalf("revision = %v, want %q", got, revision)
					}
				})
			}
		}
	}
}

// The sync fixes the Application's source(s) when it is requested, as
// argocd-server does, so a sync waiting behind a window deploys what was
// there when it was asked for.
func TestSyncArgoAppCapturesSources(t *testing.T) {
	single := map[string]any{"repoURL": "https://example.com/repo", "path": "app", "targetRevision": "HEAD"}
	multi := []any{
		map[string]any{"repoURL": "https://example.com/a", "path": "a", "targetRevision": "main"},
		map[string]any{"repoURL": "https://example.com/b", "path": "b", "targetRevision": "v1"},
	}
	t.Run("single source", func(t *testing.T) {
		client := newFakeArgo(argoAppForTest("argocd", "demo", func(obj map[string]any) {
			obj["spec"].(map[string]any)["source"] = single
		}))
		if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{}); err != nil {
			t.Fatalf("SyncArgoApp: %v", err)
		}
		sync := nestedMap(captureLastPatch(t, client), "operation", "sync")
		if !reflect.DeepEqual(sync["source"], single) || sync["sources"] != nil {
			t.Fatalf("sync source = %#v sources = %#v, want the spec's source only", sync["source"], sync["sources"])
		}
	})
	t.Run("multiple sources", func(t *testing.T) {
		client := newFakeArgo(argoAppForTest("argocd", "demo", func(obj map[string]any) {
			obj["spec"].(map[string]any)["sources"] = multi
		}))
		if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{}); err != nil {
			t.Fatalf("SyncArgoApp: %v", err)
		}
		sync := nestedMap(captureLastPatch(t, client), "operation", "sync")
		if !reflect.DeepEqual(sync["sources"], multi) || sync["source"] != nil {
			t.Fatalf("sync sources = %#v source = %#v, want the spec's sources only", sync["sources"], sync["source"])
		}
	})
}

func TestArgoOperationsAttributeInitiator(t *testing.T) {
	alice := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice@example.com", Groups: []string{"team"}})
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "authenticated user", ctx: alice, want: "alice@example.com"},
		{name: "cloud email", ctx: pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "user_01ABC", Groups: []string{"radar:member", "radar:idp:radar:email:other@example.com", "radar:email:alice@example.com"}}), want: "alice@example.com"},
		{name: "cloud without email", ctx: pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "user_01ABC", Groups: []string{"radar:member"}}), want: "user_01ABC"},
		{name: "empty email group", ctx: pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "user_01ABC", Groups: []string{"radar:email:"}}), want: "user_01ABC"},
		{name: "no per-user auth", ctx: context.Background(), want: "radar"},
		{name: "user without a name", ctx: pkgauth.ContextWithUser(context.Background(), &pkgauth.User{}), want: "radar"},
	}
	for _, tc := range cases {
		for verb, start := range map[string]func(context.Context, *fake.FakeDynamicClient) error{
			"sync": func(ctx context.Context, client *fake.FakeDynamicClient) error {
				_, err := SyncArgoApp(ctx, client, "argocd", "demo", ArgoSyncOptions{})
				return err
			},
			"rollback": func(ctx context.Context, client *fake.FakeDynamicClient) error {
				_, err := RollbackArgoApp(ctx, client, "argocd", "demo", ArgoRollbackOptions{ID: 1})
				return err
			},
		} {
			t.Run(tc.name+"/"+verb, func(t *testing.T) {
				user := pkgauth.UserFromContext(tc.ctx)
				var originalUser pkgauth.User
				if user != nil {
					originalUser = *user
					originalUser.Groups = append([]string(nil), user.Groups...)
				}
				client := newFakeArgo(appWithHistoryAndState(nil))
				if err := start(tc.ctx, client); err != nil {
					t.Fatalf("%s: %v", verb, err)
				}
				if user != nil && !reflect.DeepEqual(*user, originalUser) {
					t.Fatalf("operation changed the authenticated identity: got %#v, want %#v", *user, originalUser)
				}
				operation := nestedMap(captureLastPatch(t, client), "operation")
				if got := nestedMap(operation, "initiatedBy"); !reflect.DeepEqual(got, map[string]any{"username": tc.want}) {
					t.Fatalf("initiatedBy = %#v, want username %q only", got, tc.want)
				}
				if !argoOperationHasInfo(operation, ArgoInitiatedViaInfo, ArgoInitiatedViaRadar) {
					t.Fatalf("operation.info = %#v, want the Radar provenance", operation["info"])
				}
				if got := argoOperationHasInfo(operation, argoRollbackInfoName, "true"); got != (verb == "rollback") {
					t.Fatalf("rollback marker present = %v for %s; info = %#v", got, verb, operation["info"])
				}
			})
		}
	}
}

// conflictOnce makes the first patch fail with a conflict after applying
// change to the stored Application, as a concurrent writer would.
func conflictOnce(t *testing.T, client *fake.FakeDynamicClient, change func(app *unstructured.Unstructured)) *int {
	t.Helper()
	patches := 0
	client.PrependReactor("patch", "applications", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patches++
		if patches > 1 {
			return false, nil, nil
		}
		obj, err := client.Tracker().Get(argoAppGVR, "argocd", "demo")
		if err != nil {
			t.Fatalf("tracker get: %v", err)
		}
		app := obj.(*unstructured.Unstructured).DeepCopy()
		change(app)
		app.SetResourceVersion("18")
		if err := client.Tracker().Update(argoAppGVR, app, "argocd"); err != nil {
			t.Fatalf("tracker update: %v", err)
		}
		return true, nil, apierrors.NewConflict(argoAppGVR.GroupResource(), "demo", errors.New("changed"))
	})
	return &patches
}

// A conflicting write is retried only from a fresh read that is validated
// again: an operation another writer installed in between is refused, and
// a changed Application is written from its new state and resourceVersion.
func TestArgoOperationConflictRereadsAndRevalidates(t *testing.T) {
	seed := func() *unstructured.Unstructured {
		app := appWithHistoryAndState(nil)
		app.SetResourceVersion("17")
		return app
	}
	starts := map[string]func(*fake.FakeDynamicClient) error{
		"sync": func(client *fake.FakeDynamicClient) error {
			_, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{})
			return err
		},
		"rollback": func(client *fake.FakeDynamicClient) error {
			_, err := RollbackArgoApp(context.Background(), client, "argocd", "demo", ArgoRollbackOptions{ID: 1})
			return err
		},
	}
	for verb, start := range starts {
		t.Run(verb+": an operation installed meanwhile is refused", func(t *testing.T) {
			client := newFakeArgo(seed())
			patches := conflictOnce(t, client, func(app *unstructured.Unstructured) {
				app.Object["operation"] = map[string]any{"sync": map[string]any{"revision": "theirs"}}
			})
			if err := start(client); !errors.Is(err, ErrOperationInProgress) {
				t.Fatalf("error = %v, want ErrOperationInProgress", err)
			}
			if *patches != 1 {
				t.Fatalf("patches = %d, want only the conflicting one", *patches)
			}
			app, _ := client.Resource(argoAppGVR).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
			if revision, _, _ := unstructured.NestedString(app.Object, "operation", "sync", "revision"); revision != "theirs" {
				t.Fatalf("the other writer's operation was replaced: %#v", app.Object["operation"])
			}
		})
		t.Run(verb+": a changed Application is written from the new read", func(t *testing.T) {
			client := newFakeArgo(seed())
			patches := conflictOnce(t, client, func(app *unstructured.Unstructured) {
				spec, _ := app.Object["spec"].(map[string]any)
				spec["syncPolicy"] = map[string]any{"syncOptions": []any{"PruneLast=true"}}
			})
			if err := start(client); err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
			if *patches != 2 {
				t.Fatalf("patches = %d, want the conflict and one retry", *patches)
			}
			body := captureLastPatch(t, client)
			if rv := nestedMap(body, "metadata")["resourceVersion"]; rv != "18" {
				t.Fatalf("retry resourceVersion = %#v, want the reread 18", rv)
			}
			if got := nestedMap(body, "operation", "sync")["syncOptions"]; !reflect.DeepEqual(got, []any{"PruneLast=true"}) {
				t.Fatalf("retry syncOptions = %#v, want the reread Application's", got)
			}
		})
	}
	t.Run("a conflict on every attempt gives up with 409", func(t *testing.T) {
		client := newFakeArgo(seed())
		patches := 0
		client.PrependReactor("patch", "applications", func(action clienttesting.Action) (bool, runtime.Object, error) {
			patches++
			return true, nil, apierrors.NewConflict(argoAppGVR.GroupResource(), "demo", errors.New("changed"))
		})
		if _, err := SyncArgoApp(context.Background(), client, "argocd", "demo", ArgoSyncOptions{}); !errors.Is(err, ErrOperationInProgress) {
			t.Fatalf("error = %v, want ErrOperationInProgress", err)
		}
		if patches != argoOperationAttempts {
			t.Fatalf("patches = %d, want %d", patches, argoOperationAttempts)
		}
	})
}

func getTestApp(t *testing.T, client *fake.FakeDynamicClient) *unstructured.Unstructured {
	t.Helper()
	app, err := client.Resource(argoAppGVR).Namespace("argocd").Get(context.Background(), "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	return app
}

func setAutoSync(t *testing.T, client *fake.FakeDynamicClient, enable bool) OperationResult {
	t.Helper()
	result, err := SetArgoAutoSync(context.Background(), client, "argocd", "demo", enable)
	if err != nil {
		t.Fatalf("SetArgoAutoSync(%v): %v", enable, err)
	}
	return result
}

func automatedOf(app *unstructured.Unstructured) (map[string]any, bool) {
	automated, found, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
	return automated, found
}

func appWithAutomated(automated map[string]any, annotations map[string]any) *unstructured.Unstructured {
	return argoAppForTest("argocd", "demo", func(obj map[string]any) {
		if automated != nil {
			obj["spec"].(map[string]any)["syncPolicy"] = map[string]any{"automated": automated}
		}
		if annotations != nil {
			obj["metadata"].(map[string]any)["annotations"] = annotations
		}
	})
}

// Suspend and resume keep the whole automated block, defined for repeated
// suspends and for edits made outside Radar while suspended.
func TestSetArgoAutoSyncLifecycle(t *testing.T) {
	original := map[string]any{"prune": true, "selfHeal": false, "allowEmpty": true}

	t.Run("resume restores every field suspend removed", func(t *testing.T) {
		client := newFakeArgo(appWithAutomated(original, nil))
		setAutoSync(t, client, false)
		suspended := getTestApp(t, client)
		if _, found := automatedOf(suspended); found {
			t.Fatalf("suspend left automated in place: %#v", suspended.Object["spec"])
		}
		annotations := suspended.GetAnnotations()
		if annotations[ArgoSuspendedPruneAnnotation] != "true" || annotations[ArgoSuspendedSelfHealAnnotation] != "false" {
			t.Fatalf("suspend dropped the per-field keys older Radar builds resume from: %#v", annotations)
		}
		setAutoSync(t, client, true)
		resumed := getTestApp(t, client)
		if automated, _ := automatedOf(resumed); !reflect.DeepEqual(automated, original) {
			t.Fatalf("resumed automated = %#v, want %#v", automated, original)
		}
		if len(resumed.GetAnnotations()) != 0 {
			t.Fatalf("resume left suspension annotations: %#v", resumed.GetAnnotations())
		}
	})

	t.Run("a second suspend changes nothing", func(t *testing.T) {
		client := newFakeArgo(appWithAutomated(original, nil))
		setAutoSync(t, client, false)
		before := getTestApp(t, client).GetAnnotations()
		if result := setAutoSync(t, client, false); !result.NoChange {
			t.Fatalf("second suspend = %#v, want NoChange", result)
		}
		if after := getTestApp(t, client).GetAnnotations(); !reflect.DeepEqual(after, before) {
			t.Fatalf("second suspend rewrote the saved settings: %#v, want %#v", after, before)
		}
		setAutoSync(t, client, true)
		if automated, _ := automatedOf(getTestApp(t, client)); !reflect.DeepEqual(automated, original) {
			t.Fatalf("resume after two suspends = %#v, want %#v", automated, original)
		}
	})

	t.Run("suspending an Application without auto-sync writes nothing", func(t *testing.T) {
		client := newFakeArgo(appWithAutomated(nil, nil))
		if result := setAutoSync(t, client, false); !result.NoChange {
			t.Fatalf("suspend = %#v, want NoChange", result)
		}
		assertNoPatch(t, client)
	})

	t.Run("auto-sync re-enabled outside Radar keeps its block", func(t *testing.T) {
		client := newFakeArgo(appWithAutomated(original, nil))
		setAutoSync(t, client, false)
		app := getTestApp(t, client)
		external := map[string]any{"prune": false, "selfHeal": true}
		app.Object["spec"].(map[string]any)["syncPolicy"] = map[string]any{"automated": external}
		if err := client.Tracker().Update(argoAppGVR, app, "argocd"); err != nil {
			t.Fatalf("external edit: %v", err)
		}
		if result := setAutoSync(t, client, true); !result.NoChange {
			t.Fatalf("resume = %#v, want NoChange", result)
		}
		resumed := getTestApp(t, client)
		if automated, _ := automatedOf(resumed); !reflect.DeepEqual(automated, external) {
			t.Fatalf("resume replaced the external block: %#v, want %#v", automated, external)
		}
		if len(resumed.GetAnnotations()) != 0 {
			t.Fatalf("resume left Radar's saved copy: %#v", resumed.GetAnnotations())
		}
	})

	t.Run("a block switched off in place while suspended wins over the saved one", func(t *testing.T) {
		client := newFakeArgo(appWithAutomated(original, nil))
		setAutoSync(t, client, false)
		app := getTestApp(t, client)
		app.Object["spec"].(map[string]any)["syncPolicy"] = map[string]any{"automated": map[string]any{"enabled": false, "prune": false, "allowEmpty": true}}
		if err := client.Tracker().Update(argoAppGVR, app, "argocd"); err != nil {
			t.Fatalf("external edit: %v", err)
		}
		setAutoSync(t, client, true)
		resumed := getTestApp(t, client)
		if automated, _ := automatedOf(resumed); !reflect.DeepEqual(automated, map[string]any{"prune": false, "allowEmpty": true}) {
			t.Fatalf("resumed automated = %#v, want the in-place block without enabled", automated)
		}
		if !ArgoAutoSyncEnabled(resumed) || len(resumed.GetAnnotations()) != 0 {
			t.Fatalf("resume left auto-sync off or annotations behind: %#v", resumed.Object)
		}
	})

	t.Run("a saved block an older Radar's suspend made stale is not used", func(t *testing.T) {
		client := newFakeArgo(appWithAutomated(nil, map[string]any{
			ArgoSuspendedAutomatedAnnotation: `{"prune":true,"selfHeal":true,"allowEmpty":true}`,
			ArgoSuspendedPruneAnnotation:     "false",
			ArgoSuspendedSelfHealAnnotation:  "true",
		}))
		setAutoSync(t, client, true)
		if automated, _ := automatedOf(getTestApp(t, client)); !reflect.DeepEqual(automated, map[string]any{"prune": false, "selfHeal": true}) {
			t.Fatalf("resumed automated = %#v, want the per-field keys", automated)
		}
	})
}

// "Enable Auto-Sync" on an Application Radar never suspended has no saved
// settings to restore. It must not turn pruning on: Argo CD's own UI enables
// auto-sync with prune off, and a block that exists keeps its own values.
func TestSetArgoAutoSyncEnableNeverAutomatedLeavesPruneOff(t *testing.T) {
	client := newFakeArgo(appWithAutomated(nil, nil))
	setAutoSync(t, client, true)
	app := getTestApp(t, client)
	if !ArgoAutoSyncEnabled(app) {
		t.Fatalf("auto-sync still off: %#v", app.Object["spec"])
	}
	if automated, _ := automatedOf(app); !reflect.DeepEqual(automated, map[string]any{"prune": false, "selfHeal": true}) {
		t.Fatalf("enabled automated = %#v, want prune off and selfHeal on", automated)
	}

	saved := newFakeArgo(appWithAutomated(nil, map[string]any{
		ArgoSuspendedPruneAnnotation:    "true",
		ArgoSuspendedSelfHealAnnotation: "false",
	}))
	setAutoSync(t, saved, true)
	if automated, _ := automatedOf(getTestApp(t, saved)); !reflect.DeepEqual(automated, map[string]any{"prune": true, "selfHeal": false}) {
		t.Fatalf("resumed automated = %#v, want the saved prune on and selfHeal off", automated)
	}
}

// A Go caller encoding ArgoSyncOptions for the HTTP API must keep the
// difference between "none" ([]) and "the Application's own" (nil).
func TestArgoSyncOptionsJSONKeepsEmptyList(t *testing.T) {
	for name, tc := range map[string]struct {
		in      []string
		wantNil bool
	}{
		"none":     {in: []string{}},
		"inherit":  {in: nil, wantNil: true},
		"explicit": {in: []string{"Replace=true"}},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(ArgoSyncOptions{SyncOptions: tc.in})
			if err != nil {
				t.Fatal(err)
			}
			var out ArgoSyncOptions
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			if (out.SyncOptions == nil) != tc.wantNil || len(out.SyncOptions) != len(tc.in) {
				t.Fatalf("%s round-tripped to %#v", data, out.SyncOptions)
			}
		})
	}
}

func TestArgoRollbackRevisionMismatch(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		target, result                   []string
		multi, dryRun, unmarked, pending bool
		phase                            string
		wantTarget, wantResult           string
	}{
		{name: "single mismatch", target: []string{"old"}, result: []string{"new"}, wantTarget: "old", wantResult: "new"},
		{name: "single match", target: []string{"old"}, result: []string{"old"}},
		{name: "multi mismatch", multi: true, target: []string{"old-a", "old-b"}, result: []string{"new-a", "new-b"}, wantTarget: "old-a, old-b", wantResult: "new-a, new-b"},
		{name: "multi partial mismatch", multi: true, target: []string{"old-a", "old-b"}, result: []string{"old-a", "new-b"}, wantTarget: "old-a, old-b", wantResult: "old-a, new-b"},
		{name: "multi match", multi: true, target: []string{"a", "b"}, result: []string{"a", "b"}},
		{name: "dry run", dryRun: true, target: []string{"old"}, result: []string{"new"}},
		{name: "multi dry run", multi: true, dryRun: true, target: []string{"old"}, result: []string{"new"}},
		{name: "unmarked sync", unmarked: true, target: []string{"old"}, result: []string{"new"}},
		{name: "failed", phase: "Failed", target: []string{"old"}, result: []string{"new"}},
		{name: "running", phase: "Running", target: []string{"old"}, result: []string{"new"}},
		{name: "pending operation", pending: true, target: []string{"old"}, result: []string{"new"}},
		{name: "no result", target: []string{"old"}},
		{name: "no target", result: []string{"new"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := argoAppForTest("argocd", "demo", nil)
			sync, result := map[string]any{"dryRun": tc.dryRun}, map[string]any{}
			if tc.multi {
				_ = unstructured.SetNestedStringSlice(sync, tc.target, "revisions")
				_ = unstructured.SetNestedStringSlice(result, tc.result, "revisions")
			} else {
				if len(tc.target) > 0 {
					sync["revision"] = tc.target[0]
				}
				if len(tc.result) > 0 {
					result["revision"] = tc.result[0]
				}
			}
			operation := map[string]any{"sync": sync, "initiatedBy": map[string]any{"username": "alice"}}
			if !tc.unmarked {
				operation["info"] = []any{map[string]any{"name": argoRollbackInfoName, "value": "true"}}
			}
			phase := tc.phase
			if phase == "" {
				phase = "Succeeded"
			}
			_ = unstructured.SetNestedMap(app.Object, map[string]any{"phase": phase, "operation": operation, "syncResult": result}, "status", "operationState")
			if tc.pending {
				app.Object["operation"] = map[string]any{"sync": map[string]any{}}
			}
			target, applied := ArgoRollbackRevisionMismatch(app)
			if target != tc.wantTarget || applied != tc.wantResult {
				t.Fatalf("mismatch = (%q, %q), want (%q, %q)", target, applied, tc.wantTarget, tc.wantResult)
			}
			if tc.wantTarget != "" {
				if revision, _ := ArgoSuccessfulRollback(app); revision != "" {
					t.Fatalf("mismatch reported as successful rollback: %q", revision)
				}
			}
		})
	}
}

func TestArgoRollbackSourceComparison(t *testing.T) {
	source := map[string]any{
		"repoURL": "https://example.com/repo", "path": "historical", "targetRevision": "main",
		"helm": map[string]any{"parameters": []any{map[string]any{"name": "replicas", "value": "2"}}},
	}
	for _, multi := range []bool{false, true} {
		for _, tc := range []struct {
			name                       string
			field                      string
			value                      any
			missingResult, placeholder bool
			mismatch                   bool
		}{
			{name: "identical"},
			{name: "path differs at same revision", field: "path", value: "current", mismatch: true},
			{name: "repo differs", field: "repoURL", value: "https://example.com/other", mismatch: true},
			{name: "chart differs", field: "chart", value: "other", mismatch: true},
			{name: "target revision differs", field: "targetRevision", value: "other", mismatch: true},
			{name: "helm values differ", field: "helm", value: map[string]any{"parameters": []any{map[string]any{"name": "replicas", "value": "3"}}}, mismatch: true},
			{name: "kustomize differs", field: "kustomize", value: map[string]any{"namePrefix": "other"}, mismatch: true},
			{name: "directory differs", field: "directory", value: map[string]any{"recurse": true}, mismatch: true},
			{name: "plugin differs", field: "plugin", value: map[string]any{"name": "other"}, mismatch: true},
			{name: "missing result sources", missingResult: true},
			{name: "zero single source placeholder", placeholder: true},
			{name: "missing plural result with placeholder", missingResult: true, placeholder: true},
			{name: "placeholder does not hide plural mismatch", placeholder: true, field: "path", value: "current", mismatch: true},
		} {
			t.Run(fmt.Sprintf("multi=%v/%s", multi, tc.name), func(t *testing.T) {
				sync := map[string]any{"revision": "same"}
				result := map[string]any{"revision": "same"}
				appliedSource := (&unstructured.Unstructured{Object: source}).DeepCopy().Object
				if tc.field != "" {
					appliedSource[tc.field] = tc.value
				}
				if multi {
					sync = map[string]any{"revisions": []any{"same", "values"}, "sources": []any{source, map[string]any{"repoURL": "https://example.com/values", "ref": "values"}}}
					result = map[string]any{"revisions": []any{"same", "values"}}
					if !tc.missingResult {
						result["sources"] = []any{appliedSource, map[string]any{"repoURL": "https://example.com/values", "ref": "values"}}
					}
					if tc.placeholder {
						sync["source"] = map[string]any{}
						result["source"] = map[string]any{"repoURL": ""}
					}
				} else {
					sync["source"] = source
					if !tc.missingResult {
						result["source"] = appliedSource
					}
				}
				app := argoAppForTest("argocd", "demo", nil)
				_ = unstructured.SetNestedMap(app.Object, map[string]any{
					"phase":      "Succeeded",
					"operation":  map[string]any{"sync": sync, "info": []any{map[string]any{"name": argoRollbackInfoName, "value": "true"}}},
					"syncResult": result,
				}, "status", "operationState")
				target, applied := ArgoRollbackRevisionMismatch(app)
				if got := target != "" && applied != ""; got != tc.mismatch {
					t.Errorf("mismatch = (%q, %q), want mismatch %v", target, applied, tc.mismatch)
				}
				revision, _ := ArgoSuccessfulRollback(app)
				if got := revision != ""; got == tc.mismatch {
					t.Errorf("successful rollback = %q, want success %v", revision, !tc.mismatch)
				}
			})
		}
	}
}
