package helm

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
)

const helmOwnedLabelSelector = "owner=helm"

// releaseSummaries remembers the summary of each release Secret a release
// list or issue read has decoded, so each revision is decoded once rather
// than on every read. An entry holds for one Secret UID and resourceVersion:
// Helm stores each revision in its own Secret, and any change to it, such as
// a status change, gives it a new resourceVersion.
//
// Summaries are shared by every caller of the Client. A caller only gets the
// summaries of Secrets the API server just listed for it.
type releaseSummaries struct {
	mu      sync.Mutex
	entries map[types.NamespacedName]summaryEntry
	fills   map[string]chan struct{}
}

type summaryEntry struct {
	uid             types.UID
	resourceVersion string
	summary         releaseSummary
	used            time.Time
}

// summaryIdleTime is how long a summary no read uses is kept, so summaries of
// a namespace nobody reads any more, such as a deleted one, don't stay.
const summaryIdleTime = time.Hour

// summarizedRevision is one release Secret, as listed, with its summary.
type summarizedRevision struct {
	namespace string
	name      string
	labels    map[string]string
	summary   releaseSummary
}

// listSummaryPageSize bounds the release Secrets held at once while
// summarizing, each up to 1MiB.
const listSummaryPageSize = 50

// retain drops the entries in namespace ("" for all) that listed no longer
// holds, and entries anywhere that no read has used for summaryIdleTime. One
// added by a concurrent read after that list was taken may go too; a later
// read fetches it again.
func (m *releaseSummaries) retain(namespace string, listed []metav1.PartialObjectMetadata) {
	live := make(map[types.NamespacedName]bool, len(listed))
	for _, item := range listed {
		live[types.NamespacedName{Namespace: item.Namespace, Name: item.Name}] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	idle := time.Now().Add(-summaryIdleTime)
	for key, entry := range m.entries {
		if (namespace == "" || key.Namespace == namespace) && !live[key] || entry.used.Before(idle) {
			delete(m.entries, key)
		}
	}
}

func (m *releaseSummaries) get(meta metav1.ObjectMeta) (releaseSummary, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := types.NamespacedName{Namespace: meta.Namespace, Name: meta.Name}
	entry, ok := m.entries[key]
	if !ok || entry.uid != meta.UID || entry.resourceVersion != meta.ResourceVersion {
		return releaseSummary{}, false
	}
	entry.used = time.Now()
	m.entries[key] = entry
	return entry.summary, true
}

func (m *releaseSummaries) put(secret *corev1.Secret, summary releaseSummary) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[types.NamespacedName]summaryEntry)
	}
	m.entries[types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}] = summaryEntry{
		uid:             secret.UID,
		resourceVersion: secret.ResourceVersion,
		summary:         summary,
		used:            time.Now(),
	}
}

// lockFill lets one read at a time fill in the summaries of one namespace
// ("" for all), so reads that start together, as at startup, don't each
// download every release. Reads of other namespaces don't wait.
func (m *releaseSummaries) lockFill(ctx context.Context, namespace string) (unlock func(), _ error) {
	m.mu.Lock()
	if m.fills == nil {
		m.fills = make(map[string]chan struct{})
	}
	sem, ok := m.fills[namespace]
	if !ok {
		sem = make(chan struct{}, 1)
		m.fills[namespace] = sem
	}
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// summarize summarizes a release Secret fetched in full and remembers it.
func (m *releaseSummaries) summarize(secret *corev1.Secret) releaseSummary {
	summary, err := summarizeRelease(secret.Data["release"])
	if err != nil {
		log.Printf("[helm] failed to decode release secret %s/%s: %v", secret.Namespace, secret.Name, err)
	}
	m.put(secret, summary)
	return summary
}

// releaseListReader reads release storage for the release list.
func (c *Client) releaseListReader(client kubernetes.Interface, labelsClient metadata.Interface) func(context.Context, string) (*helmReleaseStorageSnapshot, error) {
	return func(ctx context.Context, namespace string) (*helmReleaseStorageSnapshot, error) {
		stored, err := c.listReleaseLabels(ctx, labelsClient, namespace)
		if err != nil {
			return nil, err
		}
		return c.summarizedSnapshot(ctx, client, namespace, stored, nil, true)
	}
}

// listReleaseLabels lists, with labelsClient (the caller's identity), the
// labels and versions of the release Secrets in namespace, without their
// releases. The API server decides access, as it does for a full read.
func (c *Client) listReleaseLabels(ctx context.Context, labelsClient metadata.Interface, namespace string) ([]metav1.PartialObjectMetadata, error) {
	list, err := labelsClient.Resource(secretsResource).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: helmOwnedLabelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect release storage namespaces: %w", err)
	}
	c.summaries.retain(namespace, list.Items)
	return list.Items, nil
}

// summarizedSnapshot returns the storage snapshot of the release Secrets in
// stored whose release is named in names (nil for all of them), the same as
// a full read of those Secrets, built from their summaries. Releases with a
// revision it has no summary for are listed again with client, the caller's
// identity, and summarized. When needResources is set, each release's newest
// revision also gets the resources its manifest declares.
func (c *Client) summarizedSnapshot(ctx context.Context, client kubernetes.Interface, namespace string, stored []metav1.PartialObjectMetadata, names map[string]bool, needResources bool) (*helmReleaseStorageSnapshot, error) {
	revisions := make([]summarizedRevision, 0, len(stored))
	var missing []metav1.PartialObjectMetadata
	for _, item := range stored {
		if names != nil && !names[item.Labels["name"]] {
			continue
		}
		summary, ok := c.summaries.get(item.ObjectMeta)
		if !ok {
			missing = append(missing, item)
		}
		revisions = append(revisions, summarizedRevision{namespace: item.Namespace, name: item.Name, labels: item.Labels, summary: summary})
	}
	if len(missing) > 0 {
		filled, err := c.fillSummaries(ctx, client, namespace, missing, names)
		if err != nil {
			return nil, err
		}
		if filled.listed == nil {
			for i := range revisions {
				if summary, ok := filled.found[types.NamespacedName{Namespace: revisions[i].namespace, Name: revisions[i].name}]; ok {
					revisions[i].summary = summary
				}
			}
		} else {
			// Every revision of a listed release comes from the list, so
			// each release is read at one point in time.
			kept := revisions[:0]
			for _, revision := range revisions {
				if !filled.listed.covers(revision.labels["name"]) {
					kept = append(kept, revision)
				}
			}
			revisions = append(kept, filled.listed.revisions...)
		}
	}
	return c.snapshotFromSummaries(ctx, client, revisions, needResources)
}

type filledSummaries struct {
	// found holds the summaries of the missing revisions when another read
	// summarized them while this one waited.
	found map[types.NamespacedName]releaseSummary
	// listed, otherwise, holds every revision of the releases that had a
	// missing one.
	listed *listedReleases
}

type listedReleases struct {
	names     map[string]bool // nil for every release the read wanted
	revisions []summarizedRevision
}

func (l *listedReleases) covers(name string) bool {
	return l.names == nil || l.names[name]
}

// fillSummaries lists again, in full, the releases that missing revisions
// belong to, and summarizes the revisions it has no summary for.
func (c *Client) fillSummaries(ctx context.Context, client kubernetes.Interface, namespace string, missing []metav1.PartialObjectMetadata, names map[string]bool) (filledSummaries, error) {
	unlock, err := c.summaries.lockFill(ctx, namespace)
	if err != nil {
		return filledSummaries{}, err
	}
	defer unlock()

	found := make(map[types.NamespacedName]releaseSummary, len(missing))
	for _, item := range missing {
		if summary, ok := c.summaries.get(item.ObjectMeta); ok {
			found[types.NamespacedName{Namespace: item.Namespace, Name: item.Name}] = summary
		}
	}
	if len(found) == len(missing) {
		return filledSummaries{found: found}, nil
	}
	// A Secret without Helm's name label can only be found by listing
	// everything the read wants.
	relist := make(map[string]bool)
	for _, item := range missing {
		name := item.Labels["name"]
		if name == "" {
			relist = names
			break
		}
		relist[name] = true
	}
	revisions, err := c.listSummaries(ctx, client, namespace, relist)
	if err != nil {
		return filledSummaries{}, err
	}
	return filledSummaries{listed: &listedReleases{names: relist, revisions: revisions}}, nil
}

// getReleaseSecret reads one release Secret, or nil when it is gone. It lists
// by name rather than getting the Secret, so it needs the same permission as
// the rest of the read: list Secrets.
func getReleaseSecret(ctx context.Context, client kubernetes.Interface, namespace, name string) (*corev1.Secret, error) {
	list, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", name).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read release Secret %s/%s: %w", namespace, name, err)
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return &list.Items[i], nil
		}
	}
	return nil, nil
}

// listSummaries lists the release Secrets in namespace whose release is
// named in names (nil for all), a page at a time, and summarizes those it
// has no summary for.
func (c *Client) listSummaries(ctx context.Context, client kubernetes.Interface, namespace string, names map[string]bool) ([]summarizedRevision, error) {
	selectors, err := releaseSecretSelectors(names)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	var out []summarizedRevision
	summarized := 0
	for _, selector := range selectors {
		opts := metav1.ListOptions{LabelSelector: selector, Limit: listSummaryPageSize}
		for {
			page, err := client.CoreV1().Secrets(namespace).List(ctx, opts)
			if err != nil {
				return nil, fmt.Errorf("failed to inspect release storage namespaces: %w", err)
			}
			summaries := make([]releaseSummary, len(page.Items))
			var g errgroup.Group
			g.SetLimit(summaryWorkers())
			for i := range page.Items {
				secret := &page.Items[i]
				if summary, ok := c.summaries.get(secret.ObjectMeta); ok {
					summaries[i] = summary
					continue
				}
				summarized++
				g.Go(func() error {
					if err := ctx.Err(); err != nil {
						return err
					}
					summaries[i] = c.summaries.summarize(secret)
					return nil
				})
			}
			if err := g.Wait(); err != nil {
				return nil, err
			}
			for i := range page.Items {
				meta := page.Items[i].ObjectMeta
				out = append(out, summarizedRevision{namespace: meta.Namespace, name: meta.Name, labels: meta.Labels, summary: summaries[i]})
			}
			if page.Continue == "" {
				break
			}
			opts.Continue = page.Continue
		}
	}
	if summarized > 0 {
		log.Printf("[helm] Summarized %d of %d release Secrets in %v", summarized, len(out), time.Since(start).Round(time.Millisecond))
	}
	return out, nil
}

// releaseSecretSelectors selects the release Secrets of the releases named
// in names, or all of them for nil, a bounded number of names per selector.
func releaseSecretSelectors(names map[string]bool) ([]string, error) {
	if names == nil {
		return []string{helmOwnedLabelSelector}, nil
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	slices.Sort(sorted)
	owned, err := labels.NewRequirement("owner", selection.Equals, []string{"helm"})
	if err != nil {
		return nil, fmt.Errorf("failed to select release Secrets: %w", err)
	}
	var selectors []string
	for chunk := range slices.Chunk(sorted, issueReleaseNamesPerList) {
		named, err := labels.NewRequirement("name", selection.In, chunk)
		if err != nil {
			return nil, fmt.Errorf("failed to select release Secrets: %w", err)
		}
		selectors = append(selectors, labels.NewSelector().Add(*owned, *named).String())
	}
	return selectors, nil
}

func summaryWorkers() int {
	return min(runtime.GOMAXPROCS(0), 4)
}

// snapshotFromSummaries groups revisions by release as a full read does.
// Releases too large to summarize, and a newest revision whose resources
// weren't recorded when needResources is set, are read in full with client.
func (c *Client) snapshotFromSummaries(ctx context.Context, client kubernetes.Interface, revisions []summarizedRevision, needResources bool) (*helmReleaseStorageSnapshot, error) {
	stored := make([]storedRevision, 0, len(revisions))
	summarized := make(map[*release.Release]summarizedRevision, len(revisions))
	for _, revision := range revisions {
		switch {
		case revision.summary.skip:
			continue
		case revision.summary.tooLarge:
			rel, err := readReleaseSecret(ctx, client, revision.namespace, revision.name)
			if err != nil {
				return nil, err
			}
			if rel != nil {
				stored = append(stored, storedRevision{storageNamespace: revision.namespace, release: rel})
			}
		default:
			rel := revision.summary.releaseWithLabels(revision.labels)
			stored = append(stored, storedRevision{storageNamespace: revision.namespace, release: rel})
			summarized[rel] = revision
		}
	}
	snapshot, err := helmReleaseStorageSnapshotFromRevisions(ctx, stored)
	if err != nil {
		return nil, err
	}
	snapshot.latestResources = make(map[*release.Release][]OwnedResource, len(snapshot.latest))
	for _, rel := range snapshot.latest {
		revision, ok := summarized[rel]
		if !ok {
			// Read in full; its manifest is there.
			continue
		}
		if revision.summary.resourcesRecorded {
			snapshot.latestResources[rel] = revision.summary.ownedResources(rel.Namespace)
			continue
		}
		if !needResources {
			continue
		}
		full, err := readReleaseSecret(ctx, client, revision.namespace, revision.name)
		if err != nil {
			return nil, err
		}
		var manifest string
		if full != nil {
			manifest = full.Manifest
		}
		snapshot.latestResources[rel] = parseManifestResources(manifest, rel.Namespace)
	}
	return snapshot, nil
}

// readReleaseSecret reads one release Secret in full, as a full read of the
// namespace would decode it. It returns nil for a Secret that is gone or
// that a full read skips.
func readReleaseSecret(ctx context.Context, client kubernetes.Interface, namespace, name string) (*release.Release, error) {
	secret, err := getReleaseSecret(ctx, client, namespace, name)
	if err != nil || secret == nil {
		return nil, err
	}
	encoded := secret.Data["release"]
	if len(encoded) == 0 {
		return nil, nil
	}
	rel, err := decodeHelmReleaseData(string(encoded))
	if err != nil {
		log.Printf("[helm] failed to decode release secret %s/%s: %v", secret.Namespace, secret.Name, err)
		return nil, nil
	}
	rel.Labels = secret.Labels
	return rel, nil
}
