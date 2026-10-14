package helm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/helmhistory"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
)

// issueStatuses are the statuses a release's newest revision must have for
// helmhistory.Analyze to report a live operation, which is the only operation
// a Helm issue is raised from. Any other newest revision raises no issue.
var issueStatuses = map[string]bool{
	release.StatusFailed.String():          true,
	release.StatusPendingInstall.String():  true,
	release.StatusPendingUpgrade.String():  true,
	release.StatusPendingRollback.String(): true,
	release.StatusUninstalling.String():    true,
}

// issueReleaseNamesPerList bounds the length of one `name in (...)` selector.
const issueReleaseNamesPerList = 100

var secretsResource = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// ListIssueReleasesAcrossNamespaces returns the releases that issue detection
// needs, under the same namespace and permission rules as
// ListReleasesAcrossNamespaces, without per-release resource health.
//
// Helm labels every release Secret with the release name, revision and
// status. Those labels are read first, without the encoded releases, and only
// releases whose newest revision is failed or pending are read in full, along
// with any release sharing their name in the same read so revisions are
// grouped exactly as a full read groups them. Every other release is left
// out: it cannot raise an issue.
func (c *Client) ListIssueReleasesAcrossNamespaces(ctx context.Context, namespaces []string, username string, groups []string) ([]HelmRelease, error) {
	if namespaces != nil && len(namespaces) == 0 {
		return []HelmRelease{}, nil
	}
	contextName := k8s.GetContextName()
	client, labelsClient, err := c.issueStorageClients(username, groups)
	if err != nil {
		return nil, err
	}
	return listIssueReleasesWithClients(ctx, client, labelsClient, contextName, namespaces)
}

// IssueReadDenied reports whether a failed ListIssueReleasesAcrossNamespaces
// was Kubernetes refusing the caller's release Secret list. A 401 is not: it
// is Radar's own credentials failing, not the caller's permissions.
func IssueReadDenied(err error) bool {
	return err != nil && isReleaseReadForbidden(err)
}

// IssueReadUnchecked reports whether a failed ListIssueReleasesAcrossNamespaces
// leaves the caller's Helm issues unknown. A denial does not: the caller may
// not read release Secrets, so it sees no Helm issues, like any other kind it
// can't list. Neither does a caller that left, since nobody gets the answer.
// Radar's own request deadline does: the caller is still waiting.
func IssueReadUnchecked(ctx context.Context, err error) bool {
	return err != nil && !IssueReadDenied(err) && !errors.Is(ctx.Err(), context.Canceled)
}

// Only a 403 skips a namespace here. A 401 in one namespace fails the whole
// read, so IssueReadUnchecked sees it even when another namespace succeeded.
func listIssueReleasesWithClients(ctx context.Context, client kubernetes.Interface, labelsClient metadata.Interface, contextName string, namespaces []string) ([]HelmRelease, error) {
	return listReleaseRows(ctx, contextName, namespaces, false, isReleaseReadForbidden, func(ctx context.Context, namespace string) (*helmReleaseStorageSnapshot, error) {
		snapshot, skipped, err := helmIssueStorageSnapshot(ctx, client, labelsClient, namespace)
		if err != nil {
			return nil, err
		}
		forgetUninstallStarts(contextName, skipped)
		return snapshot, nil
	})
}

// issueStorageClients builds the label and Secret clients from one config,
// so a context switch between the two lists can't send them to different
// clusters. Like storageClient, it uses c's own rest.Config when it has one,
// else Radar's current cluster.
func (c *Client) issueStorageClients(username string, groups []string) (kubernetes.Interface, metadata.Interface, error) {
	c.mu.RLock()
	restConfig := c.restConfig
	c.mu.RUnlock()
	if restConfig == nil {
		restConfig = k8s.GetConfig()
	}
	if restConfig == nil {
		return nil, nil, fmt.Errorf("kubernetes config not initialized for release storage lookup")
	}
	if username != "" {
		restConfig = pkgauth.ImpersonatedConfig(restConfig, username, groups)
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build client for release storage lookup: %w", err)
	}
	labelsClient, err := metadata.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build client for release storage lookup: %w", err)
	}
	return client, labelsClient, nil
}

// storedRelease is one release's revisions in one storage namespace, as its
// Secret labels describe them.
type storedRelease struct {
	namespace string
	name      string
	revision  int
	status    string
}

// helmIssueStorageSnapshot is helmReleaseStorageSnapshotWithClient limited to
// the releases that can raise an issue. It also returns the releases it
// skipped. When any release Secret lacks Helm's labels it falls back to the
// full read.
func helmIssueStorageSnapshot(ctx context.Context, client kubernetes.Interface, labelsClient metadata.Interface, namespace string) (*helmReleaseStorageSnapshot, []storedRelease, error) {
	list, err := labelsClient.Resource(secretsResource).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "owner=helm",
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to inspect release storage namespaces: %w", err)
	}
	newest := make(map[string]storedRelease)
	for _, item := range list.Items {
		name, status := item.Labels["name"], item.Labels["status"]
		revision, err := strconv.Atoi(item.Labels["version"])
		if name == "" || status == "" || err != nil {
			snapshot, err := helmReleaseStorageSnapshotWithClient(ctx, client, namespace)
			return snapshot, nil, err
		}
		// Helm keys a release's history by storage namespace and name. A
		// full read keys it by the release's own namespace instead, which
		// differs only if one stored history holds revisions installed
		// into two namespaces.
		key := item.Namespace + "/" + name
		if seen, ok := newest[key]; !ok || seen.revision <= revision {
			newest[key] = storedRelease{namespace: item.Namespace, name: name, revision: revision, status: status}
		}
	}

	read := make(map[string]bool)
	for _, rel := range newest {
		if issueStatuses[rel.status] {
			read[rel.name] = true
		}
	}
	var skipped []storedRelease
	for _, rel := range newest {
		if !read[rel.name] {
			skipped = append(skipped, rel)
		}
	}

	names := make([]string, 0, len(read))
	for name := range read {
		names = append(names, name)
	}
	slices.Sort(names)
	var secrets []corev1.Secret
	for chunk := range slices.Chunk(names, issueReleaseNamesPerList) {
		named, err := labels.NewRequirement("name", selection.In, chunk)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to select release Secrets: %w", err)
		}
		owned, err := labels.NewRequirement("owner", selection.Equals, []string{"helm"})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to select release Secrets: %w", err)
		}
		page, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.NewSelector().Add(*owned, *named).String(),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to inspect release storage namespaces: %w", err)
		}
		secrets = append(secrets, page.Items...)
	}
	snapshot, err := helmReleaseStorageSnapshotFromSecrets(ctx, secrets)
	if err != nil {
		return nil, nil, err
	}
	return snapshot, skipped, nil
}

// forgetUninstallStarts does for skipped releases what analyzeReleaseHistory
// does for a release whose newest revision is not uninstalling: it clears the
// release's uninstall start.
func forgetUninstallStarts(contextName string, skipped []storedRelease) {
	if len(skipped) == 0 || contextName != k8s.GetContextName() {
		return
	}
	now := time.Now()
	for _, rel := range skipped {
		// A full read never analyses a release outside Helm's list states.
		if action.ListAll&action.ListAll.FromName(rel.status) == 0 {
			continue
		}
		key := uninstallStartKey{context: contextName, storageNamespace: rel.namespace, name: rel.name}
		uninstallStarts.observe(key, helmhistory.Revision{Revision: rel.revision, Status: rel.status}, now)
	}
}
