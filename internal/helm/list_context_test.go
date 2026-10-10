package helm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"

	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestHelmReleaseStorageListStopsWithItsContext(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-r.Context().Done():
			close(requestCanceled)
		case <-release:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
		}
	}))
	defer server.Close()
	defer close(release)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := helmReleaseStorageSnapshotWithClient(ctx, client, "")
		result <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Secrets list never reached the API server")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Secrets list did not return after its context was canceled")
	}
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("the API server request was not abandoned")
	}
}

func TestHelmReleaseStorageDecodingStopsWithItsContext(t *testing.T) {
	rel := helmTestRelease("api", "demo", 1, release.StatusDeployed, "Install complete")
	client := fake.NewSimpleClientset(helmReleaseSecret(t, "demo", rel, true))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := helmReleaseStorageSnapshotWithClient(ctx, client, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestHelmReleaseRowsStopWithTheirContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := helmReleaseRowsFromStorageSnapshot(ctx, &helmReleaseStorageSnapshot{
		storageNamespaces: map[string]string{},
		histories:         map[string][]HelmRevision{},
		latest:            []*release.Release{helmTestRelease("api", "demo", 1, release.StatusDeployed, "Install complete")},
	}, nil, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

// Issue detection reads operation history and Flux ownership, never resource
// health, so its rows skip the manifest parse that health needs.
func TestIssueReleaseRowsSkipResourceHealthButKeepHistory(t *testing.T) {
	failed := helmTestRelease("api", "shop", 2, release.StatusFailed, "Upgrade \"api\" failed: timed out")
	previous := helmTestRelease("api", "shop", 1, release.StatusSuperseded, "Install complete")
	client := fake.NewSimpleClientset(
		helmReleaseSecret(t, "shop", previous, false),
		helmReleaseSecret(t, "shop", failed, false),
	)

	full, err := listReleasesAcrossNamespacesWithClient(context.Background(), client, []string{"shop"}, true)
	if err != nil {
		t.Fatal(err)
	}
	issueRows, err := listReleasesAcrossNamespacesWithClient(context.Background(), client, []string{"shop"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 1 || len(issueRows) != 1 {
		t.Fatalf("rows: full=%d issue=%d, want 1 each", len(full), len(issueRows))
	}
	if full[0].ResourceHealth == "" {
		t.Fatal("full rows lost resource health")
	}
	if issueRows[0].ResourceHealth != "" {
		t.Fatalf("issue rows computed resource health %q", issueRows[0].ResourceHealth)
	}
	if issueRows[0].LastOperation == nil || full[0].LastOperation == nil {
		t.Fatalf("operation history missing: full=%v issue=%v", full[0].LastOperation, issueRows[0].LastOperation)
	}
	if *issueRows[0].LastOperation != *full[0].LastOperation {
		t.Fatalf("issue rows analysed history differently: %+v vs %+v", *issueRows[0].LastOperation, *full[0].LastOperation)
	}
}

func TestListReleasesAcrossNamespacesSkipsForbiddenNamespaces(t *testing.T) {
	client := fake.NewSimpleClientset(
		helmReleaseSecret(t, "apps", helmTestRelease("api", "apps", 1, release.StatusDeployed, "Install complete"), false),
		helmReleaseSecret(t, "monitoring", helmTestRelease("metrics", "monitoring", 1, release.StatusDeployed, "Install complete"), false),
	)
	client.PrependReactor("list", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() != "monitoring" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("denied"))
	})

	rows, err := listReleasesAcrossNamespacesWithClient(context.Background(), client, []string{"apps", "monitoring"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "api" {
		t.Fatalf("rows = %+v, want only the readable namespace's release", rows)
	}

	_, err = listReleasesAcrossNamespacesWithClient(context.Background(), client, []string{"monitoring"}, false)
	if !IsForbiddenError(err) {
		t.Fatalf("error = %v, want forbidden when no namespace is readable", err)
	}
}

// helmStorageServer serves Helm release Secrets as a SecretList, optionally
// holding the response until released, and counts Secret list requests.
type helmStorageServer struct {
	lists    atomic.Int32
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	once     sync.Once
}

func useHelmStorageServer(t *testing.T, hold bool, secrets ...*corev1.Secret) *helmStorageServer {
	t.Helper()
	items := make([]corev1.Secret, 0, len(secrets))
	for _, secret := range secrets {
		items = append(items, *secret)
	}
	body, err := json.Marshal(corev1.SecretList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}, Items: items})
	if err != nil {
		t.Fatal(err)
	}
	s := &helmStorageServer{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	if !hold {
		close(s.release)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			_, _ = io.WriteString(w, `{"major":"1","minor":"30","gitVersion":"v1.30.0"}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/secrets") {
			http.NotFound(w, r)
			return
		}
		s.lists.Add(1)
		s.once.Do(func() { close(s.started) })
		select {
		case <-r.Context().Done():
			close(s.canceled)
		case <-s.release:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-s.release:
		default:
			close(s.release)
		}
	})
	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(clientset)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	return s
}

func TestBatchUpgradeCheckReadsHelmStorageOnce(t *testing.T) {
	rel := helmTestRelease("api", "shop", 1, release.StatusDeployed, "Install complete")
	storage := useHelmStorageServer(t, false, helmReleaseSecret(t, "shop", rel, true))
	info, err := testHelmClientWithRepos(t).BatchCheckUpgradesAcrossNamespaces(context.Background(), nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := info.Releases["shop/api"]; !ok {
		t.Fatalf("releases = %v, want shop/api", info.Releases)
	}
	if got := storage.lists.Load(); got != 1 {
		t.Fatalf("Secret lists = %d, want 1 (release list and storage namespaces from one read)", got)
	}
}

func TestBatchUpgradeCheckStopsWithItsContext(t *testing.T) {
	storage := useHelmStorageServer(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := testHelmClientWithRepos(t).BatchCheckUpgradesAcrossNamespaces(ctx, nil, "", nil)
		result <- err
	}()
	select {
	case <-storage.started:
	case <-time.After(5 * time.Second):
		t.Fatal("upgrade check never read Helm storage")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade check did not return after its context was canceled")
	}
	select {
	case <-storage.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("the Secret list was not abandoned")
	}
}
