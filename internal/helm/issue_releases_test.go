package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/issuesapi"

	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// labelledReleaseSecret is a release Secret labelled the way Helm's Secret
// driver labels it.
func labelledReleaseSecret(t *testing.T, storageNamespace string, rel *release.Release) *corev1.Secret {
	t.Helper()
	secret := helmReleaseSecret(t, storageNamespace, rel, true)
	secret.Labels = map[string]string{
		"owner":   "helm",
		"name":    rel.Name,
		"status":  rel.Info.Status.String(),
		"version": strconv.Itoa(rel.Version),
	}
	return secret
}

func secretMetadata(secrets []*corev1.Secret) []runtime.Object {
	out := make([]runtime.Object, 0, len(secrets))
	for _, secret := range secrets {
		out = append(out, &metav1.PartialObjectMetadata{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: *secret.ObjectMeta.DeepCopy(),
		})
	}
	return out
}

func metadataClientFor(t *testing.T, secrets []*corev1.Secret) *metadatafake.FakeMetadataClient {
	t.Helper()
	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return metadatafake.NewSimpleMetadataClient(scheme, secretMetadata(secrets)...)
}

// issueStorageFixture covers every status a release can end in, releases
// whose storage namespace differs from their own, and release names that
// repeat across namespaces, including two storage namespaces holding the
// same release.
func issueStorageFixture(t *testing.T) []*corev1.Secret {
	t.Helper()
	rel := func(name, namespace string, version int, status release.Status, description string) *release.Release {
		return helmTestRelease(name, namespace, version, status, description)
	}
	recent := func(r *release.Release) *release.Release {
		r.Info.LastDeployed = helmtime.Time{Time: time.Now()}
		return r
	}
	uninstalling := rel("gone", "apps", 1, release.StatusUninstalling, "Deletion in progress (or silently failed)")
	uninstalling.Info.Deleted = helmtime.Unix(10, 0)
	recentUninstall := rel("leaving", "apps", 1, release.StatusUninstalling, "Deletion in progress (or silently failed)")
	recentUninstall.Info.Deleted = helmtime.Time{Time: time.Now()}

	return []*corev1.Secret{
		// Healthy and superseded history.
		labelledReleaseSecret(t, "apps", rel("web", "apps", 1, release.StatusSuperseded, "Install complete")),
		labelledReleaseSecret(t, "apps", rel("web", "apps", 2, release.StatusDeployed, "Upgrade complete")),
		// Failed upgrade, newest revision.
		labelledReleaseSecret(t, "apps", rel("api", "apps", 1, release.StatusSuperseded, "Install complete")),
		labelledReleaseSecret(t, "apps", rel("api", "apps", 2, release.StatusFailed, "Upgrade \"api\" failed: context deadline exceeded")),
		// Failed install.
		labelledReleaseSecret(t, "apps", rel("worker", "apps", 1, release.StatusFailed, "Release \"worker\" failed: timed out")),
		// Stuck and fresh pending states.
		labelledReleaseSecret(t, "apps", rel("queue", "apps", 1, release.StatusPendingInstall, "Initial install underway")),
		labelledReleaseSecret(t, "apps", rel("billing", "apps", 1, release.StatusDeployed, "Install complete")),
		labelledReleaseSecret(t, "apps", rel("billing", "apps", 2, release.StatusPendingUpgrade, "Preparing upgrade")),
		labelledReleaseSecret(t, "apps", rel("fresh", "apps", 1, release.StatusDeployed, "Install complete")),
		labelledReleaseSecret(t, "apps", recent(rel("fresh", "apps", 2, release.StatusPendingUpgrade, "Preparing upgrade"))),
		labelledReleaseSecret(t, "apps", rel("undo", "apps", 1, release.StatusSuperseded, "Install complete")),
		labelledReleaseSecret(t, "apps", rel("undo", "apps", 2, release.StatusPendingRollback, "Rollback to 1")),
		labelledReleaseSecret(t, "apps", uninstalling),
		labelledReleaseSecret(t, "apps", recentUninstall),
		// A failed upgrade Helm rolled back, and an old failure since fixed.
		labelledReleaseSecret(t, "apps", rel("rolled", "apps", 1, release.StatusSuperseded, "Install complete")),
		labelledReleaseSecret(t, "apps", rel("rolled", "apps", 2, release.StatusFailed, "Upgrade \"rolled\" failed: timed out")),
		labelledReleaseSecret(t, "apps", rel("rolled", "apps", 3, release.StatusDeployed, "Rollback to 1")),
		labelledReleaseSecret(t, "apps", rel("fixed", "apps", 1, release.StatusFailed, "Release \"fixed\" failed: timed out")),
		labelledReleaseSecret(t, "apps", rel("fixed", "apps", 2, release.StatusDeployed, "Upgrade complete")),
		// Statuses Helm's list leaves out, or that raise nothing.
		labelledReleaseSecret(t, "apps", rel("removed", "apps", 1, release.StatusUninstalled, "Uninstallation complete")),
		labelledReleaseSecret(t, "apps", rel("odd", "apps", 1, release.StatusUnknown, "")),
		// One name in two namespaces: failed in one, healthy in the other.
		labelledReleaseSecret(t, "shop", rel("app", "shop", 1, release.StatusFailed, "Release \"app\" failed: timed out")),
		labelledReleaseSecret(t, "staging", rel("app", "staging", 1, release.StatusDeployed, "Install complete")),
		// Stored away from its own namespace (Flux storageNamespace).
		labelledReleaseSecret(t, "flux-system", rel("ingress", "edge", 1, release.StatusFailed, "Release \"ingress\" failed: timed out")),
		// The same release stored in two namespaces: a full cluster-wide read
		// groups both histories and only the newest revision counts.
		labelledReleaseSecret(t, "flux-system", rel("cache", "cache", 4, release.StatusSuperseded, "Upgrade complete")),
		labelledReleaseSecret(t, "flux-system", rel("cache", "cache", 5, release.StatusDeployed, "Upgrade complete")),
		labelledReleaseSecret(t, "cache", rel("cache", "cache", 2, release.StatusFailed, "Upgrade \"cache\" failed: timed out")),
	}
}

// raisesIssue mirrors issues.NativeHelmReleaseIssues: the rows that turn
// into an issue.
func raisesIssue(row HelmRelease) bool {
	op := row.LastOperation
	if row.ManagedByFluxHelmRelease != "" || op == nil {
		return false
	}
	switch op.Kind {
	case helmhistory.KindReleaseFailed, helmhistory.KindUpgradeFailed:
		return true
	case helmhistory.KindPending:
		return op.Status == helmhistory.StatusStuck
	}
	return false
}

func rowKey(row HelmRelease) string {
	return row.Namespace + "/" + row.StorageNamespace + "/" + row.Name
}

func issueRowKeys(rows []HelmRelease) []string {
	var keys []string
	for _, row := range rows {
		if raisesIssue(row) {
			keys = append(keys, rowKey(row))
		}
	}
	sort.Strings(keys)
	return keys
}

// The labels-first read must produce exactly the issue rows a full read of
// every release Secret produces, for every scope the handlers pass.
func TestIssueReleasesMatchAFullRead(t *testing.T) {
	secrets := issueStorageFixture(t)
	full := fake.NewSimpleClientset(secretsToObjects(secrets)...)

	scopes := map[string][]string{
		"cluster-wide":    nil,
		"every namespace": {"apps", "shop", "staging", "flux-system", "cache"},
		"one namespace":   {"cache"},
		"no releases":     {"empty"},
	}
	wantCluster := []string{
		"apps//api", "apps//billing", "apps//gone", "apps//queue", "apps//undo", "apps//worker",
		"edge/flux-system/ingress", "shop//app",
	}
	for scopeName, namespaces := range scopes {
		t.Run(scopeName, func(t *testing.T) {
			oldRows, err := listReleasesAcrossNamespacesWithClient(context.Background(), full, "", namespaces, false)
			if err != nil {
				t.Fatal(err)
			}
			newRows, err := listIssueReleasesWithClients(context.Background(), fake.NewSimpleClientset(secretsToObjects(secrets)...), metadataClientFor(t, secrets), "", namespaces)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := issueRowKeys(newRows), issueRowKeys(oldRows); !reflect.DeepEqual(got, want) {
				t.Fatalf("issue rows = %v, full read = %v", got, want)
			}
			if namespaces == nil && !reflect.DeepEqual(issueRowKeys(oldRows), wantCluster) {
				t.Fatalf("fixture issue rows = %v, want %v", issueRowKeys(oldRows), wantCluster)
			}
			// Every row the labels-first read returns is the full read's row,
			// field for field.
			oldByKey := make(map[string]HelmRelease, len(oldRows))
			for _, row := range oldRows {
				oldByKey[rowKey(row)] = row
			}
			for _, row := range newRows {
				want, ok := oldByKey[rowKey(row)]
				if !ok {
					t.Fatalf("row %s is not in the full read", rowKey(row))
				}
				if !reflect.DeepEqual(row, want) {
					t.Fatalf("row %s differs from the full read:\n got %+v\nwant %+v", rowKey(row), row, want)
				}
			}
		})
	}
}

// A release Secret without Helm's labels can't be placed by its labels, so
// that read falls back to decoding everything.
func TestIssueReleasesFallBackToAFullReadWithoutHelmLabels(t *testing.T) {
	secrets := issueStorageFixture(t)
	unlabelled := helmReleaseSecret(t, "apps", helmTestRelease("legacy", "apps", 1, release.StatusFailed, "Release \"legacy\" failed: timed out"), true)
	secrets = append(secrets, unlabelled)

	oldRows, err := listReleasesAcrossNamespacesWithClient(context.Background(), fake.NewSimpleClientset(secretsToObjects(secrets)...), "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	newRows, err := listIssueReleasesWithClients(context.Background(), fake.NewSimpleClientset(secretsToObjects(secrets)...), metadataClientFor(t, secrets), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, want := issueRowKeys(newRows), issueRowKeys(oldRows)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("issue rows = %v, full read = %v", got, want)
	}
	if !strings.Contains(strings.Join(got, ","), "apps//legacy") {
		t.Fatalf("unlabelled failed release missing: %v", got)
	}
}

// Statuses outside issueStatuses must never raise an issue, or skipping them
// would hide one.
func TestOnlyIssueStatusesRaiseIssues(t *testing.T) {
	statuses := []release.Status{
		release.StatusUnknown, release.StatusDeployed, release.StatusUninstalled, release.StatusSuperseded,
		release.StatusFailed, release.StatusUninstalling, release.StatusPendingInstall,
		release.StatusPendingUpgrade, release.StatusPendingRollback,
	}
	for _, status := range statuses {
		analysis := helmhistory.Analyze("demo", 2, []helmhistory.Revision{
			{Revision: 1, Status: release.StatusFailed.String(), Description: "Upgrade \"demo\" failed: timed out", Updated: time.Unix(1, 0)},
			{Revision: 2, Status: status.String(), Description: "Rollback to 1", Updated: time.Unix(2, 0), Deleted: time.Unix(2, 0)},
		}, helmhistory.Options{})
		raises := raisesIssue(HelmRelease{LastOperation: analysis.LastOperation})
		if raises != issueStatuses[status.String()] {
			t.Errorf("status %s: raises issue = %v, listed in issueStatuses = %v", status, raises, issueStatuses[status.String()])
		}
	}
}

// labelledStorageServer is an API server holding Helm release Secrets. It
// answers metadata-only lists with labels alone and counts the Secrets it
// sends in full.
type labelledStorageServer struct {
	secrets   []*corev1.Secret
	fullSent  atomic.Int32
	labelOnly atomic.Int32
}

func (s *labelledStorageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !strings.HasSuffix(r.URL.Path, "/secrets") {
		http.NotFound(w, r)
		return
	}
	selector, err := labels.Parse(r.URL.Query().Get("labelSelector"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	namespace := ""
	if parts := strings.Split(r.URL.Path, "/"); len(parts) > 4 && parts[3] == "namespaces" {
		namespace = parts[4]
	}
	var matched []*corev1.Secret
	for _, secret := range s.secrets {
		if (namespace == "" || secret.Namespace == namespace) && selector.Matches(labels.Set(secret.Labels)) {
			matched = append(matched, secret)
		}
	}
	if strings.Contains(r.Header.Get("Accept"), "as=PartialObjectMetadataList") {
		s.labelOnly.Add(1)
		list := metav1.PartialObjectMetadataList{TypeMeta: metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadataList"}}
		for _, secret := range matched {
			list.Items = append(list.Items, metav1.PartialObjectMetadata{
				TypeMeta:   metav1.TypeMeta{APIVersion: "meta.k8s.io/v1", Kind: "PartialObjectMetadata"},
				ObjectMeta: secret.ObjectMeta,
			})
		}
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	list := corev1.SecretList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}}
	for _, secret := range matched {
		list.Items = append(list.Items, *secret)
	}
	s.fullSent.Add(int32(len(matched)))
	_ = json.NewEncoder(w).Encode(list)
}

// Issue detection decodes only releases that can raise an issue. A full read
// pulls every revision of every release, which is what made /api/issues take
// seconds on clusters with many Helm releases.
func TestIssueReleasesReadOnlyFailedOrPendingReleasesInFull(t *testing.T) {
	storage := &labelledStorageServer{}
	for i := range 20 {
		name := fmt.Sprintf("healthy-%02d", i)
		for version := 1; version <= 3; version++ {
			status := release.StatusSuperseded
			if version == 3 {
				status = release.StatusDeployed
			}
			storage.secrets = append(storage.secrets, labelledReleaseSecret(t, "apps", helmTestRelease(name, "apps", version, status, "Upgrade complete")))
		}
	}
	storage.secrets = append(storage.secrets,
		labelledReleaseSecret(t, "apps", helmTestRelease("api", "apps", 1, release.StatusSuperseded, "Install complete")),
		labelledReleaseSecret(t, "apps", helmTestRelease("api", "apps", 2, release.StatusFailed, "Upgrade \"api\" failed: timed out")),
	)
	server := httptest.NewServer(storage)
	defer server.Close()

	client := &Client{restConfig: &rest.Config{Host: server.URL}}
	rows, err := client.ListIssueReleasesAcrossNamespaces(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := issueRowKeys(rows); !reflect.DeepEqual(got, []string{"apps//api"}) {
		t.Fatalf("issue rows = %v, want the failed release", got)
	}
	if storage.labelOnly.Load() == 0 {
		t.Fatal("release labels were never read on their own")
	}
	if got := storage.fullSent.Load(); got != 2 {
		t.Fatalf("API server sent %d release Secrets in full, want the failed release's 2 revisions out of %d", got, len(storage.secrets))
	}
}

// A namespace the caller may not read is skipped silently. Any other failure
// in one namespace keeps the releases read from the others and names the
// namespace that failed, so one bad namespace can't hide the rest.
func TestIssueReleasesSkipOnlyDeniedNamespaces(t *testing.T) {
	impersonation := apierrors.NewForbidden(schema.GroupResource{Resource: "users"}, "alice",
		fmt.Errorf(`User "system:serviceaccount:radar:radar" cannot impersonate resource "users" in API group "" at the cluster scope`))
	cases := []struct {
		name       string
		err        error
		unchecked  bool
		reason     issuesapi.UncheckedReason
		wantFailed []string
	}{
		{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", fmt.Errorf("denied"))},
		{name: "unauthorized", err: apierrors.NewUnauthorized("Unauthorized"), unchecked: true, reason: issuesapi.UncheckedReasonReadFailed, wantFailed: []string{"blocked"}},
		{name: "server error", err: apierrors.NewInternalError(fmt.Errorf("etcdserver: request timed out")), unchecked: true, reason: issuesapi.UncheckedReasonReadFailed, wantFailed: []string{"blocked"}},
		{name: "Radar may not impersonate", err: impersonation, unchecked: true, reason: issuesapi.UncheckedReasonCollectorAccessDenied, wantFailed: []string{"blocked"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secrets := []*corev1.Secret{labelledReleaseSecret(t, "open", helmTestRelease("shop", "open", 1, release.StatusFailed, "Install failed"))}
			labels := metadataClientFor(t, secrets)
			labels.PrependReactor("list", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetNamespace() == "blocked" {
					return true, nil, tc.err
				}
				return false, nil, nil
			})
			ctx := context.Background()
			releases, err := listIssueReleasesWithClients(ctx, fake.NewSimpleClientset(secretsToObjects(secrets)...), labels, "", []string{"open", "blocked"})
			if len(releases) != 1 || releases[0].Name != "shop" {
				t.Fatalf("releases = %v, want the failed release read from the open namespace (err: %v)", releases, err)
			}
			got, unchecked := IssueReadUnchecked(ctx, err)
			if unchecked != tc.unchecked {
				t.Fatalf("unchecked = %v, want %v (err: %v)", unchecked, tc.unchecked, err)
			}
			if !unchecked {
				return
			}
			want := issuesapi.UncheckedSource{Source: issuesapi.UncheckedSourceHelm, Reason: tc.reason, Namespaces: tc.wantFailed}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("unchecked = %+v, want %+v", got, want)
			}
		})
	}
}

// When no namespace was read, the whole source is unchecked, with no
// namespace list to suggest the rest was complete.
func TestIssueReleasesFailWholeWhenNoNamespaceWasRead(t *testing.T) {
	labels := metadataClientFor(t, nil)
	labels.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("Unauthorized")
	})
	ctx := context.Background()
	releases, err := listIssueReleasesWithClients(ctx, fake.NewSimpleClientset(), labels, "", []string{"a", "b"})
	if releases != nil {
		t.Fatalf("releases = %v, want none", releases)
	}
	got, unchecked := IssueReadUnchecked(ctx, err)
	want := issuesapi.UncheckedSource{Source: issuesapi.UncheckedSourceHelm, Reason: issuesapi.UncheckedReasonReadFailed}
	if !unchecked || !reflect.DeepEqual(got, want) {
		t.Fatalf("unchecked = %v %+v, want %+v", unchecked, got, want)
	}
}

// A missing impersonate permission refuses every user the same way. It is
// Radar's own access failing, not the caller's permission boundary.
func TestImpersonationDenialIsNotTheCallersDenial(t *testing.T) {
	err := apierrors.NewForbidden(schema.GroupResource{Resource: "users"}, "alice",
		fmt.Errorf(`User "system:serviceaccount:radar:radar" cannot impersonate resource "users" in API group "" at the cluster scope`))
	if ReleaseReadDeniedForCaller(err) {
		t.Fatal("impersonation denial read as the caller's own denial")
	}
	callerDenied := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", fmt.Errorf(`User "alice" cannot list resource "secrets"`))
	if !ReleaseReadDeniedForCaller(callerDenied) {
		t.Fatal("caller denial not recognised")
	}
	if ReleaseReadDeniedForCaller(apierrors.NewUnauthorized("Unauthorized")) {
		t.Fatal("401 read as the caller's own denial")
	}
}

// Open tabs and the alerts worker poll the issue stream constantly, so a
// failure that persists is logged once, and again only after a read succeeds.
func TestReportIssueReadLogsAFailureOncePerClass(t *testing.T) {
	var buf strings.Builder
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		issueReadFailureLogged.Clear()
	})
	issueReadFailureLogged.Clear()

	ctx := context.Background()
	failure := apierrors.NewInternalError(fmt.Errorf("etcdserver: request timed out"))
	for range 3 {
		ReportIssueRead(ctx, failure, "", nil, nil)
	}
	ReportIssueRead(ctx, apierrors.NewUnauthorized("Unauthorized"), "", nil, nil)
	if got := strings.Count(buf.String(), "couldn't be checked"); got != 2 {
		t.Fatalf("logged %d failures, want one per class (2):\n%s", got, buf.String())
	}
	ReportIssueRead(ctx, nil, "", nil, nil)
	ReportIssueRead(ctx, failure, "", nil, nil)
	if got := strings.Count(buf.String(), "couldn't be checked"); got != 3 {
		t.Fatalf("logged %d failures, want the failure logged again after a successful read (3):\n%s", got, buf.String())
	}
}

// One user's successful read must not reset another user's failure log: with
// payments always failing and shop always succeeding, alternating polls log
// the payments failure once.
func TestReportIssueReadSuccessOnlyClearsThatIdentity(t *testing.T) {
	var buf strings.Builder
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		issueReadFailureLogged.Clear()
	})
	issueReadFailureLogged.Clear()

	ctx := context.Background()
	failure := apierrors.NewInternalError(fmt.Errorf("etcdserver: request timed out"))
	for range 3 {
		ReportIssueRead(ctx, failure, "payments", []string{"team-payments"}, nil)
		ReportIssueRead(ctx, nil, "shop", []string{"team-shop"}, nil)
	}
	if got := strings.Count(buf.String(), "couldn't be checked"); got != 1 {
		t.Fatalf("logged %d failures, want 1:\n%s", got, buf.String())
	}
	ReportIssueRead(ctx, nil, "payments", []string{"team-payments"}, nil)
	ReportIssueRead(ctx, failure, "payments", []string{"team-payments"}, nil)
	if got := strings.Count(buf.String(), "couldn't be checked"); got != 2 {
		t.Fatalf("logged %d failures, want it logged again after payments recovered (2):\n%s", got, buf.String())
	}
}

// The same user reading a healthy namespace in one tab and a failing one in
// another logs the failure once: the healthy reads don't reset it.
func TestReportIssueReadSuccessOnlyClearsThatScope(t *testing.T) {
	var buf strings.Builder
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		issueReadFailureLogged.Clear()
	})
	issueReadFailureLogged.Clear()

	ctx := context.Background()
	failure := apierrors.NewInternalError(fmt.Errorf("etcdserver: request timed out"))
	for range 3 {
		ReportIssueRead(ctx, failure, "alice", nil, []string{"payments"})
		ReportIssueRead(ctx, nil, "alice", nil, []string{"shop"})
	}
	if got := strings.Count(buf.String(), "couldn't be checked"); got != 1 {
		t.Fatalf("logged %d failures, want 1:\n%s", got, buf.String())
	}
}

// Failed namespaces share one unchecked entry, so it may name a specific
// reason only when every namespace failed for that reason.
func TestIssueReadUncheckedMixedPartialFailuresUseTheGenericReason(t *testing.T) {
	impersonation := apierrors.NewForbidden(schema.GroupResource{Resource: "users"}, "alice", fmt.Errorf("cannot impersonate"))
	timeout := apierrors.NewInternalError(fmt.Errorf("etcdserver: request timed out"))
	ctx := context.Background()

	same, _ := IssueReadUnchecked(ctx, &PartialReadError{Namespaces: []string{"b", "a"}, Err: impersonation, Errs: []error{impersonation, impersonation}})
	if same.Reason != issuesapi.UncheckedReasonCollectorAccessDenied || !slices.Equal(same.Namespaces, []string{"a", "b"}) {
		t.Fatalf("one shared cause = %+v, want collector_access_denied for a and b", same)
	}
	mixed, _ := IssueReadUnchecked(ctx, &PartialReadError{Namespaces: []string{"a", "b"}, Err: impersonation, Errs: []error{impersonation, timeout}})
	if mixed.Reason != issuesapi.UncheckedReasonReadFailed {
		t.Fatalf("mixed causes = %+v, want the generic read_failed", mixed)
	}
}
