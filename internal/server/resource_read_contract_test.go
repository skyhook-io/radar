package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// Every code a read can fail with has an HTTP status, so none falls through
// to a 500 that hides its meaning.
func TestEveryReadCodeHasAStatus(t *testing.T) {
	for _, code := range readreason.Codes() {
		if _, ok := readStatus[code]; !ok {
			t.Errorf("%s has no HTTP status", code)
		}
	}
	// Radar's own collection is never the caller's 401/403, and a limit of
	// what it collects is never a retryable 503.
	for _, code := range []readreason.Code{readreason.RadarForbidden, readreason.RadarCredentialsRejected, readreason.RadarNamespaceNotCollected} {
		if status := readStatusFor(code); status == http.StatusUnauthorized || status == http.StatusForbidden {
			t.Errorf("%s answers %d, which reads as the caller's denial", code, status)
		}
	}
	if readStatusFor(readreason.RadarNamespaceNotCollected) == http.StatusServiceUnavailable {
		t.Error("a collection limit must not invite a retry")
	}
}

// useLoadingConfigMaps installs a cache that watches ConfigMaps in alpha and
// gamma, where alpha has synced and gamma never answers.
func useLoadingConfigMaps(t *testing.T) {
	t.Helper()
	client := fake.NewClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "alpha-cm"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "gamma", Name: "gamma-cm"}},
	)
	client.PrependReactor("list", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "gamma" {
			return true, nil, errors.New("gamma does not answer")
		}
		return false, nil, nil
	})
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:                  client,
		ResourceTypes:           map[string]bool{"configmaps": true},
		ResourceScopes:          map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}},
		ResourceScopeNamespaces: map[string][]string{"configmaps": {"alpha", "gamma"}},
		DeferredTypes:           map[string]bool{"configmaps": true},
		DeferredSyncTimeout:     time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !k8s.GetResourceCache().AssessKindRead("configmaps", []string{"alpha"}).Complete {
		if time.Now().After(deadline) {
			t.Fatal("alpha never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A built-in kind watched namespace by namespace is listed, got and counted in
// a namespace whose informer has synced while another is still loading; the
// loading namespace is a gap rather than a reason to refuse the read.
func TestResourceReadsArePerNamespaceForBuiltInKinds(t *testing.T) {
	useLoadingConfigMaps(t)

	var list testResourceListEnvelope
	if resp := get(t, "/api/resources/configmaps?namespaces=alpha,gamma"); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	} else {
		assertOK(t, resp, &list)
	}
	pending := slices.IndexFunc(list.Coverage.Gaps, func(gap resourceapi.Gap) bool { return gap.Code == readreason.KindSyncPending })
	if len(list.Items) != 1 || mustNested(t, list.Items[0], "metadata", "name") != "alpha-cm" || list.Coverage.Status != resourceapi.CoveragePartial ||
		pending < 0 || !slices.Equal(list.Coverage.Gaps[pending].Namespaces.Names, []string{"gamma"}) || !slices.Equal(list.Coverage.Read.Namespaces.Names, []string{"alpha"}) {
		t.Fatalf("list while gamma loads = %+v", list)
	}

	var only testResourceListEnvelope
	assertOK(t, get(t, "/api/resources/configmaps?namespace=alpha"), &only)
	if only.Coverage.Status != resourceapi.CoverageComplete || len(only.Items) != 1 {
		t.Fatalf("a synced namespace alone is complete: %+v", only)
	}

	assertOK(t, get(t, "/api/resources/configmaps/alpha/alpha-cm"), nil)
	missing := get(t, "/api/resources/configmaps/alpha/missing")
	defer missing.Body.Close()
	assertResourceReadError(t, missing, http.StatusNotFound, "object_not_found")
	loading := get(t, "/api/resources/configmaps/gamma/gamma-cm")
	defer loading.Body.Close()
	assertResourceReadError(t, loading, http.StatusServiceUnavailable, "kind_sync_pending")

	var counts ResourceCountsResponse
	assertOK(t, get(t, "/api/resource-counts?namespaces=alpha,gamma"), &counts)
	if counts.Counts["ConfigMap"] != 1 || counts.Coverage["ConfigMap"].Status != resourceapi.CoveragePartial {
		t.Fatalf("a partial count is the synced namespaces' count, qualified: %+v %+v", counts.Counts, counts.Coverage["ConfigMap"])
	}
}

// An unscoped list a restricted caller's access narrowed says so; a list of
// namespaces the caller named does not, and a named namespace the caller
// can't see stays in scope as a gap, named back like every other one.
func TestResourceListScopeProvenance(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "a"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "beta", Name: "b"}},
	)
	useTestResourceCache(t, client)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("scoped", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
	decode := func(resp *http.Response) testResourceListEnvelope {
		t.Helper()
		var body testResourceListEnvelope
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	implicit := decode(env.authGet(t, "/api/resources/services", "scoped", ""))
	if implicit.Coverage.Status != resourceapi.CoverageComplete || !slices.Equal(implicit.Coverage.NarrowedBy, []resourceapi.Narrowing{resourceapi.NarrowedByUserAccess}) ||
		!slices.Equal(implicit.Coverage.Scope.Namespaces.Names, []string{"alpha"}) {
		t.Fatalf("narrowed implicit list = %+v", implicit.Coverage)
	}

	named := decode(env.authGet(t, "/api/resources/services?namespaces=alpha,secret-tenant", "scoped", ""))
	hidden := slices.IndexFunc(named.Coverage.Gaps, func(gap resourceapi.Gap) bool { return gap.Code == readreason.UserNamespaceNotVisible })
	if named.Coverage.Status != resourceapi.CoveragePartial || len(named.Coverage.NarrowedBy) != 0 || hidden < 0 ||
		!slices.Equal(named.Coverage.Gaps[hidden].Namespaces.Names, []string{"secret-tenant"}) {
		t.Fatalf("named list = %+v", named.Coverage)
	}
	// The answer is about both namespaces named; only alpha was read.
	if !slices.Equal(named.Coverage.Scope.Namespaces.Names, []string{"alpha", "secret-tenant"}) || !slices.Equal(named.Coverage.Read.Namespaces.Names, []string{"alpha"}) {
		t.Fatalf("named scope = %+v, read = %+v", named.Coverage.Scope.Namespaces, named.Coverage.Read.Namespaces)
	}
	// Whether secret-tenant exists can't be told apart from the caller not
	// seeing it: a name the caller sent back is the same either way.
	missing := decode(env.authGet(t, "/api/resources/services?namespaces=alpha,no-such-namespace", "scoped", ""))
	if gap := slices.IndexFunc(missing.Coverage.Gaps, func(gap resourceapi.Gap) bool { return gap.Code == readreason.UserNamespaceNotVisible }); gap < 0 ||
		!slices.Equal(missing.Coverage.Gaps[gap].Namespaces.Names, []string{"no-such-namespace"}) {
		t.Fatalf("a missing namespace must read like an unseen one: %+v", missing.Coverage)
	}
}

// Every way a resource GET is refused carries a code, so a client can tell
// the caller's denial from a namespace they can't see or a failed check.
func TestResourceGetDenialsAreCoded(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "token"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "api"}},
	)
	useTestResourceCache(t, client)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}}
	perms.SetCanI("get", "", "secrets", "alpha", false)
	env.srv.permCache.Set("scoped", nil, perms)
	for _, tc := range []struct {
		path   string
		status int
		code   string
	}{
		{"/api/resources/secrets/alpha/token", http.StatusForbidden, "user_forbidden"},
		{"/api/resources/services/beta/api", http.StatusForbidden, "user_namespace_not_visible"},
		{"/api/ai/resources/secrets/alpha/token", http.StatusForbidden, "user_forbidden"},
	} {
		resp := env.authGet(t, tc.path, "scoped", "")
		assertResourceReadError(t, resp, tc.status, tc.code)
		resp.Body.Close()
	}
	// A caller whose namespace access couldn't be determined is told to
	// retry, not that they have none.
	resp := env.authGet(t, "/api/resources/services", "unknown-caller", "")
	defer resp.Body.Close()
	assertResourceReadError(t, resp, http.StatusServiceUnavailable, "user_permission_check_failed")
}

// A read of the whole readable target never includes a named namespace the
// caller can't see: the scope does, the read set does not.
func TestWholeTargetReadExcludesUnseenNamespaces(t *testing.T) {
	target := listTarget{explicit: true, namespaces: []string{"alpha"}, named: []string{"alpha", "beta"},
		gaps: []resourceapi.Gap{gapOf(readreason.UserNamespaceNotVisible, []string{"beta"})}}
	cov := target.coverage(nil, nil)
	if cov.Scope.Namespaces.Count != 2 || cov.Read.Namespaces.Count != 1 || !slices.Equal(cov.Read.Namespaces.Names, []string{"alpha"}) || cov.Status != resourceapi.CoveragePartial {
		t.Fatalf("coverage = scope %+v read %+v status %s", cov.Scope.Namespaces, cov.Read.Namespaces, cov.Status)
	}
}

// The AI list answers with a bare array, which can't say what it left out,
// so it refuses a request it can read only part of: a named namespace the
// caller can't see, or a collector that covers only some namespaces.
func TestAIListRefusesWhatABareArrayCantQualify(t *testing.T) {
	client := fake.NewClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "a"}})
	useTestResourceCache(t, client)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("scoped", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
	resp := env.authGet(t, "/api/ai/resources/services?namespaces=alpha,hidden", "scoped", "")
	defer resp.Body.Close()
	assertResourceReadError(t, resp, http.StatusForbidden, "user_namespace_not_visible")

	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCacheNamespaces(client, map[string]k8score.ResourceScope{k8score.Services: {Enabled: true, Namespace: "alpha"}}, map[string][]string{k8score.Services: {"alpha"}}); err != nil {
		t.Fatal(err)
	}
	narrowed := get(t, "/api/ai/resources/services")
	defer narrowed.Body.Close()
	assertResourceReadError(t, narrowed, http.StatusConflict, "radar_namespace_not_collected")
	named := get(t, "/api/ai/resources/services?namespace=alpha")
	defer named.Body.Close()
	if named.StatusCode != http.StatusOK {
		t.Fatalf("a namespace Radar collects is answered whole: %d", named.StatusCode)
	}
}
