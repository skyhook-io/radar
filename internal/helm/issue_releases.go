package helm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/helmhistory"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
// releases whose newest revision is failed or pending are read, along with any
// release sharing their name in the same read so revisions are grouped exactly
// as a full read groups them. Every other release is left out: it cannot raise
// an issue.
func (c *Client) ListIssueReleasesAcrossNamespaces(ctx context.Context, namespaces []string, username string, groups []string) ([]HelmRelease, error) {
	if namespaces != nil && len(namespaces) == 0 {
		return []HelmRelease{}, nil
	}
	contextName := k8s.GetContextName()
	client, labelsClient, err := c.releaseStorageClients(username, groups)
	if err != nil {
		return nil, err
	}
	return c.listIssueReleasesWithClients(ctx, client, labelsClient, contextName, namespaces)
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
func (c *Client) listIssueReleasesWithClients(ctx context.Context, client kubernetes.Interface, labelsClient metadata.Interface, contextName string, namespaces []string) ([]HelmRelease, error) {
	return listReleaseRows(ctx, contextName, namespaces, false, isReleaseReadForbidden, c.issueReleaseReader(client, labelsClient, contextName))
}

// issueReleaseReader reads release storage for issue detection: only the
// releases that can raise an issue, without resource health.
func (c *Client) issueReleaseReader(client kubernetes.Interface, labelsClient metadata.Interface, contextName string) func(context.Context, string) (*helmReleaseStorageSnapshot, error) {
	return func(ctx context.Context, namespace string) (*helmReleaseStorageSnapshot, error) {
		stored, err := c.listReleaseLabels(ctx, labelsClient, namespace)
		if err != nil {
			return nil, err
		}
		names, skipped := issueReleaseNames(stored)
		snapshot, err := c.summarizedSnapshot(ctx, client, namespace, stored, names, false)
		if err != nil {
			return nil, err
		}
		forgetUninstallStarts(contextName, skipped)
		return snapshot, nil
	}
}

// releaseStorageClients builds the label and Secret clients from one config,
// so a context switch between the two lists can't send them to different
// clusters. Like storageClient, it uses c's own rest.Config when it has one,
// else Radar's current cluster.
func (c *Client) releaseStorageClients(username string, groups []string) (kubernetes.Interface, metadata.Interface, error) {
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

// issueReleaseNames returns the names of the releases in stored that can
// raise an issue: those whose newest revision is failed or pending. Reading
// every revision of each, in every storage namespace of the read, groups
// revisions exactly as a full read groups them. It also returns the releases
// it leaves out. It returns nil names, for a read of every release, when a
// release Secret lacks Helm's labels.
func issueReleaseNames(stored []metav1.PartialObjectMetadata) (map[string]bool, []storedRelease) {
	newest := make(map[string]storedRelease)
	for _, item := range stored {
		name, status := item.Labels["name"], item.Labels["status"]
		revision, err := strconv.Atoi(item.Labels["version"])
		if name == "" || status == "" || err != nil {
			return nil, nil
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

	names := make(map[string]bool)
	for _, rel := range newest {
		if issueStatuses[rel.status] {
			names[rel.name] = true
		}
	}
	var skipped []storedRelease
	for _, rel := range newest {
		if !names[rel.name] {
			skipped = append(skipped, rel)
		}
	}
	return names, skipped
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
