package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func TestBootstrapResourcePermissionsRetriesNamespace401BeforeWiring(t *testing.T) {
	rejected := &PermissionCheckResult{Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "beta"}}, NamespaceProbeErrors: map[string]map[string]error{"pods": {"alpha": apierrors.NewUnauthorized("expired startup token")}}}
	recovered := &PermissionCheckResult{Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true}}, ScopeCandidates: []string{"alpha", "beta"}}
	calls := 0
	result, err := bootstrapResult(context.Background(), func(ctx context.Context, retry bool) *PermissionCheckResult {
		calls++
		if retry != (calls == 2) {
			t.Fatalf("retry=%v at call %d", retry, calls)
		}
		if calls == 1 {
			return rejected
		}
		return recovered
	})
	if err != nil || result != recovered || calls != 2 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
	}
}

func TestBootstrapResourcePermissionsPersistent401FailsIntoAuthClassification(t *testing.T) {
	for _, namespaceFailure := range []bool{false, true} {
		// Every kind ends disabled by the rejection, as probeResourceAccess
		// records it: a namespace rejection also sets the kind's ProbeErrors.
		rejection := apierrors.NewUnauthorized("credential rejected")
		result := &PermissionCheckResult{Perms: &ResourcePermissions{}, Scopes: map[string]k8score.ResourceScope{"pods": {}}, ProbeErrors: map[string]error{"pods": rejection}}
		if namespaceFailure {
			result.NamespaceProbeErrors = map[string]map[string]error{"pods": {"alpha": rejection}}
		}
		calls := 0
		got, err := bootstrapResult(context.Background(), func(context.Context, bool) *PermissionCheckResult { calls++; return result })
		if got != nil || !apierrors.IsUnauthorized(err) || !isAuthClassification(ClassifyError(err)) || calls != 2 {
			t.Fatalf("namespace=%v result=%+v err=%v classification=%q calls=%d", namespaceFailure, got, err, ClassifyError(err), calls)
		}
	}
}

// A rejection limited to some probes, while others authenticated, keeps what
// they granted instead of failing the connection.
func TestBootstrapResourcePermissionsPartial401KeepsAuthenticatedGrants(t *testing.T) {
	rejection := apierrors.NewUnauthorized("token expired mid-probe")
	for name, result := range map[string]*PermissionCheckResult{
		"one namespace": {Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "beta"}}, NamespaceProbeErrors: map[string]map[string]error{"pods": {"alpha": rejection}}},
		"one kind":      {Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true}, "secrets": {}}, ProbeErrors: map[string]error{"secrets": rejection}},
		"beside denial": {Perms: &ResourcePermissions{}, Scopes: map[string]k8score.ResourceScope{"pods": {}, "secrets": {}}, ProbeErrors: map[string]error{"secrets": rejection}},
	} {
		calls := 0
		got, err := bootstrapResult(context.Background(), func(_ context.Context, retry bool) *PermissionCheckResult {
			calls++
			return result
		})
		if err != nil || got != result || calls != 2 {
			t.Fatalf("%s: result=%+v err=%v calls=%d", name, got, err, calls)
		}
	}
}

func TestCachePermissionEvidenceSurvivesIndependentGlobalRefresh(t *testing.T) {
	bound := &PermissionCheckResult{Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "alpha"}}, ScopeNamespaces: map[string][]string{"pods": {"alpha", "beta"}}}
	cache := &ResourceCache{permissionResult: clonePermissionCheckResult(bound)}
	restore := SetTestPermissionResult(&PermissionCheckResult{Perms: &ResourcePermissions{}, ProbeErrors: map[string]error{"pods": apierrors.NewUnauthorized("another generation")}})
	defer restore()
	evidence := cache.PermissionResult()
	if evidence.Scopes["pods"].Namespace != "alpha" || evidence.ProbeErrors["pods"] != nil {
		t.Fatalf("independent global evidence leaked into captured cache: %+v", evidence)
	}
	evidence.ScopeNamespaces["pods"][0] = "mutated"
	if cache.PermissionResult().ScopeNamespaces["pods"][0] != "alpha" {
		t.Fatal("caller mutated cache-bound evidence")
	}
}

func TestBootstrapNamespace401RecoveryWiresRecoveredInformerCoverage(t *testing.T) {
	dyn := fakeDyn(t, func(_ schema.GroupVersionResource, ns string) bool { return ns == "alpha" || ns == "beta" }).(*dynamicfake.FakeDynamicClient)
	first := true
	dyn.PrependReactor("list", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "alpha" && first {
			first = false
			return true, nil, apierrors.NewUnauthorized("startup token expired")
		}
		return false, nil, nil
	})
	calls := 0
	recovered, err := bootstrapResult(context.Background(), func(ctx context.Context, retry bool) *PermissionCheckResult {
		calls++
		result, _ := probeResourceAccess(ctx, dyn, []string{"alpha", "beta"}, false)
		if calls == 1 && (!apierrors.IsUnauthorized(result.NamespaceProbeErrors["pods"]["alpha"]) || result.Scopes["pods"].Namespace != "beta") {
			t.Fatalf("failed to reproduce narrowed startup scope: %+v", result)
		}
		return result
	})
	if err != nil || calls != 2 {
		t.Fatalf("recovery err=%v calls=%d", err, calls)
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: kubernetesfake.NewClientset(), ResourceScopes: recovered.Scopes, ResourceScopeNamespaces: recovered.ScopeNamespaces})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	if assessment := core.AssessKindRead("pods", []string{"alpha", "beta"}); !assessment.Complete {
		t.Fatalf("recovered credentials still froze narrowed scope: %+v", assessment)
	}
}

func TestBootstrapProbeBudgetExpiryKeepsNonAuthGrants(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	grants := &PermissionCheckResult{Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "alpha"}}, ScopeCandidatesTruncated: true}
	calls := 0
	got, err := bootstrapResult(ctx, func(context.Context, bool) *PermissionCheckResult { calls++; return grants })
	if err != nil || got != grants || calls != 1 {
		t.Fatalf("probe budget discarded completed grants: got=%+v err=%v calls=%d", got, err, calls)
	}
}

func TestBootstrap401RetryBudgetExpiryDoesNotPublishFalseRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	rejected := &PermissionCheckResult{Perms: &ResourcePermissions{}, Scopes: map[string]k8score.ResourceScope{"pods": {}}, ProbeErrors: map[string]error{"pods": apierrors.NewUnauthorized("startup credentials rejected")}}
	calls := 0
	got, err := bootstrapResult(ctx, func(context.Context, bool) *PermissionCheckResult {
		calls++
		if calls == 1 {
			return rejected
		}
		<-ctx.Done()
		return &PermissionCheckResult{Perms: &ResourcePermissions{}, Scopes: map[string]k8score.ResourceScope{}}
	})
	if got != nil || !apierrors.IsUnauthorized(err) || calls != 2 {
		t.Fatalf("expired retry implied credentials recovered: got=%+v err=%v calls=%d", got, err, calls)
	}
}

// A retry the budget cut short counts unfinished probes as allowed, so the
// first result stands: its authenticated grants are kept, and only credentials
// rejected everywhere fail the connection.
func TestBootstrapCutShortRetryKeepsTheFirstResult(t *testing.T) {
	rejection := apierrors.NewUnauthorized("token expired mid-probe")
	partial := &PermissionCheckResult{Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "beta"}, "secrets": {}}, ProbeErrors: map[string]error{"secrets": rejection}, NamespaceProbeErrors: map[string]map[string]error{"pods": {"alpha": rejection}}}
	for name, guess := range map[string]*PermissionCheckResult{
		"recovered":      {Perms: &ResourcePermissions{Pods: true, Secrets: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true}, "secrets": {Enabled: true}}},
		"still rejected": {Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true}, "secrets": {}}, ProbeErrors: map[string]error{"secrets": rejection}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		calls := 0
		got, err := bootstrapResult(ctx, func(ctx context.Context, retry bool) *PermissionCheckResult {
			calls++
			if !retry {
				return partial
			}
			<-ctx.Done()
			return guess
		})
		cancel()
		if err != nil || got != partial || calls != 2 {
			t.Fatalf("%s: a cut-short retry replaced the first result: got=%+v err=%v calls=%d", name, got, err, calls)
		}
	}
}

// Startup publishes only the result its informers are built from. Probing
// publishes nothing, so a retry the budget cut short never becomes the
// process-wide result that capabilities, visibility and CRD scoping read.
func TestBootstrapPublishesOnlyTheChosenResult(t *testing.T) {
	InvalidateResourcePermissionsCache()
	t.Cleanup(InvalidateResourcePermissionsCache)
	prevClient := SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: "http://127.0.0.1:1"}))
	t.Cleanup(func() { SetTestClient(prevClient) })
	t.Cleanup(SetTestPermissionProbeClient(func() {}))
	if probe := freshResourcePermissions(context.Background(), false); probe.result == nil || GetCachedPermissionResult() != nil {
		t.Fatalf("probing published its result: %+v", GetCachedPermissionResult())
	}

	rejection := apierrors.NewUnauthorized("token expired mid-probe")
	partial := &PermissionCheckResult{Perms: &ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "beta"}, "secrets": {}}, ProbeErrors: map[string]error{"secrets": rejection}}
	guess := &PermissionCheckResult{Perms: &ResourcePermissions{Pods: true, Secrets: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true}, "secrets": {Enabled: true}}}
	resourcePermsMu.RLock()
	gen := resourcePermsGen
	resourcePermsMu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	chosen, err := bootstrapResourcePermissions(ctx, func(ctx context.Context, retry bool) permissionProbe {
		if !retry {
			return permissionProbe{result: partial, hadErrors: true, gen: gen, unpublished: true}
		}
		<-ctx.Done()
		return permissionProbe{result: guess, gen: gen, unpublished: true}
	})
	if err != nil || chosen.result != partial {
		t.Fatalf("a cut-short retry replaced the first result: got=%+v err=%v", chosen.result, err)
	}
	if GetCachedPermissionResult() != nil {
		t.Fatalf("bootstrap published before startup chose: %+v", GetCachedPermissionResult())
	}
	if !publishResourcePermissions(chosen) {
		t.Fatal("the chosen result was not current")
	}
	got := GetCachedPermissionResult()
	if got == nil || got.Perms.Secrets || got.Scopes["pods"].Namespace != "beta" {
		t.Fatalf("the cache differs from the informers' result: %+v", got)
	}
}

// The startup probe's retry probes again instead of reading back the cached
// rejection it is retrying.
func TestFreshResourcePermissionsRetryProbesAgain(t *testing.T) {
	InvalidateResourcePermissionsCache()
	t.Cleanup(InvalidateResourcePermissionsCache)
	prevClient := SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: "http://127.0.0.1:1"}))
	t.Cleanup(func() { SetTestClient(prevClient) })
	lists := 0
	t.Cleanup(SetTestPermissionProbeClient(func() { lists++ }))
	cached := &PermissionCheckResult{Perms: &ResourcePermissions{}, Scopes: map[string]k8score.ResourceScope{"pods": {}}, ProbeErrors: map[string]error{"pods": apierrors.NewUnauthorized("expired")}}
	t.Cleanup(SetTestPermissionResult(cached))

	if got := freshResourcePermissions(context.Background(), false).result; got.ProbeErrors["pods"] == nil || lists != 0 {
		t.Fatalf("the first probe should read the cached result: %+v lists=%d", got, lists)
	}
	got := freshResourcePermissions(context.Background(), true).result
	if lists == 0 || len(got.ProbeErrors) != 0 || !got.Scopes["pods"].Enabled {
		t.Fatalf("the retry replayed the cached rejection: errors=%v pods=%+v lists=%d", got.ProbeErrors, got.Scopes["pods"], lists)
	}
}

// bootstrapResult runs bootstrapResourcePermissions over probes that return
// plain results, as most of these tests write them.
func bootstrapResult(ctx context.Context, check func(context.Context, bool) *PermissionCheckResult) (*PermissionCheckResult, error) {
	probe, err := bootstrapResourcePermissions(ctx, func(ctx context.Context, retry bool) permissionProbe {
		return permissionProbe{result: check(ctx, retry)}
	})
	return probe.result, err
}
