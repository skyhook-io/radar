package helm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

// contextName is the cluster context the read started on; a release history
// analysed after a switch away from it records no uninstall start.
func listReleasesAcrossNamespacesWithClient(ctx context.Context, client kubernetes.Interface, contextName string, namespaces []string, includeResourceHealth bool) ([]HelmRelease, error) {
	return listReleaseRows(ctx, contextName, namespaces, includeResourceHealth, IsForbiddenError, func(ctx context.Context, namespace string) (*helmReleaseStorageSnapshot, error) {
		return helmReleaseStorageSnapshotWithClient(ctx, client, namespace)
	})
}

const summaryTestManifest = `---
# Source: web/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
---
apiVersion: v1
kind: Service
metadata:
  name: web
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: web-reader
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: web-shared
  namespace: shared
---
not: [valid yaml
`

// storedHelmSecret is a release Secret as Helm writes it: typed, labelled,
// gzipped, with the identity the API server gives it.
func storedHelmSecret(t *testing.T, storageNamespace string, rel *release.Release) *corev1.Secret {
	t.Helper()
	secret := labelledReleaseSecret(t, storageNamespace, rel)
	secret.Type = k8score.HelmReleaseSecretType
	secret.UID = types.UID(storageNamespace + "/" + secret.Name)
	secret.ResourceVersion = "1"
	return secret
}

func summaryTestSecrets(t *testing.T) []*corev1.Secret {
	t.Helper()
	withManifest := func(rel *release.Release) *release.Release {
		rel.Manifest = summaryTestManifest
		return rel
	}
	uninstalling := helmTestRelease("gone", "apps", 2, release.StatusUninstalling, "Deletion in progress")
	uninstalling.Info.Deleted = helmtime.Unix(50, 0)
	noChart := helmTestRelease("nochart", "apps", 1, release.StatusDeployed, "Install complete")
	noChart.Chart = nil

	secrets := []*corev1.Secret{
		storedHelmSecret(t, "apps", withManifest(helmTestRelease("web", "apps", 1, release.StatusSuperseded, "Install complete"))),
		storedHelmSecret(t, "apps", withManifest(helmTestRelease("web", "apps", 2, release.StatusSuperseded, "Upgrade complete"))),
		storedHelmSecret(t, "apps", withManifest(helmTestRelease("web", "apps", 3, release.StatusDeployed, "Upgrade complete"))),
		storedHelmSecret(t, "flux-system", helmTestRelease("atomic", "demo", 1, release.StatusSuperseded, "Install complete")),
		storedHelmSecret(t, "flux-system", helmTestRelease("atomic", "demo", 2, release.StatusFailed, `Upgrade "atomic" failed: timed out waiting for the condition`)),
		storedHelmSecret(t, "flux-system", withManifest(helmTestRelease("atomic", "demo", 3, release.StatusDeployed, "Rollback to 1"))),
		storedHelmSecret(t, "apps", helmTestRelease("stuck", "apps", 1, release.StatusPendingUpgrade, "Preparing upgrade")),
		storedHelmSecret(t, "apps", uninstalling),
		storedHelmSecret(t, "apps", noChart),
		// Newest revision superseded: its resources weren't recorded, so a
		// read that needs them reads it in full.
		storedHelmSecret(t, "apps", withManifest(helmTestRelease("only", "apps", 1, release.StatusSuperseded, "Install complete"))),
		// Too large to summarize: read in full every time.
		storedHelmSecret(t, "apps", withManifest(helmTestRelease("large", "apps", 1, release.StatusFailed, strings.Repeat("x", maxSummarySize+1)))),
	}
	undecodable := storedHelmSecret(t, "apps", helmTestRelease("broken", "apps", 1, release.StatusDeployed, ""))
	undecodable.Data["release"] = []byte("not a release")
	noValue := storedHelmSecret(t, "apps", helmTestRelease("empty", "apps", 1, release.StatusDeployed, ""))
	delete(noValue.Data, "release")
	// A newer revision a full read rejects for a wrongly typed field must not
	// hide the failed revision below it.
	mistyped := storedHelmSecret(t, "apps", helmTestRelease("stuck", "apps", 2, release.StatusDeployed, "Upgrade complete"))
	mistyped.Data["release"] = mistypedReleaseValue(t, helmTestRelease("stuck", "apps", 2, release.StatusDeployed, "Upgrade complete"))
	// Labelled like a release but not of Helm's type. A full read decodes it
	// anyway.
	untyped := storedHelmSecret(t, "apps", withManifest(helmTestRelease("untyped", "apps", 1, release.StatusDeployed, "Install complete")))
	untyped.Type = corev1.SecretTypeOpaque
	return append(secrets, undecodable, noValue, mistyped, untyped)
}

// mistypedReleaseValue encodes rel with config set to a string, which Helm's
// release type refuses.
func mistypedReleaseValue(t *testing.T, rel *release.Release) []byte {
	t.Helper()
	payload, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	fields["config"] = "invalid"
	payload, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(base64.StdEncoding.EncodeToString(payload))
}

// webDeployment makes resource health depend on the release's resource list:
// a full read and a summarized read only agree if both find this Deployment.
func webDeployment(namespace string) *appsv1.Deployment {
	replicas := int32(2)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: namespace, Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 2, ReadyReplicas: 0, UnavailableReplicas: 2},
	}
}

// initDeploymentCache gives resource health a Deployment to find in each of
// namespaces.
func initDeploymentCache(t *testing.T, namespaces ...string) {
	t.Helper()
	client := fake.NewSimpleClientset()
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	for _, ns := range namespaces {
		if _, err := client.AppsV1().Deployments(ns).Create(context.Background(), webDeployment(ns), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, ns := range namespaces {
		for k8s.GetResourceCache().GetResourceStatus("Deployment", ns, "web") == nil {
			if time.Now().After(deadline) {
				t.Fatal("Deployments never reached the cache")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// releaseStorage is release storage as the API server holds it: a Secret client
// and a labels-only client over the same Secrets.
type releaseStorage struct {
	secrets *fake.Clientset
	labels  metadata.Interface
}

func newReleaseStorage(t *testing.T, secrets []*corev1.Secret) releaseStorage {
	t.Helper()
	return releaseStorage{secrets: fake.NewSimpleClientset(secretsToObjects(secrets)...), labels: metadataClientFor(t, secrets)}
}

// fullSecretReads counts the Secrets requests that return release values:
// reads of one Secret by name, and lists.
func (s releaseStorage) fullSecretReads() (gets, lists int) {
	for _, action := range s.secrets.Actions() {
		list, ok := action.(clienttesting.ListAction)
		if !ok || action.GetResource().Resource != "secrets" {
			continue
		}
		if list.GetListRestrictions().Fields.Empty() {
			lists++
		} else {
			gets++
		}
	}
	return gets, lists
}

func summarizedRows(t *testing.T, c *Client, s releaseStorage, namespaces []string, withHealth bool) []HelmRelease {
	t.Helper()
	rows, err := listReleaseRows(context.Background(), "", namespaces, withHealth, IsForbiddenError, c.releaseListReader(s.secrets, s.labels))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func fullReadRows(t *testing.T, secrets []*corev1.Secret, namespaces []string, withHealth bool) []HelmRelease {
	t.Helper()
	rows, err := listReleasesAcrossNamespacesWithClient(context.Background(), fake.NewSimpleClientset(secretsToObjects(secrets)...), "", namespaces, withHealth)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func rowHealthKnown(rows []HelmRelease) bool {
	for _, row := range rows {
		if row.HealthSummary != "" {
			return true
		}
	}
	return false
}

func TestSummarizedReleaseRowsMatchFullRead(t *testing.T) {
	initDeploymentCache(t, "apps", "demo")
	secrets := summaryTestSecrets(t)

	for _, namespaces := range [][]string{nil, {"apps"}, {"flux-system"}, {"apps", "flux-system"}} {
		for _, withHealth := range []bool{true, false} {
			t.Run(fmt.Sprintf("ns=%v health=%v", namespaces, withHealth), func(t *testing.T) {
				// Each read starts with no remembered uninstall starts, so the
				// next can't borrow one's.
				freshUninstallStarts(t)
				want := fullReadRows(t, secrets, namespaces, withHealth)
				if len(want) == 0 {
					t.Fatal("full read returned no rows; the fixture is wrong")
				}
				if withHealth && namespaces == nil && !rowHealthKnown(want) {
					t.Fatal("no row has known resource health; the fixture is wrong")
				}

				c := &Client{}
				s := newReleaseStorage(t, secrets)
				for _, read := range []string{"first", "second"} {
					freshUninstallStarts(t)
					got := summarizedRows(t, c, s, namespaces, withHealth)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("%s read differs from a full read\n got: %+v\nwant: %+v", read, got, want)
					}
				}
			})
		}
	}
}

// Once a revision is summarized, reads fetch only what they can't summarize:
// the release too large to keep, and a newest revision without recorded
// resources when health needs them.
func TestSummarizedReadsDecodeEachRevisionOnce(t *testing.T) {
	secrets := summaryTestSecrets(t)
	c := &Client{}
	s := newReleaseStorage(t, secrets)

	summarizedRows(t, c, s, nil, true)
	if gets, lists := s.fullSecretReads(); gets+lists == 0 {
		t.Fatal("first read fetched nothing")
	}
	s.secrets.ClearActions()
	summarizedRows(t, c, s, nil, true)
	// "large" every time, "only" for its resources.
	if gets, lists := s.fullSecretReads(); gets != 2 || lists != 0 {
		t.Fatalf("second read: %d gets, %d lists, want only the 2 that can't be summarized", gets, lists)
	}
}

func TestSummarizedReadsFollowNewRevisions(t *testing.T) {
	secrets := summaryTestSecrets(t)
	c := &Client{}
	s := newReleaseStorage(t, secrets)
	summarizedRows(t, c, s, []string{"apps"}, false)

	failed := storedHelmSecret(t, "apps", helmTestRelease("web", "apps", 4, release.StatusFailed, `Upgrade "web" failed: boom`))
	if _, err := s.secrets.CoreV1().Secrets("apps").Create(context.Background(), failed, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, failed)
	s.labels = metadataClientFor(t, secrets)
	s.secrets.ClearActions()

	rows := summarizedRows(t, c, s, []string{"apps"}, false)
	// A list of web's revisions, plus "large" and "only" as on every read.
	if gets, lists := s.fullSecretReads(); gets != 2 || lists != 1 {
		t.Fatalf("%d gets, %d lists, want a list of web's revisions and a get of the 2 that can't be summarized", gets, lists)
	}
	if want := fullReadRows(t, secrets, []string{"apps"}, false); !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows differ from a full read\n got: %+v\nwant: %+v", rows, want)
	}
	for _, row := range rows {
		if row.Name == "web" {
			if row.Revision != 4 || row.Status != "failed" || row.LastOperation == nil {
				t.Fatalf("web row = revision %d status %s, want the new failed revision", row.Revision, row.Status)
			}
			return
		}
	}
	t.Fatal("web row missing")
}

// Helm rewrites a revision's Secret in place when its status changes, as when
// a later upgrade supersedes it.
func TestSummarizedReadsFollowRewrittenRevisions(t *testing.T) {
	secrets := summaryTestSecrets(t)
	c := &Client{}
	s := newReleaseStorage(t, secrets)
	summarizedRows(t, c, s, []string{"apps"}, true)

	failed := storedHelmSecret(t, "apps", helmTestRelease("web", "apps", 3, release.StatusFailed, `Upgrade "web" failed: boom`))
	failed.ResourceVersion = "2"
	for i, secret := range secrets {
		if secret.Name == failed.Name {
			secrets[i] = failed
		}
	}
	s = newReleaseStorage(t, secrets)

	rows := summarizedRows(t, c, s, []string{"apps"}, true)
	if want := fullReadRows(t, secrets, []string{"apps"}, true); !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows differ from a full read\n got: %+v\nwant: %+v", rows, want)
	}
	for _, row := range rows {
		if row.Name == "web" && row.Status != "failed" {
			t.Fatalf("web status = %s, want the rewritten revision's", row.Status)
		}
	}
}

// One release stored in two namespaces: a full read keeps whichever revision
// it lists last, so a read must build in list order even when only some of
// the revisions were summarized before.
func TestSummarizedReadsKeepListOrder(t *testing.T) {
	secrets := []*corev1.Secret{
		storedHelmSecret(t, "store-a", helmTestRelease("dup", "demo", 1, release.StatusDeployed, "Install complete")),
		storedHelmSecret(t, "store-b", helmTestRelease("dup", "demo", 1, release.StatusFailed, "Install failed")),
	}
	want := fullReadRows(t, secrets, nil, false)
	c := &Client{}
	summarizedRows(t, c, newReleaseStorage(t, secrets), []string{"store-b"}, false)
	if got := summarizedRows(t, c, newReleaseStorage(t, secrets), nil, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows differ from a full read\n got: %+v\nwant: %+v", got, want)
	}
}

// A Secret that changes between the labels list and the list of its release
// must not mix two versions of the release into one answer.
func TestSummarizedReadListsWhenASecretChangedAfterTheLabels(t *testing.T) {
	secrets := summaryTestSecrets(t)
	upgraded := storedHelmSecret(t, "apps", helmTestRelease("web", "apps", 4, release.StatusPendingUpgrade, "Preparing upgrade"))
	// The labels list saw revision 4 pending; by the time it is fetched,
	// Helm has finished the upgrade.
	labelled := append(secrets[:len(secrets):len(secrets)], upgraded)
	deployed := storedHelmSecret(t, "apps", helmTestRelease("web", "apps", 4, release.StatusDeployed, "Upgrade complete"))
	deployed.ResourceVersion = "2"
	current := append(secrets[:len(secrets):len(secrets)], deployed)

	c := &Client{}
	s := newReleaseStorage(t, secrets)
	summarizedRows(t, c, s, []string{"apps"}, false)
	s = releaseStorage{secrets: fake.NewSimpleClientset(secretsToObjects(current)...), labels: metadataClientFor(t, labelled)}

	rows := summarizedRows(t, c, s, []string{"apps"}, false)
	if _, lists := s.fullSecretReads(); lists != 1 {
		t.Fatalf("%d lists, want one list of the changed release", lists)
	}
	if want := fullReadRows(t, current, []string{"apps"}, false); !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows differ from a full read of current storage\n got: %+v\nwant: %+v", rows, want)
	}
}

func TestSummarizedReadWhenTheLastSecretIsGone(t *testing.T) {
	secret := storedHelmSecret(t, "apps", helmTestRelease("web", "apps", 1, release.StatusDeployed, "Install complete"))
	// Listed by labels, deleted before its release is read.
	s := releaseStorage{secrets: fake.NewSimpleClientset(), labels: metadataClientFor(t, []*corev1.Secret{secret})}
	if rows := summarizedRows(t, &Client{}, s, []string{"apps"}, true); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// A read that is fetching one namespace's releases doesn't hold up a read of
// another.
func TestSummarizedReadsOfOtherNamespacesDontWait(t *testing.T) {
	storage := &labelledStorageServer{}
	for _, ns := range []string{"slow", "fast"} {
		storage.secrets = append(storage.secrets, storedHelmSecret(t, ns, helmTestRelease("web", ns, 1, release.StatusDeployed, "Install complete")))
	}
	stalled, unstall := make(chan struct{}), make(chan struct{})
	var once sync.Once
	storage.hold = func(namespace string) {
		if namespace == "slow" {
			once.Do(func() { close(stalled) })
			<-unstall
		}
	}
	server := httptest.NewServer(storage)
	defer server.Close()
	defer close(unstall)
	c := &Client{restConfig: &rest.Config{Host: server.URL}}

	go func() { _, _ = c.ListReleasesAcrossNamespaces(context.Background(), []string{"slow"}, "", nil) }()
	<-stalled
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := c.ListReleasesAcrossNamespaces(ctx, []string{"fast"}, "", nil)
	if err != nil {
		t.Fatalf("read of another namespace: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
}

func TestSummarizedReadsForgetDeletedReleases(t *testing.T) {
	secrets := summaryTestSecrets(t)
	c := &Client{}
	summarizedRows(t, c, newReleaseStorage(t, secrets), nil, false)
	before := len(c.summaries.entries)

	var kept []*corev1.Secret
	for _, secret := range secrets {
		if secret.Labels["name"] != "web" {
			kept = append(kept, secret)
		}
	}
	// A read of another namespace forgets nothing outside it.
	summarizedRows(t, c, newReleaseStorage(t, kept), []string{"flux-system"}, false)
	if got := len(c.summaries.entries); got != before {
		t.Fatalf("a flux-system read left %d summaries, want all %d", got, before)
	}
	summarizedRows(t, c, newReleaseStorage(t, kept), []string{"apps"}, false)
	if got, want := len(c.summaries.entries), before-3; got != want {
		t.Fatalf("%d summaries after web's 3 revisions were deleted, want %d", got, want)
	}
}

// Kubernetes refusing the caller's labels list is the whole answer for that
// namespace: nothing is read or served from summaries there.
func TestSummarizedReadsSkipDeniedNamespaces(t *testing.T) {
	secrets := summaryTestSecrets(t)
	c := &Client{}
	summarizedRows(t, c, newReleaseStorage(t, secrets), nil, false)

	s := newReleaseStorage(t, secrets)
	labels := metadataClientFor(t, secrets)
	labels.PrependReactor("list", "secrets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "apps" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", fmt.Errorf("denied"))
		}
		return false, nil, nil
	})
	s.labels = labels

	rows := summarizedRows(t, c, s, []string{"apps", "flux-system"}, false)
	for _, row := range rows {
		if row.StorageNamespace != "flux-system" {
			t.Fatalf("row %s/%s served from a namespace the caller may not list", row.Namespace, row.Name)
		}
	}
	if len(rows) == 0 {
		t.Fatal("no rows from the namespace the caller may list")
	}
	_, err := listReleaseRows(context.Background(), "", []string{"apps"}, false, IsForbiddenError, c.releaseListReader(s.secrets, s.labels))
	if !apierrors.IsForbidden(err) {
		t.Fatalf("err = %v, want the API server's Forbidden", err)
	}
	if gets, lists := s.fullSecretReads(); gets+lists != 0 {
		t.Fatalf("read %d release Secrets in full for a caller who may list one namespace's summaries", gets+lists)
	}
}

func TestSummarizedReadStopsWithItsContext(t *testing.T) {
	storage := pagedReleaseStorage(t, 40, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storage.ServeHTTP(w, r)
		if storage.fullLists.Load() == 1 {
			cancel()
		}
	}))
	defer server.Close()
	c := &Client{restConfig: &rest.Config{Host: server.URL}}

	if _, err := c.ListReleasesAcrossNamespaces(ctx, nil, "", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if lists := storage.fullLists.Load(); lists != 1 {
		t.Fatalf("%d pages listed, want the read to stop after the first", lists)
	}
	rows, err := c.ListReleasesAcrossNamespaces(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 40 {
		t.Fatalf("%d rows after a cancelled read, want 40", len(rows))
	}
}

// pagedReleaseStorage is an API server holding many releases' revisions.
func pagedReleaseStorage(t *testing.T, releases, revisions int) *labelledStorageServer {
	t.Helper()
	storage := &labelledStorageServer{}
	for i := range releases {
		name := fmt.Sprintf("app-%03d", i)
		for version := 1; version <= revisions; version++ {
			status := release.StatusSuperseded
			if version == revisions {
				status = release.StatusDeployed
			}
			secret := storedHelmSecret(t, "apps", helmTestRelease(name, "apps", version, status, "Upgrade complete"))
			storage.secrets = append(storage.secrets, secret)
		}
	}
	return storage
}

func TestSummarizedReadListsManyNewRevisionsAPageAtATime(t *testing.T) {
	storage := pagedReleaseStorage(t, 40, 3)
	server := httptest.NewServer(storage)
	defer server.Close()
	c := &Client{restConfig: &rest.Config{Host: server.URL}}

	rows, err := c.ListReleasesAcrossNamespaces(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 40 {
		t.Fatalf("%d rows, want 40", len(rows))
	}
	total := int32(len(storage.secrets))
	pages := (total + listSummaryPageSize - 1) / listSummaryPageSize
	if storage.fullLists.Load() != pages || storage.gets.Load() != 0 || storage.fullSent.Load() != total {
		t.Fatalf("%d lists, %d gets, %d Secrets sent in full; want %d pages carrying each of %d Secrets once",
			storage.fullLists.Load(), storage.gets.Load(), storage.fullSent.Load(), pages, total)
	}
	if _, err := c.ListReleasesAcrossNamespaces(context.Background(), nil, "", nil); err != nil {
		t.Fatal(err)
	}
	if storage.fullSent.Load() != total {
		t.Fatalf("second read fetched %d more Secrets in full, want none", storage.fullSent.Load()-total)
	}
}

// A deploy that upgrades many releases at once lists only those releases.
func TestSummarizedReadListsOnlyChangedReleases(t *testing.T) {
	storage := pagedReleaseStorage(t, 40, 3)
	server := httptest.NewServer(storage)
	defer server.Close()
	c := &Client{restConfig: &rest.Config{Host: server.URL}}
	if _, err := c.ListReleasesAcrossNamespaces(context.Background(), nil, "", nil); err != nil {
		t.Fatal(err)
	}

	secrets := storage.secrets
	for i := range 25 {
		secrets = append(secrets, storedHelmSecret(t, "apps", helmTestRelease(fmt.Sprintf("app-%03d", i), "apps", 4, release.StatusDeployed, "Upgrade complete")))
	}
	storage.set(secrets)
	before := storage.fullSent.Load()
	rows, err := c.ListReleasesAcrossNamespaces(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := storage.fullSent.Load() - before; got != 25*4 {
		t.Fatalf("fetched %d Secrets in full, want the 4 revisions of each of the 25 upgraded releases", got)
	}
	for _, row := range rows {
		if row.Name < "app-025" && row.Revision != 4 {
			t.Fatalf("%s at revision %d, want 4", row.Name, row.Revision)
		}
	}
}

func TestReleaseSummariesForgetIdleEntries(t *testing.T) {
	var m releaseSummaries
	gone := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "preview-1", Name: "old", UID: "a", ResourceVersion: "1"}}
	kept := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web", UID: "b", ResourceVersion: "1"}}
	m.put(gone, releaseSummary{skip: true})
	m.put(kept, releaseSummary{skip: true})
	entry := m.entries[types.NamespacedName{Namespace: "preview-1", Name: "old"}]
	entry.used = time.Now().Add(-summaryIdleTime - time.Minute)
	m.entries[types.NamespacedName{Namespace: "preview-1", Name: "old"}] = entry

	// A read of another namespace drops what no read has used for long.
	m.retain("apps", []metav1.PartialObjectMetadata{{ObjectMeta: kept.ObjectMeta}})
	if _, ok := m.get(gone.ObjectMeta); ok {
		t.Fatal("kept a summary no read used for longer than summaryIdleTime")
	}
	if _, ok := m.get(kept.ObjectMeta); !ok {
		t.Fatal("dropped a summary in use")
	}
}

// Reads that start together, as the dashboard and the Helm page do at
// startup, fetch each release Secret once between them.
func TestConcurrentSummarizedReadsFetchEachSecretOnce(t *testing.T) {
	storage := pagedReleaseStorage(t, 40, 3)
	server := httptest.NewServer(storage)
	defer server.Close()
	c := &Client{restConfig: &rest.Config{Host: server.URL}}

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			rows, err := c.ListReleasesAcrossNamespaces(context.Background(), nil, "", nil)
			if err == nil && len(rows) != 40 {
				err = fmt.Errorf("%d rows, want 40", len(rows))
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, want := storage.fullSent.Load(), int32(len(storage.secrets)); got != want {
		t.Fatalf("4 concurrent reads fetched %d Secrets in full, want each of %d once", got, want)
	}
}

func TestSummarizedSnapshotRestoresSecretLabels(t *testing.T) {
	c := &Client{}
	s := newReleaseStorage(t, summaryTestSecrets(t))
	for range 2 {
		snapshot, err := c.releaseListReader(s.secrets, s.labels)(context.Background(), "apps")
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range snapshot.latest {
			if rel.Labels["owner"] != "helm" || rel.Labels["name"] != rel.Name {
				t.Fatalf("%s labels = %v, want the Secret's labels, as Helm restores them", rel.Name, rel.Labels)
			}
		}
	}
}
