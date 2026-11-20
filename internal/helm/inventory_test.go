package helm

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"helm.sh/helm/v3/pkg/release"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/internal/search"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func denySecrets(namespaces ...string) ktesting.ReactionFunc {
	return func(action ktesting.Action) (bool, runtime.Object, error) {
		for _, ns := range namespaces {
			if action.GetNamespace() == ns {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New(`User "dev" cannot list resource "secrets"`))
			}
		}
		return false, nil, nil
	}
}

func TestListReleasesRecordingAccess(t *testing.T) {
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "apps", helmTestRelease("api", "apps", 1, release.StatusDeployed, "Install complete"), false))
	client.PrependReactor("list", "secrets", denySecrets("team-b"))

	rows, read, denied, err := listReleasesRecordingAccess(context.Background(), client, "", []string{"apps", "team-b"}, scannedObjects{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !reflect.DeepEqual(read, []string{"apps"}) || !reflect.DeepEqual(denied, []string{"team-b"}) {
		t.Fatalf("rows %+v read %v denied %v", rows, read, denied)
	}
}

func TestListReleasesRecordingAccessTreatsACompleteDenialAsAResult(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "secrets", denySecrets(""))

	rows, read, denied, err := listReleasesRecordingAccess(context.Background(), client, "", nil, scannedObjects{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || rows == nil || len(read) != 0 || !reflect.DeepEqual(denied, []string{""}) {
		t.Fatalf("rows %+v read %v denied %v", rows, read, denied)
	}
}

func TestListReleasesRecordingAccessKeepsOtherFailures(t *testing.T) {
	for name, failure := range map[string]error{
		"rejected credentials": apierrors.NewUnauthorized("Unauthorized"),
		"impersonation":        apierrors.NewForbidden(schema.GroupResource{Resource: "users"}, "dev", errors.New(`User "radar" cannot impersonate resource "users"`)),
		"server error":         errors.New("etcd is down"),
	} {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("list", "secrets", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure })
			if _, _, _, err := listReleasesRecordingAccess(context.Background(), client, "", nil, scannedObjects{}); err == nil {
				t.Fatal("a failure that isn't the caller's denial must not read as one")
			}
		})
	}
}

func scopeSeeing(namespaces []string, requested []string) *ObjectScope {
	return &ObjectScope{Access: search.ScanAccess{Selection: readscope.Selection{Namespaces: namespaces}}, Requested: requested}
}

func TestUnreadStorage(t *testing.T) {
	user := &pkgauth.User{Username: "dev"}
	listed := func(names ...string) *resourceapi.NameSet { return resourceapi.Names(names, resourceapi.RESTSetLimit) }
	for _, tc := range []struct {
		name         string
		req          InventoryRequest
		read, denied []string
		want         *resourceapi.NameSet
	}{
		{
			name:   "cluster-wide denial",
			req:    InventoryRequest{User: user, Scope: scopeSeeing(nil, nil)},
			denied: []string{""},
			want:   resourceapi.AllNames(),
		},
		{
			name: "no namespace access at all",
			req:  InventoryRequest{StorageNamespaces: []string{}, User: user, Scope: scopeSeeing([]string{}, nil)},
			want: resourceapi.AllNames(),
		},
		{
			name:   "every namespace denied",
			req:    InventoryRequest{StorageNamespaces: []string{"a", "b"}, User: user, Scope: scopeSeeing([]string{"a", "b"}, nil)},
			denied: []string{"a", "b"},
			want:   listed("a", "b"),
		},
		{
			name:   "a namespace named and denied",
			req:    InventoryRequest{StorageNamespaces: []string{"a", "b"}, User: user, Scope: scopeSeeing([]string{"a", "b"}, []string{"a", "b"})},
			read:   []string{"a"},
			denied: []string{"b"},
			want:   listed("b"),
		},
		{
			// The resolver narrowed storage to where Secrets can be listed, so
			// the storage read never met b. It still can't be answered from Helm.
			name: "a namespace the storage scope left out",
			req:  InventoryRequest{StorageNamespaces: []string{"a"}, User: user, Scope: scopeSeeing([]string{"a", "b"}, nil)},
			read: []string{"a"},
			want: listed("b"),
		},
		{
			name: "everything asked about was read",
			req:  InventoryRequest{StorageNamespaces: []string{"a"}, User: user, Scope: scopeSeeing([]string{"a", "b"}, []string{"a"})},
			read: []string{"a"},
		},
		{
			// With no user the resolver's list is the identity's whole reach,
			// so a namespace outside it isn't a denial.
			name: "no user and nothing denied",
			req:  InventoryRequest{StorageNamespaces: []string{"a"}, Scope: scopeSeeing(nil, nil)},
			read: []string{"a"},
		},
		{
			// The probe found Radar can't list Secrets in b, so b was never
			// read; its releases still can't be answered from Helm.
			name: "no user and a namespace the probe skipped",
			req:  InventoryRequest{StorageNamespaces: []string{"a"}, StorageSkipped: []string{"b"}, Scope: scopeSeeing(nil, nil)},
			read: []string{"a"},
			want: listed("b"),
		},
		{
			name:   "no user and a denial",
			req:    InventoryRequest{StorageNamespaces: []string{"a", "b"}, Scope: scopeSeeing(nil, nil)},
			read:   []string{"a"},
			denied: []string{"b"},
			want:   listed("b"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unreadStorage(tc.req, tc.read, tc.denied); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unreadStorage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadInventoryDoesNotCallAnUnansweredReadADenial(t *testing.T) {
	_, err := (&Client{}).ReadInventory(context.Background(), InventoryRequest{
		StorageNamespaces: []string{},
		User:              &pkgauth.User{Username: "dev"},
		Scope:             &ObjectScope{Access: search.ScanAccess{NamespaceCheckFailed: true}},
	})
	if !errors.Is(err, ErrAccessCheckFailed) {
		t.Fatalf("err = %v, want a retryable failure, not a denial", err)
	}

	// The caller's workload namespaces were checked, but a Secrets check behind
	// the storage scope wasn't, so a namespace left out of it isn't a denial.
	_, err = (&Client{}).ReadInventory(context.Background(), InventoryRequest{
		StorageNamespaces:  []string{"team-a"},
		StorageCheckFailed: true,
		User:               &pkgauth.User{Username: "dev"},
		Scope:              &ObjectScope{},
	})
	if !errors.Is(err, ErrAccessCheckFailed) {
		t.Fatalf("unanswered Secrets check: err = %v, want a retryable failure", err)
	}
}

func TestReadInventoryOutsideThePinIsEmptyNotDenied(t *testing.T) {
	inventory, err := (&Client{}).ReadInventory(context.Background(), InventoryRequest{
		StorageNamespaces: []string{},
		Scope:             &ObjectScope{Requested: RequestedNamespaces([]string{"team-b"}, ptr("team-a"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.StorageDenied != nil || inventory.ObjectCoverage != nil || len(inventory.Observed) != 0 {
		t.Fatalf("inventory = %+v, want nothing and no denial", inventory)
	}
}

func ptr(s string) *string { return &s }

func TestRequestedNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name  string
		named []string
		pin   *string
		want  []string
	}{
		{name: "nothing narrows it"},
		{name: "named", named: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "pin alone", pin: ptr("a"), want: []string{"a"}},
		{name: "named inside the pin", named: []string{"a", "b"}, pin: ptr("a"), want: []string{"a"}},
		{name: "named outside the pin", named: []string{"b"}, pin: ptr("a"), want: []string{}},
		{name: "a pin with no target", pin: ptr(""), want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequestedNamespaces(tc.named, tc.pin); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestObservedOutsideStorage(t *testing.T) {
	stamped := func(release, namespace string) stampedObject {
		return stampedObject{kind: "ConfigMap", namespace: namespace, name: release, release: release, releaseNS: namespace}
	}
	scan := &stampScan{
		stamped: []stampedObject{stamped("kept-after-uninstall", "readable"), stamped("api", "denied"), stamped("web", "flux-target"), stamped("cache", "elsewhere")},
		flux:    []fluxEvidence{{ref: "denied/web", releaseName: "web", releaseNamespace: "flux-target", storageNamespace: "denied"}},
	}
	names := func(rows []ObservedRelease) []string {
		out := []string{}
		for _, row := range rows {
			out = append(out, row.Namespace+"/"+row.Name)
		}
		return out
	}

	got := names(observedOutsideStorage(scan, []string{"readable", "flux-target"}, nil, nil))
	// Helm's own answer stands where its storage was read, so an object kept
	// after an uninstall doesn't resurrect the release there. A Flux release is
	// judged by where Flux records it's stored, not by its target namespace.
	want := []string{"denied/api", "elsewhere/cache", "flux-target/web"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}

	got = names(observedOutsideStorage(scan, nil, []string{"elsewhere"}, nil))
	if !reflect.DeepEqual(got, []string{"elsewhere/cache"}) {
		t.Fatalf("a namespace filter selects by the release's namespace: %v", got)
	}

	scan.stamped = append(scan.stamped, stampedObject{kind: "ClusterRole", name: "cache-reader", release: "cache", releaseNS: "elsewhere"})
	rows := observedOutsideStorage(scan, nil, []string{"elsewhere"}, nil)
	if len(rows) != 1 || rows[0].ResourceCount != 2 {
		t.Fatalf("a namespace filter must keep a release's cluster-scoped objects: %+v", rows)
	}

	// Two HelmReleases deploy flux-target/web, stored in denied and team-b. The
	// row is credited to neither, and reading flux-target's storage answers
	// for neither, so it stays.
	two := &stampScan{stamped: scan.stamped, flux: append(slices.Clone(scan.flux), fluxEvidence{ref: "team-b/web", releaseName: "web", releaseNamespace: "flux-target", storageNamespace: "team-b"})}
	if got := names(observedOutsideStorage(two, []string{"flux-target"}, nil, nil)); !slices.Contains(got, "flux-target/web") {
		t.Fatalf("a release two HelmReleases store elsewhere must stay: %v", got)
	}
	if got := names(observedOutsideStorage(two, []string{"denied", "team-b"}, nil, nil)); slices.Contains(got, "flux-target/web") {
		t.Fatalf("with both storage namespaces read, Helm's answer stands: %v", got)
	}

	// Helm returned this one from a namespace the scan can't connect it to, so
	// it would otherwise appear twice.
	got = names(observedOutsideStorage(scan, nil, nil, []HelmRelease{{Name: "api", Namespace: "denied", StorageNamespace: "flux-system"}}))
	if slices.Contains(got, "denied/api") {
		t.Fatalf("a release Helm returned must not repeat as observed: %v", got)
	}
}
