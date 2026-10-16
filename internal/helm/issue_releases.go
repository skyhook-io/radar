package helm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/helmhistory"
	"github.com/skyhook-io/radar/pkg/issuesapi"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
//
// Unlike ListReleasesAcrossNamespaces, a namespace whose read fails does not
// discard the others: their releases come back with a *PartialReadError.
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
// is Radar's own credentials failing. Neither is Radar's ServiceAccount being
// refused impersonation of the caller.
func IssueReadDenied(err error) bool {
	return ReleaseReadDeniedForCaller(err)
}

// ReleaseReadDeniedForCaller reports whether err is Kubernetes refusing the
// caller's own read of release storage, as opposed to a failure of Radar's
// access: rejected credentials (401), or a ServiceAccount without the
// impersonate permission, which refuses every user the same way.
func ReleaseReadDeniedForCaller(err error) bool {
	return err != nil && isReleaseReadForbidden(err) && !isImpersonationDenied(err)
}

// isImpersonationDenied matches the API server refusing Radar's identity
// permission to impersonate the caller ("... cannot impersonate resource
// \"users\" ..."). The request never reached the caller's own RBAC.
func issueReadReason(err error) issuesapi.UncheckedReason {
	if isImpersonationDenied(err) {
		return issuesapi.UncheckedReasonCollectorAccessDenied
	}
	return issuesapi.UncheckedReasonReadFailed
}

func isImpersonationDenied(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cannot impersonate")
}

// IssueReadUnchecked reports what a failed ListIssueReleasesAcrossNamespaces
// leaves unchecked, if anything. A denial leaves nothing unchecked: the caller
// may not read release Secrets, so it sees no Helm issues, like any other kind
// it can't list. Neither does a caller that left, since nobody gets the
// answer. Radar's own request deadline does: the caller is still waiting. A
// *PartialReadError names the namespaces that failed; the releases returned
// with it are still valid.
func IssueReadUnchecked(ctx context.Context, err error) (issuesapi.UncheckedSource, bool) {
	if err == nil || ReleaseReadDeniedForCaller(err) || errors.Is(ctx.Err(), context.Canceled) {
		return issuesapi.UncheckedSource{}, false
	}
	unchecked := issuesapi.UncheckedSource{Source: issuesapi.UncheckedSourceHelm, Reason: issueReadReason(err)}
	var partial *PartialReadError
	if errors.As(err, &partial) {
		unchecked.Namespaces = slices.Clone(partial.Namespaces)
		slices.Sort(unchecked.Namespaces)
		// The namespaces share one entry, so a specific reason is only
		// honest when every one of them failed for it.
		for _, e := range partial.Errs {
			if issueReadReason(e) != unchecked.Reason {
				unchecked.Reason = issuesapi.UncheckedReasonReadFailed
				break
			}
		}
	}
	return unchecked, true
}

var (
	issueReadDeniedLogged  sync.Map
	issueReadFailureLogged sync.Map
)

// ReportIssueRead classifies the outcome of ListIssueReleasesAcrossNamespaces
// for the issue stream and logs a failure once per identity and failure class
// rather than once per poll: open tabs and the alerts worker poll this
// constantly. A successful read clears only the failure log for that identity
// and namespace scope, so a success elsewhere (another user, or the same user
// viewing other namespaces) can't make an ongoing failure log again, while a
// failure that comes back after recovering is logged again. A denial is
// logged once per identity.
func ReportIssueRead(ctx context.Context, err error, username string, groups []string, namespaces []string) (issuesapi.UncheckedSource, bool) {
	scope := slices.Clone(namespaces)
	slices.Sort(scope)
	readKey := pkgauth.IdentityCacheKey(username, groups) + "\x00" + strings.Join(scope, ",") + "\x00"
	if err == nil {
		issueReadFailureLogged.Range(func(k, _ any) bool {
			if key, _ := k.(string); strings.HasPrefix(key, readKey) {
				issueReadFailureLogged.Delete(k)
			}
			return true
		})
		return issuesapi.UncheckedSource{}, false
	}
	unchecked, ok := IssueReadUnchecked(ctx, err)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		// The caller went away; nobody is left to tell.
	case ReleaseReadDeniedForCaller(err):
		if _, seen := issueReadDeniedLogged.LoadOrStore(pkgauth.IdentityCacheKey(username, groups), struct{}{}); !seen {
			// Without this a cluster with no Secret-read binding would drop
			// Helm alerts with no trace anywhere.
			log.Printf("[issues] Helm release issues omitted for %q: Kubernetes denied listing release Secrets", username)
		}
	default:
		if _, seen := issueReadFailureLogged.LoadOrStore(readKey+issueReadFailureClass(err), struct{}{}); !seen {
			log.Printf("[issues] Helm releases couldn't be checked for issues (logged once until a read succeeds): %v", err)
		}
	}
	return unchecked, ok
}

func issueReadFailureClass(err error) string {
	partial := ""
	var p *PartialReadError
	if errors.As(err, &p) {
		partial = "partial:"
	}
	switch {
	case isImpersonationDenied(err):
		return partial + "impersonation"
	case apierrors.IsUnauthorized(err):
		return partial + "unauthorized"
	case errors.Is(err, context.DeadlineExceeded):
		return partial + "deadline"
	default:
		return partial + "other"
	}
}

// Only a caller denial skips a namespace here. Any other failure is recorded
// against its namespace, and the rest are still read, so one failing namespace
// can't hide the issues of the others.
func listIssueReleasesWithClients(ctx context.Context, client kubernetes.Interface, labelsClient metadata.Interface, contextName string, namespaces []string) ([]HelmRelease, error) {
	return listReleaseRows(ctx, contextName, namespaces, false, true, ReleaseReadDeniedForCaller, func(ctx context.Context, namespace string) (*helmReleaseStorageSnapshot, error) {
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
